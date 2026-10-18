package scout

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

func TestObservedTanzaniaReachabilityRanksAboveStaticCountryClaims(t *testing.T) {
	observed := func(status, country string) VerifiedSource {
		return VerifiedSource{CandidateSource: CandidateSource{AvailabilityStatus: "unknown"}, AvailabilityCheck: &AvailabilityCheck{Status: status, Country: country}}
	}
	media, page, foreign, blocked, missing := observed("media_reachable", "TZ"), observed("page_reachable", "TZ"), observed("page_reachable", "US"), observed("region_blocked", "TZ"), observed("not_found", "TZ")
	localListing := VerifiedSource{CandidateSource: CandidateSource{AvailabilityStatus: "regional_listing"}}
	if !(checkedWatchPriority(media, "TZ") < checkedWatchPriority(page, "TZ") && checkedWatchPriority(page, "TZ") < checkedWatchPriority(localListing, "TZ") && checkedWatchPriority(localListing, "TZ") < checkedWatchPriority(foreign, "TZ") && checkedWatchPriority(foreign, "TZ") < checkedWatchPriority(blocked, "TZ") && checkedWatchPriority(foreign, "TZ") < checkedWatchPriority(missing, "TZ")) {
		t.Fatal("observed local reachability did not outrank service-only, foreign or failed checks")
	}
	if checkedWatchPriority(observed("page_reachable", ""), "TZ") <= checkedWatchPriority(localListing, "TZ") {
		t.Fatal("unknown check country was presented as Tanzania evidence")
	}
	foreignStore := observed("page_reachable", "TZ")
	foreignStore.AvailabilityStatus = "other_region"
	if checkedWatchPriority(foreignStore, "TZ") <= checkedWatchPriority(localListing, "TZ") {
		t.Fatal("loading a foreign storefront was promoted above a local catalog listing")
	}
}

func TestGlobalRankingUsesPlaybackAndReachabilityWithoutCountryPreference(t *testing.T) {
	for _, country := range []string{"TZ", "US", "JP", ""} {
		started := playbackFixture("started")
		started.PlaybackCheck = startedFixture("ALL", country)
		media := playbackFixture("media")
		media.AvailabilityCheck.Country = country
		page := VerifiedSource{AvailabilityCheck: &AvailabilityCheck{Status: "page_reachable", Country: country}}
		unchecked := VerifiedSource{CandidateSource: CandidateSource{AvailabilityStatus: "regional_listing", Region: "TZ"}}
		if !(checkedWatchPriority(started, "ALL") < checkedWatchPriority(media, "ALL") && checkedWatchPriority(media, "ALL") < checkedWatchPriority(page, "ALL") && checkedWatchPriority(page, "ALL") < checkedWatchPriority(unchecked, "ALL")) {
			t.Errorf("global priority depended on the check country %q", country)
		}
		page.AvailabilityStatus = "other_region"
		if checkedWatchPriority(page, "ALL") != 10 {
			t.Errorf("stale regional evidence penalized a global source: %+v", page)
		}
	}
	for _, status := range []string{"regional_listing", "service_available", "other_region", "unknown"} {
		source := VerifiedSource{CandidateSource: CandidateSource{AvailabilityStatus: status}}
		if checkedWatchPriority(source, "ALL") != 42 {
			t.Errorf("global static priority depends on regional status %q", status)
		}
	}
}

func TestAutomaticSearchChecksMoviePagesOnceAndKeepsPlaybackUnconfirmed(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	var movieChecks atomic.Int32
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "api.tavily.com" {
			return searchFixtureResponse(200, "application/json", `{"results":[{"title":"Fixture Film (2014)","url":"https://www.netflix.com/title/blocked"},{"title":"Fixture Film (2014)","url":"https://www.primevideo.com/detail/working"}]}`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "www.cloudflare.com" {
			return searchFixtureResponse(200, "text/plain", "loc=TZ\n"), nil
		}
		if req.URL.Hostname() != "www.netflix.com" && req.URL.Hostname() != "www.primevideo.com" {
			return searchFixtureResponse(200, "application/json", `{"data":[]}`), nil
		}
		movieChecks.Add(1)
		if req.URL.Hostname() == "www.netflix.com" {
			return searchFixtureResponse(200, "text/html", `<title>Fixture Film (2014)</title><main><h1>Fixture Film (2014)</h1><p>This title is not available in your country.</p></main>`), nil
		}
		return searchFixtureResponse(200, "text/html", `<title>Fixture Film (2014)</title><main><h1>Fixture Film (2014)</h1><p>This is the movie detail page for the requested feature film. Sign-in and the selected membership or rental offer may be required to play this film.</p></main>`), nil
	})}
	report := s.SearchWithRegion(context.Background(), "Fixture Film 2014", "TZ")
	if len(report.Sources) != 2 || movieChecks.Load() != 2 || report.AvailabilityChecks == nil || report.AvailabilityChecks.Country != "TZ" || report.AvailabilityChecks.CheckedCount != 2 {
		t.Fatalf("automatic checks missing: %+v checks=%d", report, movieChecks.Load())
	}
	first, last := report.Sources[0], report.Sources[1]
	if first.AvailabilityCheck.Status != "page_reachable" || last.AvailabilityCheck.Status != "region_blocked" || !strings.Contains(first.URL, "primevideo") {
		t.Fatalf("working page not first: %+v", report.Sources)
	}
	for _, result := range report.Sources {
		if result.Verified || result.AvailabilityCheck.PlaybackConfirmed || result.DirectStreamURL != "" {
			t.Errorf("HTTP page claimed playback: %+v", result)
		}
	}
}

func TestAutomaticPageCheckBudgetLeavesOtherLinksUnchecked(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	var rows []string
	for i := 0; i < 10; i++ {
		rows = append(rows, fmt.Sprintf(`{"title":"Fixture Film (2014)","url":"https://www.netflix.com/title/%d"}`, i))
	}
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "api.tavily.com" {
			return searchFixtureResponse(200, "application/json", `{"results":[`+strings.Join(rows, ",")+`]}`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	var requests atomic.Int32
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "www.cloudflare.com" {
			return searchFixtureResponse(200, "text/plain", "loc=TZ\n"), nil
		}
		if req.URL.Hostname() != "www.netflix.com" {
			return searchFixtureResponse(200, "application/json", `{"data":[]}`), nil
		}
		requests.Add(1)
		return searchFixtureResponse(403, "text/html", "access check blocked"), nil
	})}
	report := s.SearchWithRegion(context.Background(), "Fixture Film 2014", "TZ")
	if len(report.Sources) != 10 || requests.Load() != maxAutomaticPageChecks || report.AvailabilityChecks.CheckedCount != maxAutomaticPageChecks {
		t.Fatalf("page budget not enforced: pages=%d requests=%d", len(report.Sources), requests.Load())
	}
	unchecked := 0
	for _, source := range report.Sources {
		if source.AvailabilityCheck.Method == "not_checked" {
			unchecked++
			if source.AvailabilityCheck.CheckedAt != "" {
				t.Error("unchecked link has a false check timestamp")
			}
		}
	}
	if unchecked != 4 {
		t.Fatalf("missing explicit unchecked links: %d", unchecked)
	}
}

func TestMediaHTTPFailuresKeepTheirDistinctObservation(t *testing.T) {
	for code, want := range map[int]string{404: "not_found", 451: "region_blocked", 403: "access_restricted", 401: "sign_in_required"} {
		check := mediaAvailabilityResult(VerifiedSource{PrunedReason: fmt.Sprintf("Media returned HTTP %d example", code)}, "TZ", "TZ")
		if check.Status != want || check.HTTPStatus != code || check.PlaybackConfirmed {
			t.Errorf("HTTP%d lost: %+v", code, check)
		}
	}
}
