package lens

import (
	"math"
	"strings"
	"testing"
)

// filmFrame is the grey cells of one frame of a synthetic film: a smooth
// pattern whose phase moves with the frame number, so consecutive frames
// differ a little and distant ones a lot. film tells two films apart.
func filmFrame(film, frame int, noise float64) []int {
	cells := make([]int, VideoFrameCells)
	columns := ImageHashColumns + 1
	for y := 0; y < ImageHashRows; y++ {
		for x := 0; x < columns; x++ {
			phase := float64(frame) * 0.3
			v := 128 + 100*math.Sin(float64((x*31+y*17)*(film+2)%97)*0.37+phase)
			v += noise * math.Sin(float64(x*7+y*13+frame*3))
			cells[y*columns+x] = int(math.Round(v))
		}
	}
	return cells
}

// filmFrames samples a film from frame first for n frames, VideoFrameInterval
// apart, timed from the sample's own start plus an offset within the slot.
func filmFrames(film, first, n int, noise, offset float64) []VideoFrame {
	frames := make([]VideoFrame, 0, n)
	for i := 0; i < n; i++ {
		frames = append(frames, VideoFrame{Seconds: float64(i)*VideoFrameInterval + offset, Cells: filmFrame(film, first+i, noise)})
	}
	return frames
}

func TestVideoFingerprintAlignsTheSameFilmAndTellsFilmsApart(t *testing.T) {
	original := VideoFingerprint(filmFrames(1, 0, 20, 0, 0.1))
	again := VideoFingerprint(filmFrames(1, 0, 20, 6, 0.3))
	later := VideoFingerprint(filmFrames(1, 3, 16, 0, 0.1))
	other := VideoFingerprint(filmFrames(2, 0, 20, 0, 0.1))
	for name, fp := range map[string]Fingerprint{"original": original, "again": again, "later": later, "other": other} {
		if !fp.Valid() || fp.Type != FingerprintVideoFrames || fp.Frames%VideoFrameWords != 0 || len(fp.Data) != 4*fp.Frames || fp.Seconds <= 0 {
			t.Fatalf("%s: a frame fingerprint is whole frames of the video type: %+v", name, fp)
		}
	}
	if VideoFrameCount(original) != 20 || original.Seconds != 10 || VideoFrameCount(later) != 16 {
		t.Fatalf("twenty frames half a second apart cover ten seconds: %d %v", VideoFrameCount(original), original.Seconds)
	}
	if c := CompareFingerprints(original, original); !c.Known || c.Distance != 0 || c.Overlap != 20 || c.Shift != 0 || c.Type != FingerprintVideoFrames || c.OverlapSeconds() != 10 {
		t.Fatalf("a film is itself: %+v", c)
	}
	if c := CompareFingerprints(original, again); !c.Known || c.Distance > SameMotionThreshold || !c.Same() {
		t.Fatalf("the same film with a little pixel noise is the same film: %+v", c)
	}
	if c := CompareFingerprints(original, later); !c.Known || c.Distance != 0 || c.Shift != 3 || c.Overlap != 16 || !c.Same() {
		t.Fatalf("the same film started three frames in is found at that shift: %+v", c)
	}
	if c := CompareFingerprints(later, original); !c.Known || c.Shift != -3 || !c.Same() {
		t.Fatalf("the alignment holds the other way round: %+v", c)
	}
	if c := CompareFingerprints(original, other); !c.Known || c.Distance < 0.3 || c.Same() {
		t.Fatalf("another film plays differently: %+v", c)
	}
	if c := CompareFingerprints(original, VideoFingerprint(filmFrames(1, 0, MinVideoFrames-1, 0, 0))); c.Known {
		t.Fatal("too few frames cannot be compared")
	}
	if c := CompareFingerprints(original, Fingerprint{Type: FingerprintImageDHash, Frames: ImageHashWords, Data: make([]byte, 32)}); c.Known {
		t.Fatal("a frame sequence is never compared with a single picture hash")
	}
	if ContentKey(original) == ContentKey(other) {
		t.Fatal("content keys name the film")
	}
	if FingerprintMethod(FingerprintVideoFrames) != MethodFrameFingerprint {
		t.Fatal("the video type names its identity method")
	}
	if (&Fingerprint{Type: FingerprintVideoFrames, Frames: 12, Data: make([]byte, 48), Seconds: 1}).Valid() || (&Fingerprint{Type: FingerprintVideoFrames, Frames: 16, Data: make([]byte, 64)}).Valid() {
		t.Fatal("a frame fingerprint holds whole frames and a span")
	}
}

func TestVideoFingerprintPlacesFramesBySlotAndFillsGaps(t *testing.T) {
	frames := filmFrames(1, 0, 8, 0, 0.1)
	// Two captures in one slot, a slot the capture missed, and a frame of
	// the wrong shape.
	frames = append(frames[:3], append([]VideoFrame{{Seconds: 1.3, Cells: filmFrame(1, 99, 0)}}, frames[3:]...)...)
	frames = append(frames[:6], frames[7:]...)
	frames = append(frames, VideoFrame{Seconds: 9, Cells: []int{1, 2, 3}})
	fp := VideoFingerprint(frames)
	if VideoFrameCount(fp) != 8 || fp.Seconds != 4 {
		t.Fatalf("eight slots give eight frames, the missed one filled and the extra ones dropped: %d frames over %v s", VideoFrameCount(fp), fp.Seconds)
	}
	reference := VideoFingerprint(filmFrames(1, 0, 8, 0, 0.1))
	if c := CompareFingerprints(fp, reference); !c.Known || c.Distance > 0.1 || c.Shift != 0 {
		t.Fatalf("the filled sequence still aligns with the clean one: %+v", c)
	}
	if fp := VideoFingerprint(nil); fp.Valid() || fp.Type != "" {
		t.Fatal("no frames give no fingerprint")
	}
	long := VideoFingerprint(filmFrames(1, 0, 100, 0, 0))
	if VideoFrameCount(long) != int(VideoFingerprintSeconds/VideoFrameInterval) || long.Seconds != VideoFingerprintSeconds {
		t.Fatalf("the fingerprint stops at %v seconds: %d frames over %v s", VideoFingerprintSeconds, VideoFrameCount(long), long.Seconds)
	}
}

func TestVideoIdentityDetailSpeaksOfFilms(t *testing.T) {
	cases := []struct {
		match IdentityMatch
		want  string
	}{
		{IdentityMatch{Method: MethodFrameFingerprint}, "Fingerprinted; nothing else of its kind has been sampled yet"},
		{IdentityMatch{Method: MethodFrameFingerprint, Verdict: VerdictSame, Effect: EffectJoined, PeerTitle: "Sintel", Distance: 0.04, Seconds: 8}, "Plays the same as Sintel (frame distance 0.04 over 8 s of the beginning): the same film under different tags."},
		{IdentityMatch{Method: MethodFrameFingerprint, Verdict: VerdictSame, Effect: EffectConfirmed, PeerTitle: "Sintel", Distance: 0.01, Seconds: 8}, "the same film, as the tags say"},
		{IdentityMatch{Method: MethodFrameFingerprint, Verdict: VerdictDifferent, Effect: EffectKeptApart, PeerTitle: "Sintel", Distance: 0.44, Seconds: 8}, "although the tags agree: a different film, kept apart"},
		{IdentityMatch{Method: MethodFrameFingerprint, Verdict: VerdictDifferent, PeerURL: "https://example.org/x", Distance: 0.5, Seconds: 8}, "Plays differently from https://example.org/x (frame distance 0.50 over 8 s of the beginning), the nearest sample compared"},
	}
	for _, tc := range cases {
		got := IdentityDetail(tc.match)
		if !strings.Contains(got, tc.want) {
			t.Errorf("IdentityDetail(%+v) = %q, want it to contain %q", tc.match, got, tc.want)
		}
		if strings.Contains(got, "ound") || strings.Contains(got, "picture") || strings.Contains(got, "text") {
			t.Errorf("a film's detail never speaks of sound, pictures or text: %q", got)
		}
	}
}
