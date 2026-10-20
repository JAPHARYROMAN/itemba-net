package lens

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
)

// patternImage draws a smooth pattern so that a smaller copy keeps the
// same cell means, as a thumbnail of a picture does.
func patternImage(width, height int, phaseX, phaseY float64) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := 128 + 100*math.Sin(float64(x)/float64(width)*6+phaseX)*math.Cos(float64(y)/float64(height)*5+phaseY)
			img.SetGray(x, y, color.Gray{Y: uint8(math.Round(v))})
		}
	}
	return img
}

func TestImageFingerprintSurvivesResizingAndTellsPicturesApart(t *testing.T) {
	original := ImageFingerprint(patternImage(640, 480, 0, 0))
	smaller := ImageFingerprint(patternImage(160, 120, 0, 0))
	other := ImageFingerprint(patternImage(640, 480, 2.1, 1.3))
	for name, fp := range map[string]Fingerprint{"original": original, "smaller": smaller, "other": other} {
		if !fp.Valid() || fp.Type != FingerprintImageDHash || fp.Frames != ImageHashWords || len(fp.Data) != 32 || fp.Seconds != 0 {
			t.Fatalf("%s: a hash is eight words of the image type: %+v", name, fp)
		}
	}
	same := CompareFingerprints(original, smaller)
	if !same.Known || same.Type != FingerprintImageDHash || same.Distance > SameSightThreshold || !same.Same() || same.Overlap != ImageHashWords {
		t.Fatalf("the same picture at a quarter of the size hashes alike: %+v", same)
	}
	different := CompareFingerprints(original, other)
	if !different.Known || different.Distance < 0.3 || different.Same() {
		t.Fatalf("another picture hashes differently: %+v", different)
	}
	if ContentKey(original) == ContentKey(other) || !strings.HasPrefix(ContentKey(original), "fp:") {
		t.Fatal("content keys name the picture")
	}
	if c := CompareFingerprints(original, Fingerprint{Type: FingerprintAcousticBands, Frames: 64, Data: make([]byte, 256)}); c.Known {
		t.Fatal("a hash is never compared with a sound")
	}
	if (&Fingerprint{Type: FingerprintImageDHash, Frames: 8, Data: make([]byte, 32), Seconds: 1}).Valid() || (&Fingerprint{Type: FingerprintImageDHash, Frames: 7, Data: make([]byte, 28)}).Valid() {
		t.Fatal("a hash has no span and exactly its words")
	}
	none, empty := ImageFingerprint(nil), ImageFingerprint(image.NewGray(image.Rect(0, 0, 0, 0)))
	if none.Valid() || empty.Valid() {
		t.Fatal("nothing hashes to a valid hash")
	}
	if FingerprintMethod(FingerprintImageDHash) != MethodPerceptualHash || FingerprintMethod(FingerprintAcousticBands) != MethodFingerprint {
		t.Fatal("a fingerprint type names its identity method")
	}
}

func TestImageIdentityMatchesBySightThenByTags(t *testing.T) {
	seen := ImageFingerprint(patternImage(320, 240, 0, 0))
	seenAgain := ImageFingerprint(patternImage(80, 60, 0, 0))
	other := ImageFingerprint(patternImage(320, 240, 1, 2))
	a := ImageListing("File:Land on the Moon 7 21 1969-repair.jpg", "Jack Weir (1928-2005)", 1969, 2356, 2795)
	if a.Title != "File:Land on the Moon 7 21 1969-repair.jpg" || a.TitleKey != "file land on the moon 7 21 1969 repair jpg" || a.CreatorKey != "jack weir 1928 2005" || a.Year != 1969 || a.Width != 2356 {
		t.Fatalf("a listing reads its tags: %+v", a)
	}
	b := ImageListing("Moon landing newspaper (copy)", "", 0, 0, 0)
	if b.Title != "Moon landing newspaper" || b.Year != 0 {
		t.Fatalf("a release label is separated as for any listing: %+v", b)
	}
	if MatchImages(a, b).Same || MatchImages(a, b).Basis != "tags" {
		t.Fatal("different titles with no hashes are different pictures")
	}
	a.Fingerprint, b.Fingerprint = &seen, &seenAgain
	if m := MatchImages(a, b); !m.Same || m.Basis != "fingerprint" || m.Distance > SameSightThreshold {
		t.Fatalf("the same sight under different tags is the same picture: %+v", m)
	}
	c := a
	c.Fingerprint = &other
	if m := MatchImages(a, c); m.Same || m.Basis != "fingerprint" {
		t.Fatalf("a different sight under the same tags is a different picture: %+v", m)
	}
	q := ParseAs("Moon landing by Jack Weir", TypeImage)
	if q.Type != TypeImage || q.Title != "Moon landing" || q.CreatorKey != "jack weir" {
		t.Fatalf("a picture query reads its maker like a performer: %+v", q)
	}
	if r := ImageRelevance(q, a); r < HostMatchRelevance || r >= 0.5 {
		t.Fatalf("a file name that shares a word keeps the host's own match: %v", r)
	}
	if r := ImageRelevance(q, ImageListing("Moon landing", "", 0, 0, 0)); r != 1 {
		t.Fatalf("the exact title scores fully: %v", r)
	}
	if r := ImageRelevance(q, ImageListing("Moon landing", "Someone Else", 0, 0, 0)); r != 0.5 {
		t.Fatalf("another maker halves the score: %v", r)
	}
	if r := ImageRelevance(ParseAs("Starry Night", TypeImage), ImageListing("Sunflowers", "", 0, 0, 0)); r != HostMatchRelevance {
		t.Fatalf("an unrelated file name still keeps the host's match: %v", r)
	}
}

func TestImageIdentityDetailSpeaksOfPictures(t *testing.T) {
	cases := []struct {
		match IdentityMatch
		want  string
	}{
		{IdentityMatch{Method: MethodPerceptualHash}, "Hashed; nothing else of its kind has been seen yet"},
		{IdentityMatch{Method: MethodPerceptualHash, Verdict: VerdictSame, Effect: EffectJoined, PeerTitle: "Moon", Distance: 0.03}, "Looks the same as Moon (hash distance 0.03): the same picture under different tags."},
		{IdentityMatch{Method: MethodPerceptualHash, Verdict: VerdictSame, Effect: EffectConfirmed, PeerTitle: "Moon", Distance: 0.01}, "the same picture, as the tags say"},
		{IdentityMatch{Method: MethodPerceptualHash, Verdict: VerdictDifferent, Effect: EffectKeptApart, PeerTitle: "Moon", Distance: 0.44}, "although the tags agree: a different picture, kept apart"},
		{IdentityMatch{Method: MethodPerceptualHash, Verdict: VerdictDifferent, PeerURL: "https://example.org/x", Distance: 0.5}, "Looks different from https://example.org/x (hash distance 0.50), the nearest hash compared"},
	}
	for _, tc := range cases {
		if got := IdentityDetail(tc.match); !strings.Contains(got, tc.want) {
			t.Errorf("IdentityDetail(%+v) = %q, want it to contain %q", tc.match, got, tc.want)
		}
		if got := IdentityDetail(tc.match); strings.Contains(got, "ound") {
			t.Errorf("a picture's detail never speaks of sound: %q", got)
		}
	}
}
