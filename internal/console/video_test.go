package console

import (
	"bytes"
	"math"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// TestVideoSampleFingerprintReachesTheLibraryWithTheFilmsLength proves the
// bridge for films: a confirmed video sample's frame fingerprint goes to
// the fingerprint library with the length the browser read, and the sample
// stays rendered evidence.
func TestVideoSampleFingerprintReachesTheLibraryWithTheFilmsLength(t *testing.T) {
	frames := make([]lens.VideoFrame, 0, 12)
	for i := 0; i < 12; i++ {
		cells := make([]int, lens.VideoFrameCells)
		for c := range cells {
			cells[c] = int(math.Round(128 + 100*math.Sin(float64(c%17)*0.4+float64(i)*0.3)*math.Cos(float64(c/17)*0.5)))
		}
		frames = append(frames, lens.VideoFrame{Seconds: float64(i) * lens.VideoFrameInterval, Cells: cells})
	}
	fingerprint := lens.VideoFingerprint(frames)
	source := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{ID: "ia-sintel", Title: "Sintel (2010)", URL: "https://archive.org/details/Sintel", DirectStreamURL: "https://archive.org/download/Sintel/sintel-2048-surround.mp4", Provider: "Internet Archive", SourceType: "Open license", AccessType: "free", ReleaseYear: 2010},
		Verified:        true, VerificationStatus: "media_reachable", DetectedManifests: []string{}, CheckedAt: "2026-10-20T06:00:00Z",
		PlaybackCheck:     &scout.PlaybackCheck{Status: "started", PlaybackConfirmed: true, Method: "isolated_video_sample", SecondsAdvanced: 2.4, DecodedFrames: 60, Width: 1024, Height: 436, DurationSeconds: 888.1, RequestedRegion: "ALL", Country: "TZ", CheckedAt: "2026-10-20T06:00:00Z", Fingerprint: &fingerprint, FingerprintStatus: "computed", Detail: "started"},
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Method: "http_media_probe"},
	}
	report := scout.SearchReport{Query: "Sintel", Kind: lens.TypeVideo, Region: "ALL", Sources: []scout.VerifiedSource{source}, VerifiedSources: []scout.VerifiedSource{source}, AvailabilityChecks: &scout.AvailabilityCheckCoverage{Country: "TZ"}}

	observations := observationsFromReport(report)
	if len(observations) != 1 || observations[0].Fingerprint == nil || observations[0].Fingerprint.Fingerprint.Type != lens.FingerprintVideoFrames || !bytes.Equal(observations[0].Fingerprint.Fingerprint.Data, fingerprint.Data) || observations[0].Fingerprint.MediaSeconds != 888.1 {
		t.Fatalf("the frame fingerprint goes to the library with the length the browser read: %+v", observations)
	}
	var rendered bool
	for _, e := range observations[0].Evidence {
		if e.Method == "isolated_video_sample" && e.Level == lens.LevelRendered {
			rendered = true
		}
	}
	if !rendered {
		t.Fatalf("a started sample is rendered evidence: %+v", observations[0].Evidence)
	}
}
