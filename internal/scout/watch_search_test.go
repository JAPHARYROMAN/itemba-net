package scout

import (
	"context"
	"net/http"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

func TestSearchReturnsDirectWatchPagesWithTanzaniaEvidenceFirst(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Hostname() {
		case "itunes.apple.com":
			return searchFixtureResponse(200, "application/json", `{"results":[{"kind":"feature-movie","trackId":1,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/tz/movie/fixture-film/id1","releaseDate":"2014-01-01T00:00:00Z","currency":"TZS","trackPrice":5000}]}`), nil
		case "api.tavily.com":
			return searchFixtureResponse(200, "application/json", `{"results":[
				{"title":"Fixture Film (2014) - IMDb","url":"https://www.imdb.com/title/tt1"},
				{"title":"Fixture Film (2014)","url":"https://www.justwatch.com/us/movie/fixture-film"},
				{"title":"Watch Fixture Film (2014) | Netflix","url":"https://www.netflix.com/title/123"},
				{"title":"Watch Fixture Film (2014) | Prime Video","url":"https://www.primevideo.com/detail/123"},
				{"title":"Fixture Film (2014) - Apple TV (US)","url":"https://tv.apple.com/us/movie/fixture-film/foreign"},
				{"title":"Fixture Film (2014)","url":"https://www.amazon.com/dp/DVD123"}
			]}`), nil
		default:
			return searchFixtureResponse(200, "application/json", `{"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
		}
	})}
	s.availabilityHTTPClient = s.httpClient
	report := s.SearchWithRegion(context.Background(), "Fixture Film 2014", "TZ")
	if len(report.Sources) != 4 {
		t.Fatalf("expected four direct movie options: %+v", report.Sources)
	}
	first := report.Sources[0]
	if first.Region != "TZ" || first.AvailabilityStatus != "regional_listing" || first.Price != "Buy TZS 5000.00" || first.Verified {
		t.Fatalf("regional store evidence was lost or overstated: %+v", first)
	}
	second := report.Sources[1]
	if second.Provider != "www.netflix.com" && second.Provider != "Netflix" {
		t.Fatalf("Tanzania-supported service was not next: %+v", second)
	}
	if second.AccessType != "subscription" || second.AccessBasis != "provider_model" || second.AvailabilityStatus != "service_available" || second.Price != "" || second.Verified {
		t.Errorf("service access was confused with a verified movie offer: %+v", second)
	}
	last := report.Sources[len(report.Sources)-1]
	if last.AvailabilityStatus != "other_region" || last.Region != "US" {
		t.Errorf("foreign storefront was promoted into Tanzania: %+v", last)
	}
	for _, source := range report.Sources {
		if source.DestinationType != "watch" || source.DirectStreamURL != "" || source.Verified {
			t.Errorf("movie listing was presented as confirmed playback: %+v", source)
		}
	}
}

func TestLocalizedHotstarMovieIsAForeignOfferInTanzania(t *testing.T) {
	options := prepareMovieWatchOptions("Interstellar 2014", "TZ", []CandidateSource{{
		Title: "Interstellar (2014)", URL: "https://www.hotstar.com/in/movies/interstellar/1971000531", SourceType: "Web Listing", Region: "Unknown",
	}})
	if len(options) != 1 || options[0].Region != "IN" || options[0].AvailabilityStatus != "other_region" || options[0].AccessType != "unknown" {
		t.Fatalf("foreign route was treated as local access: %+v", options)
	}
}
