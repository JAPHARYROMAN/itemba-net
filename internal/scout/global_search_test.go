package scout

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

func TestGlobalMovieSearchResolvesNewFreeSiteAndKeepsPaidAccessSeparate(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.UseDrivers(testDrivers(t))
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "api.tavily.com" {
			return searchFixtureResponse(200, "application/json", `{"results":[{"title":"Fixture Film (2014) - Public Domain Movies","url":"https://publicdomainmovie.net/movie/fixture-film"},{"title":"Fixture Film (2014) | Netflix","url":"https://www.netflix.com/title/fixture"}]}`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		var response *http.Response
		switch {
		case req.URL.Hostname() == "www.cloudflare.com":
			response = searchFixtureResponse(200, "text/plain", "loc=TZ\n")
		case req.URL.Hostname() == "video.blender.org":
			response = searchFixtureResponse(200, "application/json", `{"data":[]}`)
		case req.URL.Hostname() == "publicdomainmovie.net":
			response = searchFixtureResponse(200, "text/html", `<main><h1>Fixture Film (2014)</h1><div class="field-name-download"><a href="/movie.php?id=fixture-2014&amp;type=.mp4">Download movie</a></div></main>`)
		case req.URL.Path == "/metadata/fixture-2014":
			response = searchFixtureResponse(200, "application/json", `{"metadata":{"identifier":"fixture-2014","title":"Fixture Film","mediatype":"movies","year":"2014","runtime":"600"},"files":[{"name":"feature.mp4","length":"600","size":"12000000"},{"name":"trailer.mp4","length":"90","size":"100000"}]}`)
		case strings.HasPrefix(req.URL.Path, "/download/fixture-2014/"):
			response = searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom")
		default:
			response = searchFixtureResponse(200, "text/html", `<title>Fixture Film (2014)</title><main><h1>Fixture Film (2014)</h1><p>Explore this feature film and the available account membership offers on its individual movie detail page.</p></main>`)
		}
		response.Request = req
		return response, nil
	})}
	var sampled atomic.Int32
	s.playbackProbe = func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		sampled.Add(1)
		if source.Provider != "Public Domain Movies" || source.AccessType != "free" || source.AccessBasis != "archive_metadata" || !strings.HasSuffix(source.DirectStreamURL, "/feature.mp4") {
			t.Errorf("unsupported/paid media reached the sample: %+v", source)
		}
		return startedFixture(region, country)
	}
	report := s.SearchWithRegion(context.Background(), "Fixture Film 2014", "")
	if report.Region != "ALL" || sampled.Load() != 1 || len(report.Sources) != 2 || report.AvailabilityChecks.PlaybackStartedCount != 1 {
		t.Fatalf("global search lost new free site or default: %+v samples=%d", report, sampled.Load())
	}
	if report.Sources[0].Provider != "Public Domain Movies" || !playbackStarted(report.Sources[0]) || report.Sources[0].PlaybackCheck.Country != "TZ" {
		t.Fatal("observed free playback was not prioritized in global results")
	}
	paid := report.Sources[1]
	if paid.AccessType != "subscription" || paid.PlaybackCheck != nil || paid.DirectStreamURL != "" {
		t.Fatalf("paid provider lost its access boundary: %+v", paid)
	}
	var resolved bool
	for _, provider := range report.Providers {
		if provider.Name == "Free movie source resolution" && provider.Status == "ok" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("free source coverage was not reported")
	}
}
