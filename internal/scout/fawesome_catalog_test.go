package scout

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/shield"
)

// The guest flow fixtures mirror the fawesome driver's own parity test in
// internal/crawler; here they prove the shelf and the search read the same
// catalog through the driver, and that the shelf invents nothing.

const fawesomeGuestBundleFixture = `appConfig.apiEnv="production",appConfig.appId=9,appConfig.siteId=1,window.platform_id||(appConfig.platform_id=12345),appInfo={buildVersion:"v987"};function getSearchURL(){return "recipes.php"} initSecurityToken="getSecurityToken.php";i.token=appConfig.securityToken;getIptvParameterFromUrl:function(e){var a={iptv:!0,siteId:236,platform_id:12345,buildVer:appInfo.buildVersion};return o[e]?o[e]:a[e]},getCustomParameterFromCustomUrl:function(){};appConfig.siteId=helper.getIptvParameterFromUrl("siteId");if(testParam){appConfig.apiEnv="development"}else if(betaParam){appConfig.apiEnv="beta"}`
const fawesomeGuestTokenFixture = "fixture-ephemeral-guest-token"

type fawesomeGuestItem struct {
	NodeID     int64  `json:"node_id"`
	Title      string `json:"title"`
	WebURL     string `json:"web_url"`
	Runtime    any    `json:"runtime,omitempty"`
	ItemType   string `json:"item_type"`
	FeedType   string `json:"feed_type"`
	ActionType string `json:"actiontype"`
	BlockItem  string `json:"blockItem"`
	Date       string `json:"date,omitempty"`
	Desc       string `json:"description"`
}

func fawesomeGuestFixtureItem() fawesomeGuestItem {
	return fawesomeGuestItem{NodeID: 10876543, Title: "Beyond Known Examples", WebURL: "movies/10876543/beyond-known-examples", Runtime: 5400, ItemType: "movie", FeedType: "video", ActionType: "video", BlockItem: "false", Date: "Oct. 02, 2026", Desc: "A public guest feature listing."}
}

// fawesomeGuestTransport answers the public guest endpoints: the shell, the
// bundle, the token, the home rows, and recipes.php for a search, the
// popular feed and the new-arrivals feed alike.
func fawesomeGuestTransport(t *testing.T, calls *atomic.Int32, items ...fawesomeGuestItem) searchFixtureTransport {
	t.Helper()
	return searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("public discovery forwarded account headers")
		}
		body, kind := "", "application/json"
		switch req.URL.Path {
		case "/":
			body, kind = `<html><script src="/home/new/v987/assets/js/iptv-main.js?ver=2345"></script></html>`, "text/html"
		case "/home/new/v987/assets/js/iptv-main.js":
			body, kind = fawesomeGuestBundleFixture, "application/javascript"
		case "/home/new/v987/api/getSecurityToken.php":
			if req.URL.Query().Get("siteId") != "236" || req.Header.Get("token") != "" {
				t.Error("guest initialisation did not use the effective public configuration")
			}
			body = `{"securityToken":"` + fawesomeGuestTokenFixture + `"}`
		case "/home/new/v987/api/sub-categories.php":
			body = `{"status":"ok","channels":[{"name":"New Arrivals TV","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=111"},{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=mixedcontent&keys=1234&order=2&rowType=mixedcontent&dltype=0&gridstyle=flat-movie&highlight=0&akp=1-1234&userType=new&version=sv9.0&sort_type=&appId=9&siteId=1&auth-token=fixture&deviceId=&session-id="}]}`
		case "/home/new/v987/api/recipes.php":
			q := req.URL.Query()
			if q.Get("siteId") != "236" || q.Get("apiEnv") != "production" || q.Get("auth-token") != "12345" || req.Header.Get("token") != fawesomeGuestTokenFixture || req.Header.Get("X-Requested-With") != "XMLHttpRequest" || req.Header.Get("Referer") != "https://fawesome.tv/" {
				t.Errorf("the guest request did not carry the runtime configuration: %s", req.URL.RawQuery)
			}
			if q.Get("country") != "" || q.Get("countryCode") != "" || q.Get("ip_address") != "" || q.Get("deviceId") != "" || q.Get("session-id") != "" {
				t.Error("a feed or query replaced the actual location or identity")
			}
			encoded, _ := json.Marshal(map[string]any{"status": "ok", "type": "result", "results": items})
			body = string(encoded)
		default:
			t.Errorf("unobserved public guest endpoint: %s", req.URL.Path)
			body = `{}`
		}
		r := searchFixtureResponse(200, kind, body)
		r.Request = req
		return r, nil
	})
}

func TestFawesomeShelfReadsTheDriverFeedsAndKeepsCatalogDatesApart(t *testing.T) {
	var calls atomic.Int32
	item := fawesomeGuestFixtureItem()
	noRuntime := item
	noRuntime.NodeID, noRuntime.WebURL, noRuntime.Runtime = 10876544, "movies/10876544/unknown-runtime", nil
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: fawesomeGuestTransport(t, &calls, item, noRuntime, item)}}
	s.UseDrivers(testDrivers(t))
	report := s.DiscoverFawesomeMovies(context.Background(), "popular", "TZ")
	if report.Cached || report.Region != "TZ" || report.Mode != "popular" || len(report.Providers) != 1 || report.Providers[0].Status != "ok" || report.Providers[0].Name != "Fawesome public catalog" || calls.Load() != 4 {
		t.Fatalf("popular shelf: %+v calls=%d", report, calls.Load())
	}
	// A duplicate and a film without a known runtime are not shelf items, and
	// the catalog date never became a year.
	if len(report.Items) != 1 || report.Items[0].ID != "fawesome-catalog-10876543" || report.Items[0].Query != "Beyond Known Examples" || report.Items[0].SourceURL != "https://fawesome.tv/movies/10876543/beyond-known-examples" || report.Items[0].SourceName != "Fawesome catalog" || report.Items[0].CatalogDate != "2026-10-02" || report.Items[0].ReleaseYear != 0 || report.Items[0].ReleaseDate != "" || len(report.Items[0].Markets) != 0 {
		t.Fatalf("catalog items: %+v", report.Items)
	}
	recent := s.DiscoverFawesomeMovies(context.Background(), "recent", "ALL")
	if recent.Cached || len(recent.Items) != 1 || calls.Load() != 7 || recent.Providers[0].Status != "ok" || !strings.Contains(recent.Detail, "catalog date is not") {
		t.Fatalf("recent shelf (warm configuration, then token, rows and feed): %+v calls=%d", recent, calls.Load())
	}
	if again := s.DiscoverFawesomeMovies(context.Background(), "recent", "US"); !again.Cached || again.Region != "US" || calls.Load() != 7 {
		t.Fatal("a cached shelf was refetched or lost its region echo")
	}
	encoded, _ := json.Marshal(recent)
	if strings.Contains(string(encoded), fawesomeGuestTokenFixture) || strings.Contains(string(encoded), "auth-token") || strings.Contains(string(encoded), "12345") {
		t.Fatal("guest configuration escaped into the shelf report")
	}
	// The browsed catalog then backs a title search without a new feed.
	if matches := s.fawesomeCatalogMovieCandidates("Beyond Known Examples"); len(matches) != 1 || matches[0].URL != report.Items[0].SourceURL || !matches[0].ListingOnly {
		t.Fatalf("browsed catalog candidates: %+v", matches)
	}
}

func TestFawesomeShelfReportsAnUnavailableCatalogAndForgetsAnExpiredOne(t *testing.T) {
	requests := 0
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("fixture public catalog unavailable")
	})}, discoveryCache: map[string]cachedMovieDiscovery{"fawesome:popular": {
		at: time.Now().Add(-movieDiscoveryCacheTTL - time.Second), report: MovieDiscoveryReport{Mode: "popular", Region: "ALL", Items: []MovieDiscoveryItem{{Title: "Old catalog"}}, Providers: []ProviderStatus{{Status: "ok"}}},
	}}}
	s.UseDrivers(testDrivers(t))
	report := s.DiscoverFawesomeMovies(context.Background(), "popular", "ALL")
	if report.Cached || len(report.Items) != 0 || requests != 1 || len(report.Providers) != 1 || report.Providers[0].Status != "error" || fawesomeCatalogTTL(report) != time.Minute {
		t.Fatalf("expired catalog success survived an unavailable refresh: %+v requests=%d", report, requests)
	}
	// Without the driver the shelf says so instead of guessing an endpoint.
	bare := &Scout{availabilityHTTPClient: s.availabilityHTTPClient}
	if report := bare.DiscoverFawesomeMovies(context.Background(), "recent", "ALL"); len(report.Providers) != 1 || report.Providers[0].Status != "error" || requests != 1 || !strings.Contains(report.Providers[0].Detail, "driver") {
		t.Fatalf("a missing driver must be an explicit error, not a request: %+v", report)
	}
}

func TestFawesomeCatalogCacheEchoesRegionWithoutChangingGlobalItems(t *testing.T) {
	requests := 0
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected cached catalog request")
	})}, discoveryCache: map[string]cachedMovieDiscovery{"fawesome:recent": {
		at: time.Now(), report: MovieDiscoveryReport{Mode: "recent", Region: "ALL", Items: []MovieDiscoveryItem{{Title: "Example", Query: "Example", Markets: []string{}}}, Providers: []ProviderStatus{{Name: "Fawesome public catalog", Status: "ok"}}},
	}}}
	report := s.DiscoverFawesomeMovies(context.Background(), "recent", "TZ")
	if !report.Cached || report.Region != "TZ" || len(report.Items) != 1 || report.Items[0].Query != "Example" || len(report.Items[0].Markets) != 0 {
		t.Fatal("cached guest catalog was country-filtered or failed to echo requested region")
	}
	report.Items[0].Title = "Mutated"
	report.Providers[0].Status = "changed"
	if s.discoveryCache["fawesome:recent"].report.Items[0].Title != "Example" {
		t.Fatal("caller changed shared catalog cache")
	}
	second := s.DiscoverFawesomeMovies(context.Background(), "recent", "US")
	if requests != 0 || !second.Cached || second.Region != "US" || second.Items[0].Title != "Example" || second.Providers[0].Status != "ok" {
		t.Fatal("region change fetched or mutated the shared global catalog")
	}
}

func TestFawesomeCatalogItemsNeedAnExactPageAndAKnownRuntime(t *testing.T) {
	listing := atlas.Listing{Title: "Example Movie", URL: "https://fawesome.tv/movies/12345/example-movie", AccessType: "free", Duration: "5400", Year: 2026, CatalogDate: "2026-10-02", Thumbnail: "https://img.fawesome.tv/p.jpg"}
	items := fawesomeCatalogItems([]atlas.Listing{listing, listing})
	if len(items) != 1 || items[0].Query != "Example Movie" || items[0].ReleaseYear != 0 || items[0].ReleaseDate != "" || items[0].CatalogDate != "2026-10-02" || len(items[0].Markets) != 0 || items[0].SourceURL != listing.URL || items[0].Thumbnail != listing.Thumbnail {
		t.Fatalf("catalog date became original year, a region claim or a duplicate: %+v", items)
	}
	for name, mutate := range map[string]func(*atlas.Listing){
		"unknown runtime": func(l *atlas.Listing) { l.Duration = "" },
		"short":           func(l *atlas.Listing) { l.Duration = "600" },
		"series route":    func(l *atlas.Listing) { l.URL = "https://fawesome.tv/tv-shows/12345/example" },
		"priced":          func(l *atlas.Listing) { l.Price = "USD 3.99" },
		"paid":            func(l *atlas.Listing) { l.AccessType = "paid" },
		"untitled":        func(l *atlas.Listing) { l.Title = " " },
	} {
		mutated := listing
		mutate(&mutated)
		if len(fawesomeCatalogItems([]atlas.Listing{mutated})) != 0 {
			t.Fatalf("%s was advertised as a full-length catalog film", name)
		}
	}
}

func TestFawesomeDynamicDiscoveryFlowsThroughGlobalSearchAndNativeProof(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.UseDrivers(testDrivers(t))
	var tavily, samples, guestCalls atomic.Int32
	item := fawesomeGuestFixtureItem()
	guestTransport := fawesomeGuestTransport(t, &guestCalls, item)
	page := fawesomeFixturePage(fawesomeFixtureInfo(), "https://ftmain.cachefly.net/files/main.mp4?signature=private-main-fixture")
	page = strings.NewReplacer("Abduction", item.Title, "10540876", "10876543", "abduction", "beyond-known-examples", "5835", "5400", "PT1H37M15S", "PT1H30M").Replace(page)
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.tavily.com" {
			tavily.Add(1)
			return searchFixtureResponse(200, "application/json", `{"results":[{"title":"Watch Beyond Known Examples | Netflix","url":"https://www.netflix.com/title/81234567"}]}`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		var r *http.Response
		switch req.URL.Host {
		case "www.cloudflare.com":
			r = searchFixtureResponse(200, "text/plain", "loc=TZ\n")
		case "fawesome.tv":
			if req.URL.Path == "/" || strings.HasPrefix(req.URL.Path, "/home/new/") {
				return guestTransport.RoundTrip(req)
			}
			r = searchFixtureResponse(200, "text/html", page)
		case "ftmain.cachefly.net":
			r = searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom")
		case "www.netflix.com":
			r = searchFixtureResponse(200, "text/html", `<title>Beyond Known Examples</title><main><h1>Beyond Known Examples</h1><p>Watch this full movie with a subscription.</p></main>`)
		default:
			r = searchFixtureResponse(200, "application/json", `{"results":[],"data":[],"feed":{"results":[]}}`)
		}
		r.Request = req
		return r, nil
	})}
	s.playbackProbe = func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		samples.Add(1)
		if source.Provider != "Fawesome" || source.AccessType != "free" || !eligibleFreePlayback(source) || region != "ALL" || country != "TZ" {
			t.Error("wrong provider, paid source, region bias or unbound media entered native sampling")
		}
		check := startedFixture(region, country)
		check.DurationSeconds = 5400
		return check
	}
	report := s.SearchWithOptions(context.Background(), item.Title, "", SearchOptions{FullLengthOnly: true})
	if report.Region != "ALL" || tavily.Load() != 1 || samples.Load() != 1 {
		t.Fatalf("dynamic global search did not preserve proof/one broad search: region=%s tavily=%d samples=%d providers=%+v", report.Region, tavily.Load(), samples.Load(), report.Providers)
	}
	found, paid := false, false
	for _, source := range report.Sources {
		if source.Provider == "Fawesome" {
			found = playbackStarted(source)
		}
		if strings.Contains(source.URL, "netflix.com") {
			paid = true
			if source.PlaybackCheck != nil || source.AccessType == "free" {
				t.Fatal("paid title received automatic playback")
			}
		}
	}
	if !found || !paid {
		t.Fatalf("dynamic guest movie or paid offer was lost: %+v", report.Sources)
	}
	encoded, _ := json.Marshal(report)
	for _, private := range []string{fawesomeGuestTokenFixture, "private-main-fixture", "auth-token=12345"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("guest/private media escaped global search JSON")
		}
	}
}

func TestLiveFawesomeCatalog(t *testing.T) {
	mode := os.Getenv("ITEMBA_LIVE_FAWESOME_CATALOG")
	if mode != "popular" && mode != "recent" {
		t.Skip("opt-in bounded public catalog request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fawesomeDiscoveryDeadline)
	defer cancel()
	client := newMovieAvailabilityClient()
	transport := client.Transport
	requests := 0
	client.Transport = searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		response, err := transport.RoundTrip(req)
		if response != nil {
			t.Logf("public guest endpoint=%s HTTP status=%d", path.Base(req.URL.Path), response.StatusCode)
		}
		return response, err
	})
	s := &Scout{availabilityHTTPClient: client}
	s.UseDrivers(testDrivers(t))
	listings, err := s.fawesomeFeed(ctx, mode)
	if err != nil {
		t.Fatal(err)
	}
	items := fawesomeCatalogItems(listings)
	if len(items) == 0 {
		t.Fatal("public catalog returned no full-length movie listings")
	}
	proof := struct {
		Mode      string               `json:"mode"`
		CheckedAt string               `json:"checkedAt"`
		Requests  int                  `json:"requests"`
		Items     []MovieDiscoveryItem `json:"items"`
	}{mode, time.Now().UTC().Format(time.RFC3339), requests, items}
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile("../../.itemba-runtime/fawesome-"+mode+"-catalog-proof.json", encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("dynamic %s catalog returned %d full-length listings in %d public requests", mode, len(items), requests)
	for i, item := range items {
		if i >= 10 {
			break
		}
		t.Logf("title=%s page=%s catalogDate=%s", item.Title, item.SourceURL, item.CatalogDate)
	}
}
