package scout

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

func TestWebSearchBypassesCachedReportsAndDoesNotRetainResults(t *testing.T) {
	for _, provider := range []struct{ name, key, host, body string }{
		{"Brave", "BRAVE_SEARCH_API_KEY", "api.search.brave.com", `{"web":{"results":[{"title":"Fixture Original Film","url":"https://movie.example/watch/fixture","description":"Source page"}]}}`},
		{"Tavily", "TAVILY_API_KEY", "api.tavily.com", `{"results":[{"title":"Fixture Original Film","url":"https://movie.example/watch/fixture","content":"Source page"}]}`},
	} {
		t.Run(provider.name, func(t *testing.T) {
			t.Setenv("BRAVE_SEARCH_API_KEY", "")
			t.Setenv("TAVILY_API_KEY", "")
			t.Setenv(provider.key, "fixture-key")
			s := New(shield.New(), "http://127.0.0.1:8080")
			s.cache["fixture original film|US"] = cachedSearch{
				at: time.Now(), report: SearchReport{Query: "Stale catalog-only report", Sources: []VerifiedSource{}},
			}
			var webRequests atomic.Int32
			s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == provider.host {
					webRequests.Add(1)
					return searchFixtureResponse(200, "application/json", provider.body), nil
				}
				return searchFixtureResponse(200, "application/json", `{"response":{"docs":[]},"data":[],"query":{"pages":{}},"results":[]}`), nil
			})}
			s.availabilityHTTPClient = s.httpClient
			for i := 0; i < 2; i++ {
				report := s.SearchWithRegion(context.Background(), "Fixture Original Film", "US")
				if report.Cached || len(report.Sources) != 1 || !report.Sources[0].ListingOnly || report.Sources[0].Verified {
					t.Fatalf("web search used a stale report or lost live results: cached=%v, sources=%d", report.Cached, len(report.Sources))
				}
			}
			if webRequests.Load() != 2 {
				t.Fatalf("expected one fresh web request per search, got %d", webRequests.Load())
			}
			if cached := s.cache["fixture original film|US"]; cached.report.Query != "Stale catalog-only report" {
				t.Fatal("web API results were retained in the report cache")
			}
		})
	}
}
