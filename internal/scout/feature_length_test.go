package scout

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

func TestFeatureLengthPreservesUnknownAndUsesObservedVideoRuntime(t *testing.T) {
	for _, tc := range []struct {
		listed   string
		observed float64
		short    bool
	}{
		{"", 0, false}, {"9:56", 0, true}, {"40:00", 0, false}, {"1h 35m", 0, false},
		{"1:35:00", 90, true}, {"10:00", 5700, false}, {"1:30:00", math.NaN(), false},
	} {
		if got := knownShortMovie(tc.listed, tc.observed); got != tc.short {
			t.Errorf("listed=%q observed=%v short=%v", tc.listed, tc.observed, got)
		}
	}
}

func TestFeatureCoverageDoesNotCountSampledShortsExcludedFromResults(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Hostname() == "api.tavily.com":
			return searchFixtureResponse(200, "application/json", `{"results":[{"title":"Fixture Film (2014)","url":"https://archive.org/details/fixture-2014"}]}`), nil
		case req.URL.Hostname() == "www.cloudflare.com":
			return searchFixtureResponse(200, "text/plain", "loc=US\n"), nil
		case req.URL.Path == "/metadata/fixture-2014":
			return searchFixtureResponse(200, "application/json", `{"metadata":{"identifier":"fixture-2014","title":"Fixture Film","mediatype":"movies","year":"2014"},"files":[{"name":"movie.mp4","length":"5700","size":"120000000"}]}`), nil
		case strings.HasPrefix(req.URL.Path, "/download/fixture-2014/"):
			return searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom"), nil
		default:
			return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
		}
	})}
	s.httpClient, s.availabilityHTTPClient = client, client
	var samples atomic.Int32
	s.playbackProbe = func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		samples.Add(1)
		result := startedFixture(region, country)
		result.DurationSeconds = 240
		return result
	}
	report := s.SearchWithOptions(context.Background(), "Fixture Film 2014", "ALL", SearchOptions{FullLengthOnly: true})
	if samples.Load() != 1 || len(report.Sources) != 0 || len(report.PrunedSources) != 1 || report.AvailabilityChecks == nil || report.AvailabilityChecks.PlaybackStartedCount != 0 || report.AvailabilityChecks.PlaybackCheckedCount != 0 {
		t.Fatalf("excluded short leaked into feature coverage: samples=%d report=%+v", samples.Load(), report)
	}
}

func TestDirectURLInspectionReportsItsEffectiveLengthPreference(t *testing.T) {
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		return searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom"), nil
	})}
	report := s.SearchWithOptions(context.Background(), "https://media.fixture.net/short.mp4", "ALL", SearchOptions{FullLengthOnly: true})
	if report.FullLengthOnly || len(report.Sources) != 1 {
		t.Fatalf("explicit URL was described as a full-length search: %+v", report)
	}
}

func TestFullLengthSearchFiltersKnownShortsKeepsUnknownAndSeparatesCache(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "itunes.apple.com" {
			return searchFixtureResponse(200, "application/json", `{"results":[{"kind":"feature-movie","trackId":1,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/us/movie/fixture-film/id1","releaseDate":"2014-01-01T00:00:00Z","trackTimeMillis":600000},{"kind":"feature-movie","trackId":2,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/us/movie/fixture-film/id2","releaseDate":"2014-01-01T00:00:00Z","trackTimeMillis":2100000},{"kind":"feature-movie","trackId":3,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/us/movie/fixture-film/id3","releaseDate":"2014-01-01T00:00:00Z"}]}`), nil
		}
		if req.URL.Hostname() == "www.cloudflare.com" {
			return searchFixtureResponse(200, "text/plain", "loc=US\n"), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	s.httpClient, s.availabilityHTTPClient = client, client
	features := s.SearchWithOptions(context.Background(), "Fixture Film 2014", "US", SearchOptions{FullLengthOnly: true})
	if !features.FullLengthOnly || len(features.Sources) != 1 {
		t.Fatalf("feature filter removed unknown length or kept a short: %+v", features)
	}
	for _, source := range features.Sources {
		if strings.HasSuffix(source.URL, "/id1") {
			t.Fatal("known short was retained")
		}
	}
	all := s.SearchWithRegion(context.Background(), "Fixture Film 2014", "US")
	if all.Cached || all.FullLengthOnly || len(all.Sources) != 3 {
		t.Fatalf("length preference cache mixed or shorts toggle lost: %+v", all)
	}
	features = s.SearchWithOptions(context.Background(), "Fixture Film 2014", "US", SearchOptions{FullLengthOnly: true})
	if !features.Cached || !features.FullLengthOnly || len(features.Sources) != 1 {
		t.Fatal("feature-mode cache was lost")
	}
}
