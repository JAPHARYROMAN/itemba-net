package atlas

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// fixtureFingerprints reads Lens's fixture set, by recording name.
func fixtureFingerprints(t *testing.T) map[string]struct {
	Title        string           `json:"title"`
	Creator      string           `json:"creator"`
	MediaSeconds float64          `json:"mediaSeconds"`
	Fingerprint  lens.Fingerprint `json:"fingerprint"`
} {
	t.Helper()
	data, err := os.ReadFile("../lens/testdata/fingerprints.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Recordings []struct {
			Name         string           `json:"name"`
			Title        string           `json:"title"`
			Creator      string           `json:"creator"`
			MediaSeconds float64          `json:"mediaSeconds"`
			Fingerprint  lens.Fingerprint `json:"fingerprint"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	out := map[string]struct {
		Title        string           `json:"title"`
		Creator      string           `json:"creator"`
		MediaSeconds float64          `json:"mediaSeconds"`
		Fingerprint  lens.Fingerprint `json:"fingerprint"`
	}{}
	for _, r := range fx.Recordings {
		out[r.Name] = struct {
			Title        string           `json:"title"`
			Creator      string           `json:"creator"`
			MediaSeconds float64          `json:"mediaSeconds"`
			Fingerprint  lens.Fingerprint `json:"fingerprint"`
		}{r.Title, r.Creator, r.MediaSeconds, r.Fingerprint}
	}
	return out
}

// TestFingerprintsDecideARecordingsObject proves the fingerprint library
// on the fixture set: a reissue under a bare title joins the album by its
// sound, a different take under the album's own tags is kept apart as an
// object of its own, a radio edit with the album's beginning on a shorter
// file is kept apart too, a re-sampled listing stays where it is, a listing
// with no fingerprint joins by its tags as before, the nearest lookup finds
// the stored fingerprints, and every verdict comes back with the location.
func TestFingerprintsDecideARecordingsObject(t *testing.T) {
	store, ctx := liveStore(t)
	fx := fixtureFingerprints(t)
	// Seeded at the present, so the rendered evidence never ages past its
	// freshness window and reads one level down.
	observed := time.Now().UTC()
	listing := func(name, title, creator, url string, withFingerprint bool) Observation {
		r, ok := fx[name]
		if !ok {
			t.Fatalf("no fixture %s", name)
		}
		l := Listing{Kind: lens.TypeAudio, Title: title, Creator: creator, URL: url, Provider: "Internet Archive", AccessType: "free", Duration: strconv.FormatFloat(r.MediaSeconds, 'f', -1, 64)}
		if withFingerprint {
			l.Fingerprint = &FingerprintObservation{Fingerprint: r.Fingerprint, MediaSeconds: r.MediaSeconds, ObservedAt: observed}
		}
		return Observation{Listing: l, Evidence: []Evidence{{Level: lens.LevelRendered, Method: "isolated_audio_sample", Status: "started", Egress: "TZ", ObservedAt: observed}}}
	}
	q := lens.ParseAs("So What by Miles Davis", lens.TypeAudio)
	album := "https://archive.org/details/so-what-album"
	reissue := "https://archive.org/details/track-01-reissue"
	otherTake := "https://archive.org/details/so-what-other-take"
	radioEdit := "https://archive.org/details/so-what-radio-edit"
	untested := "https://example.org/so-what"

	first, err := store.RecordObservations(ctx, q, []Observation{listing("so-what-album", "So What", "Miles Davis", album, true)})
	if err != nil || first.Fingerprints != 1 || first.Objects != 1 || first.Identities[album].Verdict != "" || first.Identities[album].Detail == "" {
		t.Fatalf("the first fingerprint of its kind is stored with nothing to compare: %+v %v", first, err)
	}

	second, err := store.RecordObservations(ctx, q, []Observation{
		listing("so-what-reissue", "Track 01", "", reissue, true),
		listing("so-what-other-take", "So What", "Miles Davis", otherTake, true),
	})
	if err != nil || second.Fingerprints != 2 {
		t.Fatalf("both fingerprints are stored: %+v %v", second, err)
	}
	if m := second.Identities[reissue]; m.Verdict != lens.VerdictSame || m.Effect != lens.EffectJoined || m.PeerURL != album || m.Distance > lens.SameSoundThreshold || m.Seconds < 20 {
		t.Fatalf("the reissue joined the album by its sound: %+v", m)
	}
	if m := second.Identities[otherTake]; m.Verdict != lens.VerdictDifferent || m.Effect != lens.EffectKeptApart || (m.PeerURL != album && m.PeerURL != reissue) || m.Distance < 0.4 {
		t.Fatalf("the other take was kept apart from the album: %+v", m)
	}
	// Objects: the query's own (counted once per recording call), the
	// other take's variant, and none left behind for the title "Track 01".
	if second.Objects != 2 {
		t.Fatalf("the query object and one variant were counted, got %d", second.Objects)
	}
	var trackObjects int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM atlas.objects WHERE title_key = 'track 01'").Scan(&trackObjects); err != nil || trackObjects != 0 {
		t.Fatalf("no object is stored for a title the sound overruled: %d %v", trackObjects, err)
	}

	third, err := store.RecordObservations(ctx, q, []Observation{listing("so-what-radio-edit", "So What", "Miles Davis", radioEdit, true)})
	if err != nil {
		t.Fatal(err)
	}
	if m := third.Identities[radioEdit]; m.Verdict != lens.VerdictSame || m.Effect != lens.EffectKeptApart || m.PeerURL != album && m.PeerURL != reissue {
		t.Fatalf("the radio edit sounds like the album but is kept apart by its length: %+v", m)
	}

	again, err := store.RecordObservations(ctx, q, []Observation{listing("so-what-other-take", "So What", "Miles Davis", otherTake, true)})
	if err != nil || again.Objects != 1 || again.Identities[otherTake].Effect != lens.EffectKeptApart {
		t.Fatalf("a re-sampled take stays the variant it is: %+v %v", again, err)
	}
	plain, err := store.RecordObservations(ctx, q, []Observation{listing("so-what-album", "So What", "Miles Davis", untested, false)})
	if err != nil || plain.Fingerprints != 0 || len(plain.Identities) != 0 {
		t.Fatalf("a listing without a fingerprint carries no verdict: %+v %v", plain, err)
	}

	known, err := store.Lookup(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]KnownLocation{}
	for _, k := range known {
		byURL[k.Location.URL] = k
	}
	if len(known) != 5 {
		t.Fatalf("five locations are known for the title: %d", len(known))
	}
	if byURL[reissue].Object.ID != byURL[album].Object.ID || byURL[untested].Object.ID != byURL[album].Object.ID || byURL[album].Object.ContentKey != "" {
		t.Fatalf("the album, its reissue and the untested listing share the query's object: %+v %+v %+v", byURL[album].Object, byURL[reissue].Object, byURL[untested].Object)
	}
	if o := byURL[otherTake].Object; o.ID == byURL[album].Object.ID || o.ContentKey == "" || o.TitleKey != "so what" || o.CreatorKey != "miles davis" {
		t.Fatalf("the other take is an object of its own under the same tags and a content key: %+v", o)
	}
	if o := byURL[radioEdit].Object; o.ID == byURL[album].Object.ID || o.ID == byURL[otherTake].Object.ID || o.ContentKey == "" {
		t.Fatalf("the radio edit is a third object: %+v", o)
	}
	if m := byURL[reissue].Identity; m == nil || m.Verdict != lens.VerdictSame || m.Effect != lens.EffectJoined || m.PeerURL != album || m.PeerTitle != "So What" || m.Detail == "" {
		t.Fatalf("a lookup carries the verdict reached when the fingerprint arrived: %+v", m)
	}
	if m := byURL[album].Identity; m == nil || m.Verdict != lens.VerdictSame || m.Effect != lens.EffectConfirmed || m.PeerURL != reissue || m.Method != lens.MethodFingerprint {
		t.Fatalf("the first fingerprint stored, which arrived with nothing to compare with, took the reissue that joined it as its peer, confirmed: %+v", m)
	}
	if byURL[untested].Identity != nil {
		t.Fatal("a location without a fingerprint carries no identity")
	}
	for _, url := range []string{album, reissue, otherTake, radioEdit} {
		if byURL[url].EffectiveLevel != lens.LevelRendered {
			t.Fatalf("%s keeps its rendered evidence: %+v", url, byURL[url])
		}
	}

	nearest, err := store.NearestFingerprints(ctx, fx["so-what-vinyl"].Fingerprint, 8)
	if err != nil || len(nearest) != 4 {
		t.Fatalf("every stored fingerprint of the type is compared: %d %v", len(nearest), err)
	}
	if !nearest[0].Comparison.Same() || nearest[0].Object.ID != byURL[album].Object.ID || nearest[len(nearest)-1].Comparison.Same() || nearest[len(nearest)-1].URL != otherTake {
		t.Fatalf("the vinyl rip is nearest the album's editions and farthest from the other take: %+v", nearest)
	}
	if _, err := store.NearestFingerprints(ctx, lens.Fingerprint{Type: "chromaprint", Frames: 1, Data: []byte{1, 2, 3, 4}}, 8); err == nil {
		t.Fatal("an invalid fingerprint cannot be compared")
	}
}
