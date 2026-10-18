package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

const fawesomeDiscoveryBundleFixture = `appConfig.apiEnv="production",appConfig.appId=9,appConfig.siteId=1,window.platform_id||(appConfig.platform_id=12345),appInfo={buildVersion:"v987"};function getSearchURL(){return "recipes.php"} initSecurityToken="getSecurityToken.php";i.token=appConfig.securityToken;getIptvParameterFromUrl:function(e){var a={iptv:!0,siteId:236,platform_id:12345,buildVer:appInfo.buildVersion};return o[e]?o[e]:a[e]},getCustomParameterFromCustomUrl:function(){};appConfig.siteId=helper.getIptvParameterFromUrl("siteId");if(testParam){appConfig.apiEnv="development"}else if(betaParam){appConfig.apiEnv="beta"}`
const fawesomeDiscoveryGuestFixture = "fixture-ephemeral-guest-token"

func fawesomeDiscoveryFixtureItem() fawesomeDiscoveryItem {
	return fawesomeDiscoveryItem{NodeID: 10876543, Title: "Beyond Known Examples", WebURL: "movies/10876543/beyond-known-examples", Runtime: 5400, ItemType: "movie", FeedType: "video", ActionType: "video", BlockItem: "false", Description: "A public guest feature listing."}
}

func fawesomeDiscoveryFixturePayload(items ...fawesomeDiscoveryItem) string {
	data, _ := json.Marshal(struct {
		Status  string                  `json:"status"`
		Type    string                  `json:"type"`
		Results []fawesomeDiscoveryItem `json:"results"`
	}{"ok", "result", items})
	return string(data)
}

func fawesomeDiscoveryFixtureTransport(t *testing.T, calls *atomic.Int32, items ...fawesomeDiscoveryItem) searchFixtureTransport {
	t.Helper()
	return searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("public discovery forwarded account headers")
		}
		body, kind := "", "application/json"
		switch req.URL.Path {
		case "/":
			body = `<html><script src="/home/new/v987/assets/js/iptv-main.js?ver=2345"></script></html>`
			kind = "text/html"
		case "/home/new/v987/assets/js/iptv-main.js":
			body = fawesomeDiscoveryBundleFixture
			kind = "application/javascript"
		case "/home/new/v987/api/getSecurityToken.php":
			if req.URL.Query().Get("siteId") != "236" {
				t.Error("constructor iFood site selected instead of effective Fawesome guest site")
			}
			if req.Header.Get("token") != "" {
				t.Error("previous guest token forwarded to initialization")
			}
			body = `{"securityToken":"` + fawesomeDiscoveryGuestFixture + `"}`
		case "/home/new/v987/api/recipes.php":
			if req.URL.Query().Get("siteId") != "236" || req.URL.Query().Get("apiEnv") != "production" {
				t.Error("actual Fawesome runtime guest configuration was not used")
			}
			if req.Header.Get("token") != fawesomeDiscoveryGuestFixture || req.Header.Get("X-Requested-With") != "XMLHttpRequest" || req.Header.Get("Referer") != "https://fawesome.tv/" {
				t.Error("normal guest headers were missing")
			}
			if req.URL.Query().Get("country") != "" || req.URL.Query().Get("countryCode") != "" || req.URL.Query().Get("ip_address") != "" {
				t.Error("public movie discovery replaced actual location")
			}
			body = fawesomeDiscoveryFixturePayload(items...)
		default:
			t.Errorf("unobserved public guest endpoint: %s", req.URL.Path)
			body = `{}`
		}
		r := searchFixtureResponse(200, kind, body)
		r.Request = req
		return r, nil
	})
}

func TestFawesomeDiscoveryDynamicallyLoadsPublicConfigAndKeepsGuestTokenRequestScoped(t *testing.T) {
	var calls atomic.Int32
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://fawesome.tv/")
	jar.SetCookies(u, []*http.Cookie{{Name: "existing-account", Value: "fixture"}})
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: fawesomeDiscoveryFixtureTransport(t, &calls, fawesomeDiscoveryFixtureItem()), Jar: jar}}
	for pass, want := range []int32{4, 6} {
		matches, err := s.queryFawesomeMovieSites(context.Background(), "Beyond Known Examples")
		if err != nil || len(matches) != 1 || calls.Load() != want {
			t.Fatalf("pass%d dynamic public guest flow failed: matches=%d calls=%d err=%v", pass, len(matches), calls.Load(), err)
		}
		if matches[0].ReleaseYear != 0 || !matches[0].ListingOnly || matches[0].providerContentBound || matches[0].providerStreamURL != "" || matches[0].DirectStreamURL != "" {
			t.Fatal("discovery invented release year, media binding or playback")
		}
		encoded, _ := json.Marshal(matches)
		if strings.Contains(string(encoded), fawesomeDiscoveryGuestFixture) || strings.Contains(string(encoded), "12345") || strings.Contains(string(encoded), "auth-token") {
			t.Fatal("guest/public configuration escaped into candidate output")
		}
	}
}

func TestFawesomeDiscoveryRejectsEpisodesShortsMismatchesAndUntrustedRoutes(t *testing.T) {
	base := fawesomeDiscoveryFixtureItem()
	for name, mutate := range map[string]func(*fawesomeDiscoveryItem){
		"wrong node":      func(i *fawesomeDiscoveryItem) { i.NodeID++ },
		"short":           func(i *fawesomeDiscoveryItem) { i.Runtime = 90 },
		"zero runtime":    func(i *fawesomeDiscoveryItem) { i.Runtime = 0 },
		"series":          func(i *fawesomeDiscoveryItem) { i.SeriesName = "A show" },
		"episode title":   func(i *fawesomeDiscoveryItem) { i.Title = "S01 E02 Beyond Known Examples" },
		"episode route":   func(i *fawesomeDiscoveryItem) { i.WebURL = "tv-shows/10876543/beyond-known-examples" },
		"nonmovie":        func(i *fawesomeDiscoveryItem) { i.ItemType = "show" },
		"nonnative":       func(i *fawesomeDiscoveryItem) { i.FeedType = "iframe" },
		"country blocked": func(i *fawesomeDiscoveryItem) { i.BlockItem = "true" },
		"SSRF":            func(i *fawesomeDiscoveryItem) { i.WebURL = "https://127.0.0.1/movie.mp4" },
		"trailer":         func(i *fawesomeDiscoveryItem) { i.Title = "Beyond Known Examples Trailer" },
		"unrelated":       func(i *fawesomeDiscoveryItem) { i.Title = "Entirely Unrelated" },
	} {
		t.Run(name, func(t *testing.T) {
			item := base
			mutate(&item)
			matches, err := fawesomeDiscoveryCandidates([]byte(fawesomeDiscoveryFixturePayload(item)), "Beyond Known Examples")
			if err != nil || len(matches) != 0 {
				t.Fatalf("unrelated/unsupported movie entered results: %+v err=%v", matches, err)
			}
		})
	}
	data := strings.TrimSuffix(fawesomeDiscoveryFixturePayload(base), "}") + `,"date":"2026-01-01","release_date":"2025-01-01","updated":"2026"}`
	matches, err := fawesomeDiscoveryCandidates([]byte(data), "Beyond Known Examples")
	if err != nil || len(matches) != 1 || matches[0].ReleaseYear != 0 {
		t.Fatal("catalog dates were promoted to original release year")
	}
	for _, bad := range []string{`Security Error`, `{"status":"fail","type":"error","results":[]}`} {
		if _, err := fawesomeDiscoveryCandidates([]byte(bad), ""); err == nil {
			t.Fatal("changed catalog contract was accepted")
		}
	}
	stringsRuntime := strings.Replace(fawesomeDiscoveryFixturePayload(base), `"runtime":5400`, `"runtime":"5400"`, 1)
	matches, err = fawesomeDiscoveryCandidates([]byte(stringsRuntime), "Beyond Known Examples")
	if err != nil || len(matches) != 1 || matches[0].Duration != "5400" {
		t.Fatal("observed numeric-string runtime discarded a full feature")
	}
	badItem := strings.Replace(fawesomeDiscoveryFixturePayload(base), `"runtime":5400`, `"runtime":"not-a-runtime"`, 1)
	matches, err = fawesomeDiscoveryCandidates([]byte(badItem), "")
	if err != nil || len(matches) != 0 {
		t.Fatal("invalid individual runtime was accepted or broke the whole catalog")
	}
}

func TestFawesomeGuestRejectsExternalBundleChangedConfigurationAndRedirects(t *testing.T) {
	page, _ := url.Parse("https://fawesome.tv/")
	for _, body := range []string{`<script src="https://evil.example/home/new/v987/assets/js/iptv-main.js"></script>`, `<script src="/home/new/v987/assets/js/iptv-main.js?auth-token=anything"></script>`, `<script src="/home/new/v987/assets/js/iptv-main.js"></script><script src="/home/new/v987/assets/js/iptv-main.js"></script>`} {
		if _, ok := fawesomeCurrentBundle([]byte(body), page); ok {
			t.Fatal("untrusted/ambiguous configuration script accepted")
		}
	}
	bundle, _ := url.Parse("https://fawesome.tv/home/new/v987/assets/js/iptv-main.js")
	for _, text := range []string{strings.Replace(fawesomeDiscoveryBundleFixture, `buildVersion:"v987"`, `buildVersion:"v988"`, 1), fawesomeDiscoveryBundleFixture + `appConfig.appId=77,`} {
		if _, ok := parseFawesomePublicConfig([]byte(text), bundle); ok {
			t.Fatal("changed/ambiguous public configuration accepted")
		}
	}
	var calls atomic.Int32
	guest := &fawesomeGuestClient{config: fawesomePublicConfig{apiBase: "https://fawesome.tv/home/new/v987/api/", appID: "9", siteID: "1", platformID: "12345"}, token: fawesomeDiscoveryGuestFixture, client: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		r := searchFixtureResponse(302, "text/plain", "")
		r.Header.Set("Location", "https://evil.example/collect")
		r.Request = req
		return r, nil
	})}}
	_, err := guest.fetch(context.Background(), "recipes.php", nil)
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), fawesomeDiscoveryGuestFixture) || strings.Contains(err.Error(), "12345") {
		t.Fatal("redirect leaked guest credentials or error diagnostics")
	}
	if _, err := guest.fetch(context.Background(), "admin.php", nil); err == nil || calls.Load() != 1 {
		t.Fatal("unsupported endpoint was requested")
	}
}

func TestFawesomeDynamicDiscoveryFlowsThroughGlobalSearchAndNativeProof(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	var tavily, samples, guestCalls atomic.Int32
	item := fawesomeDiscoveryFixtureItem()
	guestTransport := fawesomeDiscoveryFixtureTransport(t, &guestCalls, item)
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
	for _, private := range []string{fawesomeDiscoveryGuestFixture, "private-main-fixture", "auth-token=12345"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("guest/private media escaped global search JSON")
		}
	}
}

func TestLiveFawesomeDynamicPublicSearch(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FAWESOME_DISCOVERY") != "1" {
		t.Skip("set ITEMBA_LIVE_FAWESOME_DISCOVERY=1 for bounded current public guest search")
	}
	s := &Scout{availabilityHTTPClient: newMovieAvailabilityClient()}
	ctx, cancel := context.WithTimeout(context.Background(), fawesomeDiscoveryDeadline)
	defer cancel()
	query := os.Getenv("ITEMBA_LIVE_FAWESOME_QUERY")
	if query == "" {
		query = "The Shape of Shadows"
	}
	matches, err := s.queryFawesomeMovieSites(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var safe []struct {
		Title    string `json:"title"`
		URL      string `json:"url"`
		Duration string `json:"duration"`
		Listing  bool   `json:"listingOnly"`
	}
	for _, m := range matches {
		safe = append(safe, struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Duration string `json:"duration"`
			Listing  bool   `json:"listingOnly"`
		}{m.Title, m.URL, m.Duration, m.ListingOnly})
	}
	encoded, _ := json.Marshal(safe)
	t.Logf("Current public guest discovery (not playback proof): %s", encoded)
	if len(matches) == 0 {
		t.Error("current guest search returned no matching feature")
	}
}
