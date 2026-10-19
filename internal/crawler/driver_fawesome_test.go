package crawler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The bundle fixture and guest flow are the legacy scout provider's own
// test, so the driver is proven against the provider it replaces.

const fawesomeBundleFixture = `appConfig.apiEnv="production",appConfig.appId=9,appConfig.siteId=1,window.platform_id||(appConfig.platform_id=12345),appInfo={buildVersion:"v987"};function getSearchURL(){return "recipes.php"} initSecurityToken="getSecurityToken.php";i.token=appConfig.securityToken;getIptvParameterFromUrl:function(e){var a={iptv:!0,siteId:236,platform_id:12345,buildVer:appInfo.buildVersion};return o[e]?o[e]:a[e]},getCustomParameterFromCustomUrl:function(){};appConfig.siteId=helper.getIptvParameterFromUrl("siteId");if(testParam){appConfig.apiEnv="development"}else if(betaParam){appConfig.apiEnv="beta"}`
const fawesomeGuestToken = "fixture-ephemeral-guest-token"

type fawesomeItem struct {
	NodeID     int64  `json:"node_id"`
	Title      string `json:"title"`
	WebURL     string `json:"web_url"`
	Runtime    any    `json:"runtime"`
	ItemType   string `json:"item_type"`
	FeedType   string `json:"feed_type"`
	ActionType string `json:"actiontype"`
	SeriesName string `json:"series_name"`
	BlockItem  string `json:"blockItem"`
	Desc       string `json:"description"`
	Picture    string `json:"main_picture"`
}

func fawesomeFixtureItem() fawesomeItem {
	return fawesomeItem{NodeID: 10876543, Title: "Beyond Known Examples", WebURL: "movies/10876543/beyond-known-examples", Runtime: 5400, ItemType: "movie", FeedType: "video", ActionType: "video", BlockItem: "false", Desc: "A public guest feature listing.", Picture: "https://img.fawesome.tv/poster.jpg"}
}

func fawesomePayload(items ...fawesomeItem) string {
	data, _ := json.Marshal(map[string]any{"status": "ok", "type": "result", "results": items})
	return string(data)
}

// fawesomeTransport answers the four public guest endpoints and checks the
// headers and identifiers the website's own client would send.
func fawesomeTransport(t *testing.T, calls *atomic.Int32, bundle string, items ...fawesomeItem) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Host != "fawesome.tv" || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Errorf("public discovery left its origin or forwarded account headers: %s", req.URL)
		}
		switch req.URL.Path {
		case "/":
			return htmlResponse(req, `<html><script src="/vendor.js"></script><script src="/home/new/v987/assets/js/iptv-main.js?ver=2345"></script></html>`), nil
		case "/home/new/v987/assets/js/iptv-main.js":
			r := jsonResponse(200, bundle)
			r.Header.Set("Content-Type", "application/javascript")
			r.Request = req
			return r, nil
		case "/home/new/v987/api/getSecurityToken.php":
			q := req.URL.Query()
			if q.Get("siteId") != "236" || q.Get("appId") != "9" || q.Get("auth-token") != "12345" || q.Get("apiEnv") != "production" {
				t.Errorf("guest initialisation did not use the effective public configuration: %s", req.URL.RawQuery)
			}
			if req.Header.Get("token") != "" {
				t.Error("a previous guest token was forwarded to initialisation")
			}
			r := jsonResponse(200, `{"securityToken":"`+fawesomeGuestToken+`"}`)
			r.Request = req
			return r, nil
		case "/home/new/v987/api/recipes.php":
			q := req.URL.Query()
			if q.Get("siteId") != "236" || q.Get("apiEnv") != "production" || q.Get("searchType") != "search" || q.Get("platform_id") != "12345" || q.Get("keys") != "beyond known examples" {
				t.Errorf("search did not use the runtime guest configuration: %s", req.URL.RawQuery)
			}
			if req.Header.Get("token") != fawesomeGuestToken || req.Header.Get("X-Requested-With") != "XMLHttpRequest" || req.Header.Get("Referer") != "https://fawesome.tv/" {
				t.Error("normal guest headers were missing")
			}
			if q.Get("country") != "" || q.Get("countryCode") != "" || q.Get("ip_address") != "" {
				t.Error("public movie discovery replaced the actual location")
			}
			r := jsonResponse(200, fawesomePayload(items...))
			r.Request = req
			return r, nil
		}
		t.Errorf("unobserved public guest endpoint: %s", req.URL.Path)
		r := jsonResponse(404, `{}`)
		r.Request = req
		return r, nil
	}
}

func freshFawesome(t *testing.T) *Driver {
	t.Helper()
	d, err := LoadFile(filepath.Join("..", "..", "drivers", "fawesome.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFawesomeDriverLoadsPublicConfigOnceAndKeepsTheGuestTokenRequestScoped(t *testing.T) {
	d := freshFawesome(t)
	var calls atomic.Int32
	fetcher := NewFetcher(fawesomeTransport(t, &calls, fawesomeBundleFixture, fawesomeFixtureItem()))
	for pass, want := range []int32{4, 6} {
		listings, err := d.Search(context.Background(), fetcher, "The Beyond Known Examples", "")
		if err != nil || len(listings) != 1 || calls.Load() != want {
			t.Fatalf("pass %d: listings=%d calls=%d err=%v", pass, len(listings), calls.Load(), err)
		}
		got := listings[0]
		if got.ID != "fawesome-10876543" || got.URL != "https://fawesome.tv/movies/10876543/beyond-known-examples" || got.Title != "Beyond Known Examples" || got.Duration != "5400" {
			t.Fatalf("identity: %+v", got)
		}
		if got.AccessType != "free" || got.AccessBasis != "provider_model" || got.AccessEvidenceURL != "https://fawesome.tv/" || !got.ListingOnly || got.MediaURL != "" || got.Year != 0 || got.SourceType != "Provider movie listing" || got.Thumbnail != "https://img.fawesome.tv/poster.jpg" {
			t.Fatalf("access and provider fields: %+v", got)
		}
		encoded, _ := json.Marshal(listings)
		if strings.Contains(string(encoded), fawesomeGuestToken) || strings.Contains(string(encoded), "12345") || strings.Contains(string(encoded), "auth-token") {
			t.Fatal("guest or public configuration escaped into listing output")
		}
	}
}

func TestFawesomeDriverRejectsEpisodesShortsMismatchesAndUntrustedRoutes(t *testing.T) {
	base := fawesomeFixtureItem()
	for name, mutate := range map[string]func(*fawesomeItem){
		"wrong node":      func(i *fawesomeItem) { i.NodeID++ },
		"short":           func(i *fawesomeItem) { i.Runtime = 90 },
		"zero runtime":    func(i *fawesomeItem) { i.Runtime = 0 },
		"textual runtime": func(i *fawesomeItem) { i.Runtime = "soon" },
		"series":          func(i *fawesomeItem) { i.SeriesName = "A show" },
		"episode title":   func(i *fawesomeItem) { i.Title = "S01 E02 Beyond Known Examples" },
		"episode route":   func(i *fawesomeItem) { i.WebURL = "tv-shows/10876543/beyond-known-examples" },
		"nonmovie":        func(i *fawesomeItem) { i.ItemType = "show" },
		"nonnative":       func(i *fawesomeItem) { i.FeedType = "iframe" },
		"country blocked": func(i *fawesomeItem) { i.BlockItem = "true" },
		"SSRF":            func(i *fawesomeItem) { i.WebURL = "https://127.0.0.1/movie.mp4" },
		"markup in title": func(i *fawesomeItem) { i.Title = "Beyond <b>Known</b> Examples" },
		"overlong title":  func(i *fawesomeItem) { i.Title = strings.Repeat("x", 241) },
	} {
		t.Run(name, func(t *testing.T) {
			item := base
			mutate(&item)
			d := freshFawesome(t)
			var calls atomic.Int32
			listings, err := d.Search(context.Background(), NewFetcher(fawesomeTransport(t, &calls, fawesomeBundleFixture, item)), "Beyond Known Examples", "")
			if err != nil || len(listings) != 0 {
				t.Fatalf("unsupported movie entered listings: %+v err=%v", listings, err)
			}
		})
	}
	// An absent runtime and a non-https poster are not reasons to drop a film.
	item := base
	item.Runtime = nil
	item.Picture = "http://img.fawesome.tv/poster.jpg"
	var calls atomic.Int32
	listings, err := freshFawesome(t).Search(context.Background(), NewFetcher(fawesomeTransport(t, &calls, fawesomeBundleFixture, item)), "Beyond Known Examples", "")
	if err != nil || len(listings) != 1 || listings[0].Duration != "" || listings[0].Thumbnail != "" {
		t.Fatalf("optional fields: %+v %v", listings, err)
	}
}

func TestFawesomeDriverRefusesChangedConfigurationAmbiguousBundlesAndRedirects(t *testing.T) {
	for name, bundle := range map[string]string{
		"build mismatch":   strings.Replace(fawesomeBundleFixture, `buildVersion:"v987"`, `buildVersion:"v988"`, 1),
		"ambiguous app id": fawesomeBundleFixture + `appConfig.appId=77,`,
		"missing helper":   strings.Replace(fawesomeBundleFixture, `appConfig.siteId=helper.getIptvParameterFromUrl("siteId")`, ``, 1),
		"platform drift":   strings.Replace(fawesomeBundleFixture, `platform_id:12345,buildVer`, `platform_id:99999,buildVer`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			listings, err := freshFawesome(t).Search(context.Background(), NewFetcher(fawesomeTransport(t, &calls, bundle, fawesomeFixtureItem())), "Beyond Known Examples", "")
			if err == nil || len(listings) != 0 || calls.Load() != 2 {
				t.Fatalf("changed configuration must stop before any guest request: calls=%d %+v %v", calls.Load(), listings, err)
			}
			if strings.Contains(err.Error(), fawesomeGuestToken) || strings.Contains(err.Error(), "12345") {
				t.Fatalf("diagnostics leaked configuration: %v", err)
			}
		})
	}

	for name, shell := range map[string]string{
		"external bundle":  `<script src="https://evil.example/home/new/v987/assets/js/iptv-main.js"></script>`,
		"token in query":   `<script src="/home/new/v987/assets/js/iptv-main.js?auth-token=anything"></script>`,
		"ambiguous bundle": `<script src="/home/new/v987/assets/js/iptv-main.js"></script><script src="/home/new/v988/assets/js/iptv-main.js"></script>`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return htmlResponse(req, shell), nil
			}))
			if listings, err := freshFawesome(t).Search(context.Background(), fetcher, "x", ""); err == nil || len(listings) != 0 || calls.Load() != 1 {
				t.Fatalf("untrusted or ambiguous configuration script accepted: calls=%d %+v %v", calls.Load(), listings, err)
			}
		})
	}

	var calls atomic.Int32
	redirecting := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/home/new/v987/api/getSecurityToken.php" {
			calls.Add(1)
			r := jsonResponse(302, "")
			r.Header.Set("Location", "https://evil.example/collect")
			r.Request = req
			return r, nil
		}
		return fawesomeTransport(t, &calls, fawesomeBundleFixture)(req)
	}
	listings, err := freshFawesome(t).Search(context.Background(), NewFetcher(roundTripFunc(redirecting)), "x", "")
	if err == nil || len(listings) != 0 || calls.Load() != 3 {
		t.Fatalf("a redirecting guest endpoint must fail without being followed: calls=%d %+v %v", calls.Load(), listings, err)
	}
}

func TestStepCaptureRulesAndCache(t *testing.T) {
	body := "name: a-b\nprovider: P\nhosts: [h.example]\nsearch:\n  url: https://h.example/s/{v}\n  items: r\n  steps:\n    - url: https://h.example/c\n      response: text\n      cache_seconds: 60\n      capture:\n        v: { regex: 'v=([0-9]+)', unique: true }\n        w: { regex: 'w=([0-9]+)', default: '7' }\n  fields: {title: {path: t}, url: {path: u}}\n"
	d, err := loadDriverText(t, body)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	configBody := "v=1"
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Path == "/c" {
			r := jsonResponse(200, configBody)
			r.Request = req
			return r, nil
		}
		if req.URL.Path != "/s/1" {
			t.Errorf("captured variable not applied to the search url: %s", req.URL)
		}
		r := jsonResponse(200, `{"r":[{"t":"T","u":"https://h.example/x"}]}`)
		r.Request = req
		return r, nil
	}))
	if listings, err := d.Search(context.Background(), fetcher, "q", ""); err != nil || len(listings) != 1 || calls.Load() != 2 {
		t.Fatalf("first search: %+v %v calls=%d", listings, err, calls.Load())
	}
	if _, err := d.Search(context.Background(), fetcher, "q", ""); err != nil || calls.Load() != 3 {
		t.Fatalf("a cached step is not refetched: calls=%d %v", calls.Load(), err)
	}
	d.ClearStepCache()
	configBody = "v=1 v=2"
	if _, err := d.Search(context.Background(), fetcher, "q", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("an ambiguous unique capture must fail: %v", err)
	}
	configBody = "nothing"
	if _, err := d.Search(context.Background(), fetcher, "q", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a missing capture without a default must fail: %v", err)
	}
}

func loadDriverText(t *testing.T, body string) (*Driver, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadFile(path)
}
