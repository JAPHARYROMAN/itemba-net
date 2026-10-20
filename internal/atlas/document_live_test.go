package atlas

import (
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// proseText composes a deterministic text of n words from a vocabulary,
// so the test can hold the same text twice and a different one.
func proseText(vocabulary string, seed uint32, n int) string {
	words := strings.Fields(vocabulary)
	out := make([]string, 0, n)
	x := seed
	for i := 0; i < n; i++ {
		x = x*1664525 + 1013904223
		out = append(out, words[(x>>16)%uint32(len(words))])
	}
	return strings.Join(out, " ")
}

// misread misreads every twenty-fifth word, as OCR does on a scan.
func misread(text string) string {
	words := strings.Fields(text)
	for i := 24; i < len(words); i += 25 {
		words[i] = "xq" + words[i][1:]
	}
	return strings.Join(words, " ")
}

const (
	millProse  = "the river ran past the old mill where the miller kept a ledger of every sack that crossed the bridge and the ferryman counted the lanterns on the far bank while the geese slept under the willow and nobody asked why the bell rang twice at dusk"
	coastProse = "salt wind over shingle beach gulls wheel above tide pools keeper trims lamp before fog rolls in boats return late nets heavy with herring harbour master logs each arrival chalk cliff crumbles slowly into grey sea"
)

// TestLaterFingerprintRevisitsEarlierVerdict proves the library does not
// let the order of recording show: a text recorded with nothing to compare
// with takes a later arrival that reads the same as its peer, confirmed
// once the two sit in one object; a lookup repeats the revised verdict
// with the text's own method; and a different text under the same tags,
// kept apart later, revises nothing.
func TestLaterFingerprintRevisitsEarlierVerdict(t *testing.T) {
	store, ctx := liveStore(t)
	q := lens.ParseAs("Alice in Wonderland by Lewis Carroll", lens.TypeDocument)
	body := proseText(millProse, 7, 1200)
	first := lens.TextFingerprint(body)
	again := lens.TextFingerprint(misread(body))
	other := lens.TextFingerprint(proseText(coastProse, 11, 1200))
	const (
		scan        = "https://archive.org/details/alice-scan"
		edition     = "https://archive.org/details/alice-edition"
		anotherText = "https://archive.org/details/alice-other"
	)

	summary, err := store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Kind: lens.TypeDocument, Title: "Alice in Wonderland", Creator: "Carroll, Lewis, 1832-1898", URL: scan, Provider: "Internet Archive", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: first}}},
	})
	if err != nil || summary.Fingerprints != 1 || summary.Revisited != 0 {
		t.Fatalf("the first text is fingerprinted with nothing to revisit: %+v %v", summary, err)
	}
	if alone := summary.Identities[scan]; alone.Verdict != "" || alone.PeerURL != "" || alone.Method != lens.MethodTextFingerprint {
		t.Fatalf("the first text has nothing to compare with: %+v", alone)
	}

	summary, err = store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Kind: lens.TypeDocument, Title: "Adventures Under Ground", Creator: "Carroll, Lewis", URL: edition, Provider: "Internet Archive", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: again}}},
		{Listing: Listing{Kind: lens.TypeDocument, Title: "Alice in Wonderland", Creator: "Lewis Carroll", URL: anotherText, Provider: "Internet Archive", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: other}}},
	})
	if err != nil || summary.Fingerprints != 2 || summary.Revisited != 1 {
		t.Fatalf("two more texts, one revisit: %+v %v", summary, err)
	}
	if joined := summary.Identities[edition]; joined.Verdict != lens.VerdictSame || joined.Effect != lens.EffectJoined || joined.PeerURL != scan {
		t.Fatalf("the same text under another title joins the first: %+v", joined)
	}
	revised, ok := summary.Identities[scan]
	if !ok || revised.Verdict != lens.VerdictSame || revised.Effect != lens.EffectConfirmed || revised.PeerURL != edition || revised.PeerTitle != "Adventures Under Ground" || revised.Distance > lens.SameTextThreshold || !strings.Contains(revised.Detail, "as the tags say") {
		t.Fatalf("the first text takes the later arrival as its peer, confirmed: %+v", revised)
	}
	if apart := summary.Identities[anotherText]; apart.Verdict != lens.VerdictDifferent || apart.Effect != lens.EffectKeptApart {
		t.Fatalf("a different text under the same tags is kept apart: %+v", apart)
	}

	known, err := store.Lookup(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]KnownLocation{}
	for _, k := range known {
		byURL[k.Location.URL] = k
	}
	if len(byURL) != 3 {
		t.Fatalf("the lookup returns the three locations: %+v", known)
	}
	stored := byURL[scan]
	if stored.Identity == nil || stored.Identity.Verdict != lens.VerdictSame || stored.Identity.Effect != lens.EffectConfirmed || stored.Identity.PeerURL != edition || stored.Identity.Method != lens.MethodTextFingerprint || !strings.Contains(stored.Identity.Detail, "Reads the same as") {
		t.Fatalf("the lookup repeats the revised verdict with the text's method: %+v", stored.Identity)
	}
	if byURL[edition].Object.ID != stored.Object.ID || byURL[anotherText].Object.ID == stored.Object.ID || byURL[anotherText].Object.ContentKey == "" {
		t.Fatalf("the same text shares the object and the different text has its own: %+v %+v %+v", stored.Object, byURL[edition].Object, byURL[anotherText].Object)
	}

	// Recorded again, the different text revises nothing: the first text
	// already has a same peer, and the later arrival is different.
	summary, err = store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Kind: lens.TypeDocument, Title: "Alice in Wonderland", Creator: "Lewis Carroll", URL: anotherText, Provider: "Internet Archive", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: other}}},
	})
	if err != nil || summary.Revisited != 0 {
		t.Fatalf("a different text revises no same verdict: %+v %v", summary, err)
	}
}
