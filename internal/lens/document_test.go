package lens

import (
	"strings"
	"testing"
)

// sampleText composes a deterministic text of n words from a vocabulary,
// so a test can read the same text twice, once with OCR noise, and a
// different text, without quoting anyone.
func sampleText(vocabulary string, seed uint32, n int) string {
	words := strings.Fields(vocabulary)
	out := make([]string, 0, n)
	x := seed
	for i := 0; i < n; i++ {
		x = x*1664525 + 1013904223
		out = append(out, words[(x>>16)%uint32(len(words))])
	}
	return strings.Join(out, " ")
}

const (
	millVocabulary  = "the river ran past the old mill where the miller kept a ledger of every sack that crossed the bridge and the ferryman counted the lanterns on the far bank while the geese slept under the willow and nobody asked why the bell rang twice at dusk"
	coastVocabulary = "salt wind over shingle beach gulls wheel above tide pools keeper trims lamp before fog rolls in boats return late nets heavy with herring harbour master logs each arrival chalk cliff crumbles slowly into grey sea"
)

// noisyText misreads every twenty-fifth word, as OCR does on a scan.
func noisyText(text string) string {
	words := strings.Fields(text)
	for i := 24; i < len(words); i += 25 {
		words[i] = "xq" + words[i][1:]
	}
	return strings.Join(words, " ")
}

func TestTextFingerprintSurvivesNoiseAndTellsTextsApart(t *testing.T) {
	body := sampleText(millVocabulary, 7, 1200)
	original := TextFingerprint(body)
	again := TextFingerprint(body)
	noisy := TextFingerprint(noisyText(body))
	edition := TextFingerprint(sampleText(coastVocabulary, 3, 60) + " " + body)
	other := TextFingerprint(sampleText(coastVocabulary, 11, 1200))
	for name, fp := range map[string]Fingerprint{"original": original, "noisy": noisy, "edition": edition, "other": other} {
		if !fp.Valid() || fp.Type != FingerprintTextSimhash || fp.Frames != TextHashWords || len(fp.Data) != 32 || fp.Seconds != 0 {
			t.Fatalf("%s: a text fingerprint is eight words of the text type: %+v", name, fp)
		}
	}
	if c := CompareFingerprints(original, again); !c.Known || c.Distance != 0 || !c.Same() || c.Type != FingerprintTextSimhash || c.Overlap != TextHashWords {
		t.Fatalf("the same text fingerprints identically: %+v", c)
	}
	if c := CompareFingerprints(original, noisy); !c.Known || c.Distance > SameTextThreshold || !c.Same() {
		t.Fatalf("OCR noise leaves the same text: %+v", c)
	}
	if c := CompareFingerprints(original, edition); !c.Known || c.Distance > SameTextThreshold || !c.Same() {
		t.Fatalf("another front matter before the same body is the same text: %+v", c)
	}
	if c := CompareFingerprints(original, other); !c.Known || c.Distance < 0.3 || c.Same() {
		t.Fatalf("a different text fingerprints differently: %+v", c)
	}
	if ContentKey(original) == ContentKey(other) || !strings.HasPrefix(ContentKey(original), "fp:") {
		t.Fatal("content keys name the text")
	}
	if c := CompareFingerprints(original, Fingerprint{Type: FingerprintImageDHash, Frames: ImageHashWords, Data: make([]byte, 32)}); c.Known {
		t.Fatal("a text is never compared with a picture")
	}
	if short := TextFingerprint(sampleText(millVocabulary, 1, MinFingerprintWords-1)); short.Valid() || short.Type != "" {
		t.Fatalf("too few words give no fingerprint: %+v", short)
	}
	if (&Fingerprint{Type: FingerprintTextSimhash, Frames: 8, Data: make([]byte, 32), Seconds: 1}).Valid() || (&Fingerprint{Type: FingerprintTextSimhash, Frames: 7, Data: make([]byte, 28)}).Valid() {
		t.Fatal("a text fingerprint has no span and exactly its words")
	}
	if FingerprintMethod(FingerprintTextSimhash) != MethodTextFingerprint {
		t.Fatal("the text type names its identity method")
	}
	if words := TextWords("Alice's  Adventures, in\nWONDERLAND 1865"); strings.Join(words, "|") != "alice|s|adventures|in|wonderland|1865" {
		t.Fatalf("words are lower-case runs of letters and digits: %v", words)
	}
}

func TestNormalizeAuthorReadsCatalogueCredits(t *testing.T) {
	cases := map[string]string{
		"Carroll, Lewis, 1832-1898":      "Lewis Carroll",
		"Carroll, Lewis, 1832-":          "Lewis Carroll",
		"Twain, Mark, 1835-1910.":        "Mark Twain",
		"Shakespeare, William":           "William Shakespeare",
		"Jane Austen (1775-1817)":        "Jane Austen",
		"Lewis Carroll":                  "Lewis Carroll",
		"NASA":                           "NASA",
		"Smith, John, Jr.":               "Smith, John, Jr.",
		"United States. Congress. House": "United States. Congress. House",
		"":                               "",
	}
	for credit, want := range cases {
		if got := NormalizeAuthor(credit); got != want {
			t.Errorf("NormalizeAuthor(%q) = %q, want %q", credit, got, want)
		}
	}
}

func TestDocumentIdentityMatchesByTextThenByTags(t *testing.T) {
	body := sampleText(millVocabulary, 5, 800)
	read := TextFingerprint(body)
	readAgain := TextFingerprint(noisyText(body))
	other := TextFingerprint(sampleText(coastVocabulary, 9, 800))
	a := DocumentListing("Alice's Adventures in Wonderland", "Carroll, Lewis, 1832-1898", 1865, 232)
	if a.Title != "Alice's Adventures in Wonderland" || a.TitleKey != "alice s adventures in wonderland" || a.Creator != "Lewis Carroll" || a.CreatorKey != "lewis carroll" || a.Year != 1865 || a.Pages != 232 {
		t.Fatalf("a listing reads its tags: %+v", a)
	}
	b := DocumentListing("Through the Looking-Glass", "", 0, 0)
	if m := MatchDocuments(a, b); m.Same || m.Basis != "tags" {
		t.Fatal("different titles with no fingerprints are different documents")
	}
	sameTags := DocumentListing("Alice's Adventures in Wonderland", "Lewis Carroll", 1898, 0)
	if m := MatchDocuments(a, sameTags); !m.Same || m.Basis != "tags" {
		t.Fatalf("the same title by the same author is the same document by its tags: %+v", m)
	}
	if m := MatchDocuments(a, DocumentListing("Alice's Adventures in Wonderland", "Someone Else", 0, 0)); m.Same {
		t.Fatal("the same title by another author is not the same document")
	}
	a.Fingerprint, b.Fingerprint = &read, &readAgain
	if m := MatchDocuments(a, b); !m.Same || m.Basis != "fingerprint" || m.Distance > SameTextThreshold {
		t.Fatalf("the same text under different tags is the same document: %+v", m)
	}
	c := sameTags
	c.Fingerprint = &other
	if m := MatchDocuments(a, c); m.Same || m.Basis != "fingerprint" {
		t.Fatalf("a different text under the same tags is a different document: %+v", m)
	}
	q := ParseAs("Alice in Wonderland by Lewis Carroll", TypeDocument)
	if q.Type != TypeDocument || q.Title != "Alice in Wonderland" || q.CreatorKey != "lewis carroll" {
		t.Fatalf("a document query reads its author like a performer: %+v", q)
	}
	if r := DocumentRelevance(q, DocumentListing("Alice in Wonderland", "Carroll, Lewis, 1832-1898", 0, 0)); r != 1 {
		t.Fatalf("the exact title by the author scores fully: %v", r)
	}
	if r := DocumentRelevance(q, DocumentListing("Alice in Wonderland", "Someone Else", 0, 0)); r != 0.5 {
		t.Fatalf("another author halves the score: %v", r)
	}
	if r := DocumentRelevance(ParseAs("Alice in Wonderland 1865", TypeDocument), DocumentListing("Alice in Wonderland", "", 1920, 0)); r != 0.8 {
		t.Fatalf("another year lowers the score: %v", r)
	}
	if r := DocumentRelevance(q, DocumentListing("Cooking with Geese", "", 0, 0)); r != HostMatchRelevance {
		t.Fatalf("an unrelated title keeps the host's own match: %v", r)
	}
}

func TestTextIdentityDetailSpeaksOfText(t *testing.T) {
	cases := []struct {
		match IdentityMatch
		want  string
	}{
		{IdentityMatch{Method: MethodTextFingerprint}, "Fingerprinted; nothing else of its kind has been read yet"},
		{IdentityMatch{Method: MethodTextFingerprint, Verdict: VerdictSame, Effect: EffectJoined, PeerTitle: "Alice", Distance: 0.03}, "Reads the same as Alice (text distance 0.03): the same text under different tags."},
		{IdentityMatch{Method: MethodTextFingerprint, Verdict: VerdictSame, Effect: EffectConfirmed, PeerTitle: "Alice", Distance: 0.01}, "the same text, as the tags say"},
		{IdentityMatch{Method: MethodTextFingerprint, Verdict: VerdictDifferent, Effect: EffectKeptApart, PeerTitle: "Alice", Distance: 0.44}, "although the tags agree: a different text, kept apart"},
		{IdentityMatch{Method: MethodTextFingerprint, Verdict: VerdictDifferent, PeerURL: "https://example.org/x", Distance: 0.5}, "Reads differently from https://example.org/x (text distance 0.50), the nearest text compared"},
	}
	for _, tc := range cases {
		got := IdentityDetail(tc.match)
		if !strings.Contains(got, tc.want) {
			t.Errorf("IdentityDetail(%+v) = %q, want it to contain %q", tc.match, got, tc.want)
		}
		if strings.Contains(got, "ound") || strings.Contains(got, "picture") {
			t.Errorf("a document's detail never speaks of sound or pictures: %q", got)
		}
	}
}
