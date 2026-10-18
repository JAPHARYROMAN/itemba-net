package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTavilyUsesOneCreditRequestAndServerOnlyCredentials(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-tavily-private-key")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != tavilySearchEndpoint {
			t.Errorf("unexpected endpoint: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer fixture-tavily-private-key" || req.Header.Get("Content-Type") != "application/json" {
			t.Error("missing server-side request authentication or JSON content type")
		}
		body, _ := io.ReadAll(req.Body)
		if strings.Contains(string(body), "fixture-tavily-private-key") || strings.Contains(req.URL.String(), "fixture-tavily-private-key") {
			t.Error("credentials leaked outside the authentication header")
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("invalid request JSON: %v", err)
		}
		if payload["search_depth"] != "basic" || payload["topic"] != "general" || payload["max_results"] != float64(20) || payload["chunks_per_source"] != float64(1) {
			t.Errorf("unexpected search scope or cost: %+v", payload)
		}
		for _, field := range []string{"auto_parameters", "include_answer", "include_raw_content", "include_images"} {
			if value, exists := payload[field]; !exists || value != false {
				t.Errorf("%s must be explicitly disabled", field)
			}
		}
		if payload["country"] != "tanzania" || !strings.Contains(payload["query"].(string), "Night of the Living Dead 1968") {
			t.Errorf("country preference or full title/year lost: %+v", payload)
		}
		if payload["include_domains_mode"] != "prefer" || !strings.Contains(payload["query"].(string), "Tanzania") {
			t.Error("watch destinations and Tanzania were not preferred")
		}
		encodedDomains, _ := json.Marshal(payload["include_domains"])
		encodedExcluded, _ := json.Marshal(payload["exclude_domains"])
		if !strings.Contains(string(encodedDomains), "netflix.com") || !strings.Contains(string(encodedDomains), "azamtvmax.com") || !strings.Contains(string(encodedExcluded), "imdb.com") {
			t.Error("watch services or reference-page exclusions were missing")
		}
		return providerResponse(200, `{"results":[{"title":"Watch Night of the Living Dead (1968) Online | Prime Video","url":"https://www.primevideo.com/detail/feature","content":"Catalog description &amp; details.","score":0.95}]}`), nil
	})
	got, status := s.queryTavilyMovieSites(context.Background(), "Night of the Living Dead 1968", "TZ")
	if len(got) != 1 || status.Status != "ok" || !strings.Contains(status.Detail, "tanzania") {
		t.Fatalf("unexpected results/status: %+v %+v", got, status)
	}
	if got[0].Title != "Night of the Living Dead (1968)" || relevanceScore("Night of the Living Dead 1968", got[0]) == 0 {
		t.Errorf("normalized movie title failed relevance: %+v", got[0])
	}
	if !strings.Contains(got[0].Description, "Page title: Watch Night of the Living Dead (1968)") || !strings.Contains(got[0].Description, "Catalog description & details.") {
		t.Errorf("original source evidence was lost: %+v", got[0])
	}
	if got[0].AccessType != "unknown" || !got[0].ListingOnly || got[0].DirectStreamURL != "" || got[0].Price != "" || got[0].Region != "Unknown" || len(got[0].KnownManifests) != 0 {
		t.Errorf("search result asserted availability or playback: %+v", got[0])
	}
}

func TestTavilyCountryPreferencesAreDocumentedAndOptional(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	for _, tc := range []struct {
		region, country string
	}{
		{"US", "united states"}, {"GB", "united kingdom"}, {"IN", "india"},
		{"ZA", "south africa"}, {"KE", "kenya"}, {"TZ", "tanzania"},
		{"CA", "canada"}, {"AU", "australia"}, {"DE", "germany"}, {"FR", "france"},
		{"ALL", ""}, {"", ""}, {"ZZ", ""},
	} {
		t.Run(tc.region, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				var payload map[string]any
				_ = json.NewDecoder(req.Body).Decode(&payload)
				value, present := payload["country"]
				if tc.country == "" && present || tc.country != "" && value != tc.country {
					t.Errorf("incorrect country preference: %+v", payload)
				}
				return providerResponse(200, `{"results":[]}`), nil
			})
			_, status := s.queryTavilyMovieSites(context.Background(), "Film", tc.region)
			if status.Status != "empty" || tc.region == "ZZ" && !strings.Contains(status.Detail, "global results were used") {
				t.Errorf("unsupported preference or empty search was not explained: %+v", status)
			}
		})
	}
}

func TestGlobalWebSearchHasNoCountryOrPreferredProviderBias(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-key")
	for _, region := range []string{"ALL", "", " all "} {
		t.Run(region, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == "api.tavily.com" {
					var payload map[string]any
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					for _, key := range []string{"country", "include_domains", "include_domains_mode"} {
						if _, present := payload[key]; present {
							t.Errorf("global request retained %s bias", key)
						}
					}
					query, _ := payload["query"].(string)
					if strings.Contains(strings.ToLower(query), "tanzania") || !strings.Contains(query, "free") || !strings.Contains(query, "rent buy") {
						t.Errorf("global query lost free/paid scope or retained Tanzania: %q", query)
					}
					return providerResponse(200, `{"results":[]}`), nil
				}
				if _, present := req.Header["X-Loc-Country"]; present {
					t.Error("global Brave request included a location header")
				}
				query := req.URL.Query().Get("q")
				if req.URL.Query().Get("country") != "ALL" || strings.Contains(strings.ToLower(query), "tanzania") || !strings.Contains(query, "free") {
					t.Errorf("Brave global request was country biased: %s", req.URL)
				}
				return providerResponse(200, `{"web":{"results":[]}}`), nil
			})
			_, _ = s.queryTavilyMovieSites(context.Background(), "Fixture Film 2014", region)
			_, _ = s.queryBraveMovieSites(context.Background(), "Fixture Film 2014", region)
		})
	}
}

func TestMovieWatchQueryIncludesSubscriptionAndDirectProviderOptions(t *testing.T) {
	query := movieWatchWebQuery("Fixture Film 2026", "ALL")
	for _, scope := range []string{"Fixture Film 2026", "official", "subscription", "rent buy", "free"} {
		if !strings.Contains(query, scope) {
			t.Errorf("global watch query omitted %q: %s", scope, query)
		}
	}
	if strings.Contains(query, "Tanzania") {
		t.Fatal("global query introduced a country preference")
	}
	if relevanceScore("Fixture Film 2026", CandidateSource{Title: "Fixture Film 2026 reveal"}) != 0 {
		t.Fatal("a promotional reveal became a full movie candidate")
	}
}

func TestTavilyFiltersUnsafeDestinationsDuplicatesAndDerivatives(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		return providerResponse(200, `{"results":[
			{"title":"Watch Inception | Netflix","url":"https://www.netflix.com/title/1","content":"Movie page"},
			{"title":"Duplicate","url":"https://www.netflix.com/title/1"},
			{"title":"Watch Inception (Trailer) | Netflix","url":"https://www.netflix.com/title/trailer"},
			{"title":"Inception review","url":"https://review.example/movie"},
			{"title":"Inception","url":"javascript:alert(1)"},
			{"title":"Inception","url":"file:///private/file"},
			{"title":"Inception","url":"https://user:password@stream.example/movie"},
			{"title":"Inception","url":"/relative/movie"},
			{"title":"   ","url":"https://stream.example/empty-title"},
			{"title":"Watch Inception 2 | Netflix","url":"https://www.netflix.com/title/sequel"},
			{"title":"Watch Inception | Netflix","url":"https://netflix.com.impostor.example/movie"}
		]}`), nil
	})
	got, status := s.queryTavilyMovieSites(context.Background(), "Inception", "TZ")
	if len(got) != 3 || status.Status != "ok" || got[0].Title != "Inception" {
		t.Fatalf("invalid source filtering: %+v %+v", got, status)
	}
	if len(rankCandidates("Inception", got, false)) != 1 {
		t.Errorf("a sequel or impostor title survived common relevance: %+v", got)
	}
}

func TestTavilyNoKeyDoesNotCallNetwork(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", " ")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		t.Fatal("unconfigured Tavily must not call the network")
		return nil, nil
	})
	got, status := s.queryTavilyMovieSites(context.Background(), "Film", "TZ")
	if len(got) != 0 || status.Status != "not_configured" {
		t.Errorf("missing key was hidden: %+v %+v", got, status)
	}
}

func TestWebMovieDiscoveryDropsTicketsAndChannelsButPreservesVideoPages(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-key")
	const results = `{"results":[
		{"title":"Interstellar (2014) Tickets & Showtimes","url":"https://www.fandango.com/interstellar/movie-overview"},
		{"title":"Interstellar Movie","url":"https://www.youtube.com/interstellarmovie"},
		{"title":"Interstellar Movie","url":"https://www.youtube.com/@interstellarmovie/videos"},
		{"title":"Interstellar Movie","url":"https://www.youtube.com/channel/abc"},
		{"title":"Interstellar (2014)","url":"https://athome.fandango.com/content/browse/details/Interstellar/123"},
		{"title":"Interstellar (2014)","url":"https://www.youtube.com/watch?v=movie"},
		{"title":"Interstellar (2014)","url":"https://www.youtube.com/embed/movie"},
		{"title":"Interstellar (2014)","url":"https://youtu.be/movie"}
	]}`
	for _, provider := range []string{"Tavily", "Brave"} {
		t.Run(provider, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				if provider == "Brave" {
					return providerResponse(200, `{"web":`+results+`}`), nil
				}
				return providerResponse(200, results), nil
			})
			var got []CandidateSource
			if provider == "Brave" {
				got, _ = s.queryBraveMovieSites(context.Background(), "Interstellar 2014", "ALL")
			} else {
				got, _ = s.queryTavilyMovieSites(context.Background(), "Interstellar 2014", "ALL")
			}
			if len(got) != 4 || got[0].Provider != "athome.fandango.com" {
				t.Fatalf("ticket/channel filtering lost movie pages: %+v", got)
			}
		})
	}
}

func TestTavilyErrorsAreActionableAndSecretSafe(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-private-key")
	for _, tc := range []struct {
		name   string
		code   int
		body   string
		action string
	}{
		{"unauthorized", 401, `{"detail":"fixture-private-key"}`, "Check the server's API key"},
		{"access denied", 403, `{"detail":"fixture-private-key"}`, "account access"},
		{"rate limit", 429, `{"detail":"fixture-private-key"}`, "Wait and try again"},
		{"plan credits", 432, `{"detail":"fixture-private-key"}`, "remaining search credits"},
		{"pay as you go limit", 433, `{"detail":"fixture-private-key"}`, "Tavily dashboard"},
		{"server error", 500, `{"detail":"fixture-private-key"}`, "Try again later"},
		{"malformed JSON", 200, `<html>fixture-private-key</html>`, "unreadable response"},
		{"missing results", 200, `{"detail":"fixture-private-key"}`, "unreadable response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				return providerResponse(tc.code, tc.body), nil
			})
			got, status := s.queryTavilyMovieSites(context.Background(), "Film", "TZ")
			if len(got) != 0 || status.Status != "error" || !strings.Contains(status.Detail, tc.action) || strings.Contains(status.Detail, "fixture-private-key") {
				t.Errorf("unsafe or unhelpful error: %+v", status)
			}
		})
	}
}

func TestTavilyResponseAndResultCountsAreBounded(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		var results []tavilyMovieResult
		for i := 0; i < 25; i++ {
			results = append(results, tavilyMovieResult{Title: "Film", URL: fmt.Sprintf("https://stream.example/movie/%d", i)})
		}
		body, _ := json.Marshal(map[string]any{"results": results})
		return providerResponse(200, string(body)), nil
	})
	got, _ := s.queryTavilyMovieSites(context.Background(), "Film", "TZ")
	if len(got) != providerResultLimit {
		t.Errorf("unbounded result count: %d", len(got))
	}
	s.httpClient.Transport = movieProviderTransport(func(req *http.Request) (*http.Response, error) {
		return providerResponse(200, `{"results":[{"title":"`+strings.Repeat("x", 3<<20)+`"}]}`), nil
	})
	got, status := s.queryTavilyMovieSites(context.Background(), "Film", "TZ")
	if len(got) != 0 || status.Status != "error" || !strings.Contains(status.Detail, "unreadable") {
		t.Errorf("oversized response was not bounded: %+v", status)
	}
}

func TestTavilyCancellationIsObservable(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, status := s.queryTavilyMovieSites(ctx, "Film", "TZ")
	if len(got) != 0 || status.Status != "error" || !strings.Contains(status.Detail, "canceled or timed out") {
		t.Errorf("cancellation was hidden: %+v %+v", got, status)
	}
}

func TestTavilyHasIndependentBudgetAndHonorsSearchDeadline(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Run("slow web response survives catalog timeout", func(t *testing.T) {
		s := providerTestScout(func(req *http.Request) (*http.Response, error) {
			if req.URL.Hostname() == "api.tavily.com" {
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) < 9*time.Second || time.Until(deadline) > tavilyRequestTimeout {
					t.Errorf("unexpected web request deadline: %v", deadline)
				}
			}
			select {
			case <-time.After(60 * time.Millisecond):
				return providerResponse(200, `{"results":[]}`), nil
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		})
		s.httpClient.Timeout = 20 * time.Millisecond
		_, status := s.queryTavilyMovieSites(context.Background(), "Film", "TZ")
		if status.Status != "empty" {
			t.Fatalf("web request inherited catalog timeout: %+v", status)
		}
		if s.httpClient.Timeout != 20*time.Millisecond {
			t.Fatal("web request mutated the shared client's timeout")
		}
		var catalogResponse any
		if err := s.fetchProviderJSON(context.Background(), "https://catalog.example/movies", nil, &catalogResponse); err == nil {
			t.Fatal("catalog request lost its original timeout")
		}
	})
	t.Run("parent deadline wins", func(t *testing.T) {
		s := providerTestScout(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, status := s.queryTavilyMovieSites(ctx, "Film", "TZ")
		if status.Status != "error" || !strings.Contains(status.Detail, "canceled or timed out") || time.Since(started) > time.Second {
			t.Fatalf("parent search deadline was ignored: %+v", status)
		}
	})
}

func TestWebProviderSelectionUsesOnlyOneConfiguredAccount(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-tavily-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-brave-key")
	var called []string
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		called = append(called, req.URL.Hostname())
		if req.URL.Hostname() == "api.tavily.com" {
			return providerResponse(401, `{"detail":"invalid fixture key"}`), nil
		}
		return providerResponse(200, `{"web":{"results":[]}}`), nil
	})
	_, statuses := s.queryMovieWebProvider(context.Background(), "Film", "TZ")
	if len(called) != 1 || called[0] != "api.tavily.com" || len(statuses) != 1 || statuses[0].Name != "Tavily web search" || statuses[0].Status != "error" {
		t.Fatalf("Tavily preference or no-fallback rule failed: calls=%+v statuses=%+v", called, statuses)
	}
	t.Setenv("TAVILY_API_KEY", "")
	called = nil
	_, statuses = s.queryMovieWebProvider(context.Background(), "Film", "TZ")
	if len(called) != 1 || called[0] != "api.search.brave.com" || len(statuses) != 1 || statuses[0].Name != "Brave web search" {
		t.Fatalf("Brave-only configuration failed: calls=%+v statuses=%+v", called, statuses)
	}
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	called = nil
	_, statuses = s.queryMovieWebProvider(context.Background(), "Film", "TZ")
	if len(called) != 0 || len(statuses) != 2 || statuses[0].Status != "not_configured" || statuses[1].Status != "not_configured" {
		t.Fatalf("unconfigured providers should be explicit: calls=%+v statuses=%+v", called, statuses)
	}
}
