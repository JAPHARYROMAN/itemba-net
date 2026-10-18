package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestPlaybackSampleHintsRespectEvidenceRelevanceAndDiscoveredHosts(t *testing.T) {
	plain, encoded := playbackFixture("plain"), playbackFixture("encoded")
	plain.RelevanceScore, encoded.RelevanceScore = 100, 100
	encoded.mediaBrowserFormat, encoded.mediaBitrate = true, 300000
	if !playbackSampleLess(encoded, plain, "ALL") || playbackSampleLess(plain, encoded, "ALL") {
		t.Fatal("equal evidence did not prefer a declared browser encoding")
	}
	encoded.RelevanceScore = 99
	if playbackSampleLess(encoded, plain, "ALL") || !playbackSampleLess(plain, encoded, "ALL") {
		t.Fatal("encoding hints displaced a more relevant movie")
	}
	encoded.RelevanceScore = 100
	encoded.AvailabilityCheck.Status = "unconfirmed"
	if playbackSampleLess(encoded, plain, "ALL") {
		t.Fatal("encoding hints displaced stronger media reachability evidence")
	}
	encoded.AvailabilityCheck.Status = "media_reachable"
	encoded.SourceType = "HLS Reference"
	if playbackSampleLess(encoded, plain, "ALL") {
		t.Fatal("reference fallback displaced a discovered movie host")
	}
}

func TestPlaybackSampleHintsPreferLowerFiniteKnownBitrateWithoutSiteOrCountryBias(t *testing.T) {
	lower, higher := playbackFixture("lower"), playbackFixture("higher")
	lower.RelevanceScore, higher.RelevanceScore = 100, 100
	lower.mediaBrowserFormat, higher.mediaBrowserFormat = true, true
	lower.mediaBitrate, higher.mediaBitrate = 250000, 1000000
	lower.URL, higher.URL = "https://movie-a.tv/movie/one", "https://movie-b.tv/movie/one"
	lower.Provider, higher.Provider = "Catalog A", "Catalog B"
	lower.AvailabilityCheck.Country, higher.AvailabilityCheck.Country = "US", "TZ"
	if !playbackSampleLess(lower, higher, "ALL") || playbackSampleLess(higher, lower, "ALL") {
		t.Fatal("lower known bitrate did not win equal global evidence")
	}
	lower.URL, higher.URL = higher.URL, lower.URL
	lower.Provider, higher.Provider = higher.Provider, lower.Provider
	lower.AvailabilityCheck.Country, higher.AvailabilityCheck.Country = higher.AvailabilityCheck.Country, lower.AvailabilityCheck.Country
	if !playbackSampleLess(lower, higher, "ALL") {
		t.Fatal("metadata scheduling depended on a site or the check country")
	}
	for _, unknown := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		higher.mediaBitrate = unknown
		if !playbackSampleLess(lower, higher, "ALL") || playbackSampleLess(higher, lower, "ALL") {
			t.Fatalf("invalid or unknown bitrate displaced a known positive bitrate: %v", unknown)
		}
	}
	lower.mediaBitrate, higher.mediaBitrate = math.NaN(), math.Inf(1)
	if playbackSampleLess(lower, higher, "ALL") || playbackSampleLess(higher, lower, "ALL") {
		t.Fatal("invalid hints broke stable ordering")
	}
}

func TestPlaybackSampleHintsKeepTwoSlotSiteDiversity(t *testing.T) {
	high, low, other := playbackFixture("high"), playbackFixture("low"), playbackFixture("other")
	for _, source := range []*VerifiedSource{&high, &low, &other} {
		source.RelevanceScore = 100
	}
	high.mediaBrowserFormat, low.mediaBrowserFormat = true, true
	high.mediaBitrate, low.mediaBitrate = 1000000, 250000
	other.ID, other.Provider, other.SourceType = "wm-fixture", "Wikimedia Commons", "Open media"
	other.URL, other.DirectStreamURL = "https://commons.wikimedia.org/wiki/File:Movie.webm", "https://upload.wikimedia.org/movie.webm"
	var mu sync.Mutex
	called := map[string]bool{}
	s := &Scout{playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		mu.Lock()
		called[source.ID] = true
		mu.Unlock()
		return &PlaybackCheck{Status: "not_started", RequestedRegion: region, Country: country, Method: "isolated_video_sample"}
	}}
	results := []VerifiedSource{high, other, low}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if len(called) != 2 || !called[low.ID] || !called[other.ID] || called[high.ID] {
		t.Fatalf("hints lost the two-video or site diversity budget: %v", called)
	}
	if results[2].PlaybackCheck.PlaybackConfirmed || playbackStarted(results[2]) {
		t.Fatal("metadata hints were treated as actual playback evidence")
	}
}

func TestArchivePlaybackHintsUseExplicitMetadataAndRemainInternal(t *testing.T) {
	for _, test := range []struct {
		name, format, file, size string
		browser                  bool
		bitrate                  float64
	}{
		{"h264", "h.264", "feature.mp4", "100000000", true, 200000},
		{"avc", "AVC", "feature.mp4", "100000000", true, 200000},
		{"webm", "WebM", "feature.webm", "100000000", true, 200000},
		{"streaming_512_derivative", "512Kb MPEG4", "feature.mp4", "100000000", true, 200000},
		{"generic_mpeg4", "MPEG4", "feature.mp4", "100000000", false, 200000},
		{"other_512_label_is_unknown", "512Kb MPEG4 custom", "feature.mp4", "100000000", false, 200000},
		{"extension_is_not_codec_evidence", "", "feature.webm", "100000000", false, 200000},
		{"missing_size", "h.264", "feature.mp4", "", true, 0},
		{"nan_size", "h.264", "feature.mp4", "NaN", true, 0},
		{"infinite_size", "h.264", "feature.mp4", "Inf", true, 0},
		{"negative_size", "h.264", "feature.mp4", "-1", true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
				body := fmt.Sprintf(`{"metadata":{"identifier":"fixture","title":"Fixture Film","mediatype":"movies","year":"1940","runtime":"4000"},"files":[{"name":%q,"format":%q,"length":"4000","size":%q}]}`, test.file, test.format, test.size)
				response := searchFixtureResponse(200, "application/json", body)
				response.Request = req
				return response, nil
			})}
			resolved, ok := resolveFreeArchiveSource(context.Background(), client, CandidateSource{Title: "Fixture Film", ReleaseYear: 1940, URL: "https://archive.org/details/fixture", ListingOnly: true}, "fixture", false)
			if !ok || resolved.mediaBrowserFormat != test.browser || resolved.mediaBitrate != test.bitrate {
				t.Fatalf("incorrect declared encoding hints: ok=%v browser=%v bitrate=%v", ok, resolved.mediaBrowserFormat, resolved.mediaBitrate)
			}
			encoded, err := json.Marshal(resolved)
			if err != nil || strings.Contains(string(encoded), "mediaBrowserFormat") || strings.Contains(string(encoded), "mediaBitrate") {
				t.Fatal("internal scheduling hints leaked into public movie claims")
			}
		})
	}
}

func TestArchiveSelectsLowerBitrateRecognized512StreamingDerivative(t *testing.T) {
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"metadata":{"identifier":"fixture","title":"Fixture Film","mediatype":"movies","year":"1940","runtime":"4000"},"files":[{"name":"generic_small.mp4","format":"MPEG4","length":"4000","size":"50000000"},{"name":"declared_h264.mp4","format":"h.264","length":"4000","size":"200000000"},{"name":"streaming_512.mp4","format":"512Kb MPEG4","length":"4000","size":"100000000"}]}`
		response := searchFixtureResponse(200, "application/json", body)
		response.Request = req
		return response, nil
	})}
	resolved, ok := resolveFreeArchiveSource(context.Background(), client, CandidateSource{Title: "Fixture Film", ReleaseYear: 1940, URL: "https://archive.org/details/fixture", ListingOnly: true}, "fixture", false)
	if !ok || !strings.HasSuffix(resolved.DirectStreamURL, "/streaming_512.mp4") || !resolved.mediaBrowserFormat || resolved.mediaBitrate != 200000 {
		t.Fatalf("declared low bitrate streaming derivative not selected: ok=%v source=%+v", ok, resolved)
	}
}
