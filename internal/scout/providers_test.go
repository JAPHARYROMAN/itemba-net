package scout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/crawler"
	"time"
	"unicode/utf8"
)

type movieProviderTransport func(*http.Request) (*http.Response, error)

func (f movieProviderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func providerResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func providerTestScout(fn movieProviderTransport) *Scout {
	return &Scout{httpClient: &http.Client{Transport: fn}}
}

func TestMovieProvidersSurfaceFailuresAndDoNotLeakSecrets(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "private-api-key")
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", 401, `{"error":"private-api-key"}`},
		{"rate limited", 429, `{"error":"quota exceeded"}`},
		{"invalid JSON", 200, `<html>unavailable</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				return providerResponse(tc.code, tc.body), nil
			})
			got, status := s.queryBraveMovieSites(context.Background(), "Film", "US")
			if len(got) != 0 || status.Status != "error" || strings.Contains(status.Detail, "private-api-key") || strings.Contains(status.Detail, "quota exceeded") {
				t.Errorf("bad status/result: %+v %+v", got, status)
			}
		})
	}
}

func TestBraveNoKeyIsExplicitAndDoesNotMakeNetworkRequest(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		t.Fatal("unconfigured web search must not make a request")
		return nil, errors.New("unexpected network request")
	})
	got, status := s.queryBraveMovieSites(context.Background(), "Film", "ALL")
	if len(got) != 0 || status.Status != "not_configured" || !strings.Contains(status.Detail, "not configured") {
		t.Errorf("unexpected unconfigured state: %+v %+v", got, status)
	}
}

func TestBraveDestinationLinksRemainUnverifiedAndUnknownPrice(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "test-secret")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("X-Subscription-Token") != "test-secret" || req.Header.Get("X-Loc-Country") != "KE" || req.URL.Query().Get("country") != "ALL" {
			t.Errorf("incorrect Brave request")
		}
		return providerResponse(200, `{"web":{"results":[
			{"title":"Film &amp; More","url":"https://stream.example/movie/123","description":"Watch &amp; enjoy"},
			{"title":"Duplicate","url":"https://stream.example/movie/123"},
			{"title":"Invalid","url":"file:///private/file"},
			{"title":"Credentials","url":"https://user:pass@stream.example/"}
		]}}`), nil
	})
	got, status := s.queryBraveMovieSites(context.Background(), "Film", "KE")
	if status.Status != "ok" || len(got) != 1 {
		t.Fatalf("unexpected results: %+v, %+v", got, status)
	}
	if !got[0].ListingOnly || got[0].DirectStreamURL != "" || got[0].Price != "" || got[0].AccessType != "unknown" || got[0].Region != "Unknown" || got[0].Title != "Film & More" {
		t.Errorf("web result was mislabeled: %+v", got[0])
	}
}

func TestWorldwideStoresAreBoundedAndRegionLabeled(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	var active, peak atomic.Int32
	var mu sync.Mutex
	countries := make(map[string]bool)
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); count > previous && !peak.CompareAndSwap(previous, count); previous = peak.Load() {
		}
		country := req.URL.Query().Get("country")
		mu.Lock()
		countries[country] = true
		mu.Unlock()
		time.Sleep(time.Millisecond)
		return providerResponse(200, fmt.Sprintf(`{"results":[{"kind":"feature-movie","trackId":1,"trackName":"Film","trackViewUrl":"https://itunes.apple.com/%s/movie/film/id1","currency":"USD","trackPrice":9.99}]}`, strings.ToLower(country))), nil
	})
	s.UseDrivers([]*crawler.Driver{testDriver(t, "apple-movies")})
	got, statuses := s.queryDrivers(context.Background(), "Film", "ALL")
	if len(got) != 8 || len(statuses) != 8 || peak.Load() > 4 {
		t.Fatalf("unbounded/wrong stores: results=%d statuses=%d peak=%d", len(got), len(statuses), peak.Load())
	}
	for _, country := range []string{"US", "GB", "IN", "ZA", "DE", "FR", "JP", "AU"} {
		if !countries[country] {
			t.Errorf("missing storefront %s", country)
		}
	}
	for _, candidate := range got {
		if !countries[candidate.Region] || !candidate.ListingOnly {
			t.Errorf("incorrect region label: %+v", candidate)
		}
	}
}

func TestBraveOfficialPageTitlesSurviveRelevanceWithoutChangingAccess(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-key")
	for _, tc := range []struct {
		name, query, pageTitle, destination, movieTitle string
	}{
		{"Netflix", "Inception", "Watch Inception | Netflix Official Site", "https://www.netflix.com/title/123", "Inception"},
		{"Apple Tanzania brand", "Inception", "Inception - Apple TV (TZ)", "https://tv.apple.com/tz/movie/inception/123", "Inception"},
		{"Hulu streaming wrapper", "Inception", "Watch Inception Streaming Online | Hulu", "https://www.hulu.com/movie/inception-123", "Inception"},
		{"Prime UK", "Inception", "Watch Inception | Prime Video", "https://www.amazon.co.uk/gp/video/detail/ABC", "Inception"},
		{"Prime Video", "Night of the Living Dead", "Watch Night of the Living Dead online | Prime Video", "https://www.primevideo.com/detail/123", "Night of the Living Dead"},
		{"IMDb year", "Interstellar 2014", "Interstellar (2014) - IMDb", "https://www.imdb.com/title/tt123/", "Interstellar (2014)"},
		{"Plex subtitle", "Nosferatu", "Watch Nosferatu: A Symphony of Horror (1922) Online - Plex", "https://watch.plex.tv/movie/nosferatu", "Nosferatu: A Symphony of Horror (1922)"},
		{"Archive wrapper", "Night of the Living Dead 1968", "Night of the Living Dead (1968) : Free Download, Borrow, and Streaming : Internet Archive", "https://archive.org/details/night-of-the-living-dead", "Night of the Living Dead (1968)"},
		{"Public Domain Movies", "Night of the Living Dead 1968", "Night of the Living Dead (1968) | Public Domain Movies", "https://publicdomainmovie.net/movie/night-of-the-living-dead", "Night of the Living Dead (1968)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				return providerResponse(200, fmt.Sprintf(`{"web":{"results":[{"title":%q,"url":%q,"description":"Original snippet."}]}}`, tc.pageTitle, tc.destination)), nil
			})
			got, status := s.queryBraveMovieSites(context.Background(), tc.query, "ALL")
			if status.Status != "ok" || len(got) != 1 {
				t.Fatalf("unexpected source/status: %+v %+v", got, status)
			}
			if got[0].Title != tc.movieTitle || relevanceScore(tc.query, got[0]) == 0 {
				t.Errorf("movie identity lost: %+v", got[0])
			}
			if !strings.Contains(got[0].Description, tc.pageTitle) || !strings.Contains(got[0].Description, "Original snippet.") {
				t.Errorf("original page evidence lost: %q", got[0].Description)
			}
			if got[0].AccessType != "unknown" || !got[0].ListingOnly || got[0].DirectStreamURL != "" || got[0].Price != "" || got[0].Region != "Unknown" {
				t.Errorf("normalization asserted access or availability: %+v", got[0])
			}
		})
	}
}

func TestAdditionalOfficialMovieTitleDecorationsKeepIdentityAndYear(t *testing.T) {
	for _, tc := range []struct{ host, raw, want string }{
		{"www.disneyplus.com", "Thunderbolts* | Watch on Disney+", "Thunderbolts*"},
		{"www.paramountplus.com", "Top Gun - Watch Full Movie on Paramount Plus", "Top Gun"},
		{"athome.fandango.com", "Watch, Rent, or Buy Superman: The Movie (1978) at Home + Online | Stream on Fandango (Vudu)", "Superman: The Movie (1978)"},
		{"www.vudu.com", "Watch Interstellar (2014) | Vudu", "Interstellar (2014)"},
		{"watch.plex.tv", "Where to Watch Interstellar (2014) - Plex", "Interstellar (2014)"},
		{"watch.plex.tv", "Watch Free Men (2011) Full Movie Free on Plex - Plex", "Free Men (2011)"},
		{"www.rakuten.tv", "Interstellar (2014) - Rakuten TV", "Interstellar (2014)"},
		{"filmzie.com", "Watch Imprint | Filmzie", "Imprint"},
		{"pluto.tv", "Watch Arrival (2016) | Pluto TV", "Arrival (2016)"},
	} {
		if got := normalizeWebMovieTitle(tc.raw, tc.host); got != tc.want {
			t.Errorf("normalization lost movie identity: %q → %q, want %q", tc.raw, got, tc.want)
		}
	}
	wrongYear := normalizeWebMovieTitle("Where to Watch Interstellar (2025) - Plex", "watch.plex.tv")
	if availabilityMovieMatches(CandidateSource{Title: "Interstellar", ReleaseYear: 2014}, availabilityPage{title: wrongYear}, "watch.plex.tv") {
		t.Fatal("Plex title normalization must preserve an explicit conflicting release year")
	}
	if got := normalizeWebMovieTitle("Where to Watch Interstellar (2014) - Plex", "plex.tv.impostor.example"); got != "Where to Watch Interstellar (2014) - Plex" {
		t.Fatal("official title decorations must not be trusted on lookalike hosts")
	}
}

func TestBraveNormalizationCannotHideDerivativesOrChangeSequels(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-key")
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		return providerResponse(200, `{"web":{"results":[
			{"title":"Watch Inception | Netflix | Trailer","url":"https://www.netflix.com/title/trailer"},
			{"title":"Watch Inception (Review) | Netflix","url":"https://www.netflix.com/title/review"},
			{"title":"Watch Inception 2 | Netflix","url":"https://www.netflix.com/title/sequel"},
			{"title":"Watch Inception | Netflix","url":"https://netflix.com.impostor.example/title/123"},
			{"title":"Watch Inception | Netflix","url":"https://notnetflix.com/title/123"}
		]}}`), nil
	})
	got, _ := s.queryBraveMovieSites(context.Background(), "Inception", "ALL")
	if len(got) != 3 {
		t.Fatalf("derivative evidence was hidden or valid input lost: %+v", got)
	}
	for _, source := range got {
		if relevanceScore("Inception", source) != 0 {
			t.Errorf("normalization promoted an unrelated/impostor title: %+v", source)
		}
	}
	if got[0].Title != "Inception 2" || got[1].Title != "Watch Inception | Netflix" || got[2].Title != "Watch Inception | Netflix" {
		t.Errorf("unexpected title rewrites: %+v", got)
	}
	wrongYear := CandidateSource{Title: normalizeWebMovieTitle("Watch Dune: Part Two (2024) | Netflix", "netflix.com")}
	if relevanceScore("Dune 2021", wrongYear) != 0 {
		t.Errorf("normalization dropped the sequel year: %+v", wrongYear)
	}
}

func TestWebMovieDescriptionIsBoundedAndPreservesUnicode(t *testing.T) {
	description := webMovieDescription("Watch Film | Netflix", "Film", strings.Repeat("映", 1200))
	if utf8.RuneCountInString(description) > 1000 || !utf8.ValidString(description) || !strings.HasPrefix(description, "Page title: Watch Film | Netflix.") {
		t.Errorf("invalid or unbounded original page evidence: %d runes", utf8.RuneCountInString(description))
	}
}

func TestBraveErrorsProvideNextStepsWithoutCredentials(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "fixture-private-key")
	for _, tc := range []struct {
		name   string
		code   int
		body   string
		action string
	}{
		{"invalid key", 401, `{"error":"fixture-private-key"}`, "Check the server's Brave API key"},
		{"plan access", 403, `{"error":"fixture-private-key"}`, "plan includes Web Search"},
		{"quota", 429, `{"error":"fixture-private-key"}`, "Wait and try again"},
		{"malformed", 200, `<html>fixture-private-key</html>`, "Try again later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := providerTestScout(func(req *http.Request) (*http.Response, error) {
				return providerResponse(tc.code, tc.body), nil
			})
			got, status := s.queryBraveMovieSites(context.Background(), "Film", "ALL")
			if len(got) != 0 || status.Status != "error" || !strings.Contains(status.Detail, tc.action) || strings.Contains(status.Detail, "fixture-private-key") {
				t.Errorf("error is not actionable or leaks credentials: %+v", status)
			}
		})
	}
}
