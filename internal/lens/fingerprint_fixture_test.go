package lens

import (
	"encoding/json"
	"os"
	"testing"
)

// The fixture set: recordings with known fingerprints and the pairs whose
// identity the fingerprints must confirm or deny. The synthetic recordings
// are generated here (run with ITEMBA_UPDATE_FIXTURES=1 to regenerate
// them) from the same tunes the unit tests use, as editions of one another
// would be published: with a lead-in, at another level, with noise, cut
// short, or as a radio edit that keeps the album's beginning on a shorter
// file. Recordings with source archive were sampled live from the Internet
// Archive by the harness page and are kept as they came. Other packages
// read the same file for their tests.
const fingerprintFixturePath = "testdata/fingerprints.json"

type fixtureRecording struct {
	Name         string      `json:"name"`
	Source       string      `json:"source"`
	Title        string      `json:"title"`
	Creator      string      `json:"creator,omitempty"`
	URL          string      `json:"url,omitempty"`
	MediaSeconds float64     `json:"mediaSeconds,omitempty"`
	Fingerprint  Fingerprint `json:"fingerprint"`
}

type fixturePair struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Same bool   `json:"same"`
	Why  string `json:"why"`
}

type fingerprintFixtures struct {
	Recordings []fixtureRecording `json:"recordings"`
	Pairs      []fixturePair      `json:"pairs"`
}

func loadFingerprintFixtures(t testing.TB) fingerprintFixtures {
	t.Helper()
	data, err := os.ReadFile(fingerprintFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fx fingerprintFixtures
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// writeSyntheticFixtures regenerates the synthetic recordings and their
// pairs, keeping the Archive recordings and the pairs among them.
func writeSyntheticFixtures(t *testing.T) {
	t.Helper()
	var kept fingerprintFixtures
	if data, err := os.ReadFile(fingerprintFixturePath); err == nil {
		_ = json.Unmarshal(data, &kept)
	}
	soWhat := tune(7, 28)
	synthetic := []struct {
		name, title, creator string
		seconds              float64
		samples              []float64
	}{
		{"so-what-album", "So What", "Miles Davis", 562, soWhat},
		{"so-what-reissue", "Track 01", "", 560, edition(soWhat, 0.8, 0.6, 0.03, 4)},
		{"so-what-vinyl", "So What (vinyl rip)", "Miles Davis", 565, edition(soWhat, 2, 0.9, 0.08, 5)},
		{"so-what-radio-edit", "So What", "Miles Davis", 210, soWhat},
		{"so-what-other-take", "So What", "Miles Davis", 562, tune(12, 28)},
		{"freddie-freeloader", "Freddie Freeloader", "Miles Davis", 589, tune(9, 28)},
		{"blue-in-green-short", "Blue in Green", "Miles Davis", 337, tune(13, 8)},
		{"blue-in-green-full", "Blue in Green", "Miles Davis", 337, tune(13, 28)},
	}
	fx := fingerprintFixtures{Pairs: []fixturePair{
		{"so-what-album", "so-what-reissue", true, "the same sound under a bare track title with no performer"},
		{"so-what-album", "so-what-vinyl", true, "the same sound two seconds later, quieter and noisier"},
		{"so-what-reissue", "so-what-vinyl", true, "two editions of each other"},
		{"so-what-album", "so-what-radio-edit", false, "the same beginning on a file a third the length"},
		{"so-what-album", "so-what-other-take", false, "the same tags, a different sound"},
		{"so-what-album", "freddie-freeloader", false, "another track"},
		{"so-what-other-take", "freddie-freeloader", false, "two different tracks"},
		{"blue-in-green-short", "blue-in-green-full", true, "eight seconds are enough to compare"},
		{"so-what-reissue", "so-what-other-take", false, "a different sound under a bare title"},
		{"so-what-vinyl", "so-what-radio-edit", false, "an edition against an edit"},
	}}
	for _, s := range synthetic {
		fx.Recordings = append(fx.Recordings, fixtureRecording{Name: s.name, Source: "synthetic", Title: s.title, Creator: s.creator, MediaSeconds: s.seconds, Fingerprint: AcousticFingerprint(s.samples)})
	}
	archive := map[string]bool{}
	for _, r := range kept.Recordings {
		if r.Source == "archive" {
			fx.Recordings = append(fx.Recordings, r)
			archive[r.Name] = true
		}
	}
	for _, p := range kept.Pairs {
		if archive[p.A] && archive[p.B] {
			fx.Pairs = append(fx.Pairs, p)
		}
	}
	data, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fingerprintFixturePath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFixtureFingerprintsConfirmTheirKnownPairs is the Phase 3 exit check
// for the fingerprint library: on the fixture set, the fingerprints must
// confirm identity, or deny it, on every known pair, and the rate must be
// at least nine in ten even if one pair is ever allowed to slip.
func TestFixtureFingerprintsConfirmTheirKnownPairs(t *testing.T) {
	if os.Getenv("ITEMBA_UPDATE_FIXTURES") != "" {
		writeSyntheticFixtures(t)
	}
	fx := loadFingerprintFixtures(t)
	byName := map[string]fixtureRecording{}
	for _, r := range fx.Recordings {
		if !r.Fingerprint.Valid() {
			t.Fatalf("fixture %s carries an invalid fingerprint", r.Name)
		}
		byName[r.Name] = r
	}
	identity := func(r fixtureRecording) AudioIdentity {
		id := AudioListing(r.Title, r.Creator, "", 0)
		id.DurationSeconds = r.MediaSeconds
		fp := r.Fingerprint
		id.Fingerprint = &fp
		return id
	}
	confirmed := 0
	for _, p := range fx.Pairs {
		a, okA := byName[p.A]
		b, okB := byName[p.B]
		if !okA || !okB {
			t.Fatalf("pair %s/%s names an unknown recording", p.A, p.B)
		}
		got := MatchRecordings(identity(a), identity(b))
		if got.Basis == "tags" {
			t.Errorf("%s/%s: the fingerprints could not be compared, the tags decided", p.A, p.B)
			continue
		}
		if got.Same != p.Same {
			t.Errorf("%s/%s: same=%t by %s at %.3f, want %t (%s)", p.A, p.B, got.Same, got.Basis, got.Distance, p.Same, p.Why)
			continue
		}
		confirmed++
	}
	t.Logf("fingerprints confirmed identity on %d of %d known pairs (%d recordings)", confirmed, len(fx.Pairs), len(fx.Recordings))
	if len(fx.Pairs) < 10 || confirmed*10 < len(fx.Pairs)*9 {
		t.Fatalf("the exit criterion needs at least nine in ten of at least ten pairs, got %d of %d", confirmed, len(fx.Pairs))
	}
}
