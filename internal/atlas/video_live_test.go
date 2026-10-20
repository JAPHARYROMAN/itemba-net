package atlas

import (
	"math"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// filmFingerprint is the frame fingerprint of a synthetic film: a smooth
// pattern whose phase moves with the frame, with pixel noise so two
// samples of one film differ a little.
func filmFingerprint(film int, noise float64) lens.Fingerprint {
	frames := make([]lens.VideoFrame, 0, 16)
	columns := lens.ImageHashColumns + 1
	for i := 0; i < 16; i++ {
		cells := make([]int, lens.VideoFrameCells)
		for y := 0; y < lens.ImageHashRows; y++ {
			for x := 0; x < columns; x++ {
				phase := float64(i) * 0.3
				v := 128 + 100*math.Sin(float64((x*31+y*17)*(film+2)%97)*0.37+phase)
				cells[y*columns+x] = int(math.Round(v + noise*math.Sin(float64(x*7+y*13+i*3))))
			}
		}
		frames = append(frames, lens.VideoFrame{Seconds: float64(i) * lens.VideoFrameInterval, Cells: cells})
	}
	return lens.VideoFingerprint(frames)
}

// TestFrameFingerprintsJoinTheSameFilm proves the library takes a video
// sample's frame fingerprint like a recording's: two hosts' copies of one
// film join by what they showed, and a different film under the same
// title is kept apart, with the verdicts in the film's words.
func TestFrameFingerprintsJoinTheSameFilm(t *testing.T) {
	store, ctx := liveStore(t)
	q := lens.ParseAs("Sintel", lens.TypeVideo)
	sintel := filmFingerprint(1, 0)
	sintelAgain := filmFingerprint(1, 6)
	other := filmFingerprint(2, 0)
	summary, err := store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Title: "Sintel (2010)", URL: "https://archive.org/details/Sintel", Provider: "Internet Archive", AccessType: "free", Duration: "14:48", Fingerprint: &FingerprintObservation{Fingerprint: sintel, MediaSeconds: 888}}},
		{Listing: Listing{Title: "Sintel - Blender open movie", URL: "https://video.blender.org/videos/watch/sintel", Provider: "Blender", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: sintelAgain, MediaSeconds: 888}}},
		{Listing: Listing{Title: "Sintel", URL: "https://example.org/sintel-fan-film", Provider: "Example", AccessType: "free", Fingerprint: &FingerprintObservation{Fingerprint: other, MediaSeconds: 600}}},
	})
	if err != nil || summary.Fingerprints != 3 {
		t.Fatalf("three films are fingerprinted: %+v %v", summary, err)
	}
	first := summary.Identities["https://archive.org/details/Sintel"]
	if first.Method != lens.MethodFrameFingerprint || first.Verdict != lens.VerdictSame || first.Effect != lens.EffectConfirmed {
		t.Fatalf("the first copy, revisited by the second, is confirmed by frames: %+v", first)
	}
	second := summary.Identities["https://video.blender.org/videos/watch/sintel"]
	if second.Verdict != lens.VerdictSame || second.Effect != lens.EffectJoined || second.PeerURL != "https://archive.org/details/Sintel" || second.Distance > lens.SameMotionThreshold || second.Seconds != 8 {
		t.Fatalf("the second host's copy joins the first by frames: %+v", second)
	}
	if !contains(second.Detail, "Plays the same as") || contains(second.Detail, "ound") {
		t.Fatalf("the verdict speaks of films: %q", second.Detail)
	}
	third := summary.Identities["https://example.org/sintel-fan-film"]
	if third.Verdict != lens.VerdictDifferent || third.Effect != lens.EffectKeptApart {
		t.Fatalf("a different film under the same title is kept apart: %+v", third)
	}
	known, err := store.Lookup(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]KnownLocation{}
	for _, k := range known {
		byURL[k.Location.URL] = k
	}
	if byURL["https://video.blender.org/videos/watch/sintel"].Object.ID != byURL["https://archive.org/details/Sintel"].Object.ID || byURL["https://example.org/sintel-fan-film"].Object.ID == byURL["https://archive.org/details/Sintel"].Object.ID {
		t.Fatalf("the same film shares an object and the other has its own: %+v", known)
	}
	if m := byURL["https://video.blender.org/videos/watch/sintel"].Identity; m == nil || m.Method != lens.MethodFrameFingerprint || m.Verdict != lens.VerdictSame {
		t.Fatalf("the lookup names the frame fingerprint: %+v", m)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
