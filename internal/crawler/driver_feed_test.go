package crawler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// The shelf feeds are held to the legacy scout catalog client's own tests:
// the popular contract, the one New Arrivals row that may be followed, the
// catalog controls that row may carry, and what a shelf may not invent.

type fawesomeFeedItem struct {
	fawesomeItem
	Date string `json:"date,omitempty"`
}

const fawesomeNewArrivalsRows = `{"status":"ok","channels":[{"name":"New Arrivals TV","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=111"},{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=mixedcontent&keys=1234&order=2&rowType=mixedcontent&dltype=0&gridstyle=flat-movie&highlight=0&akp=1-1234&userType=new&version=sv9.0&sort_type=&appId=9&siteId=1&auth-token=fixture&deviceId=&session-id="}]}`

// fawesomeFeedTransport answers the guest endpoints the feeds use on top of
// the search transport: the home rows and recipes.php for a feed, while a
// typed search is still answered by the search transport.
func fawesomeFeedTransport(t *testing.T, calls *atomic.Int32, rows string, feedChecks func(*testing.T, url.Values), items ...fawesomeFeedItem) roundTripFunc {
	t.Helper()
	search := fawesomeTransport(t, calls, fawesomeBundleFixture, fawesomeFixtureItem())
	return func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		switch {
		case req.URL.Path == "/home/new/v987/api/sub-categories.php":
			calls.Add(1)
			if q.Get("parent") != "home" || q.Get("siteId") != "236" || q.Get("auth-token") != "12345" || req.Header.Get("token") != fawesomeGuestToken {
				t.Errorf("home rows were not requested as the site does: %s", req.URL.RawQuery)
			}
			r := jsonResponse(200, rows)
			r.Request = req
			return r, nil
		case req.URL.Path == "/home/new/v987/api/recipes.php" && q.Get("searchType") != "search":
			calls.Add(1)
			if req.Header.Get("token") != fawesomeGuestToken || q.Get("siteId") != "236" || q.Get("appId") != "9" || q.Get("auth-token") != "12345" || q.Get("apiEnv") != "production" {
				t.Errorf("the feed did not carry the fresh guest configuration: %s", req.URL.RawQuery)
			}
			if q.Get("platform_id") != "" {
				t.Error("platform_id belongs to the typed search only")
			}
			if feedChecks != nil {
				feedChecks(t, q)
			}
			data, _ := json.Marshal(map[string]any{"status": "ok", "type": "result", "results": items})
			r := jsonResponse(200, string(data))
			r.Request = req
			return r, nil
		}
		return search(req)
	}
}

func TestFawesomePopularFeedUsesTheGuestContractAndReadsCatalogDates(t *testing.T) {
	d := freshFawesome(t)
	var calls atomic.Int32
	dated := fawesomeFeedItem{fawesomeItem: fawesomeFixtureItem(), Date: "Oct. 02, 2026"}
	undated := dated
	undated.NodeID, undated.WebURL, undated.Runtime, undated.Date = 10876544, "movies/10876544/unknown-runtime", nil, "Oct. 03, 2026"
	odd := dated
	odd.NodeID, odd.WebURL, odd.Date = 10876545, "movies/10876545/odd-date", "yesterday"
	fetcher := NewFetcher(fawesomeFeedTransport(t, &calls, fawesomeNewArrivalsRows, func(t *testing.T, q url.Values) {
		if q.Get("searchType") != "popular" || len(q) != 5 {
			t.Errorf("popular feed parameters: %s", q.Encode())
		}
	}, dated, undated, odd, dated))
	listings, err := d.Feed(context.Background(), fetcher, "popular")
	if err != nil || len(listings) != 2 || calls.Load() != 4 {
		t.Fatalf("cold popular feed: %d listings calls=%d err=%v", len(listings), calls.Load(), err)
	}
	got := listings[0]
	if got.ID != "fawesome-10876543" || got.URL != "https://fawesome.tv/movies/10876543/beyond-known-examples" || got.CatalogDate != "2026-10-02" || got.Duration != "5400" || got.Year != 0 || !got.ListingOnly || got.AccessType != "free" {
		t.Fatalf("dated listing: %+v", got)
	}
	// A film without a known runtime is not a shelf item; an unreadable date
	// is dropped, not guessed, and the film stays.
	if listings[1].ID != "fawesome-10876545" || listings[1].CatalogDate != "" {
		t.Fatalf("odd-date listing: %+v", listings[1])
	}
	encoded, _ := json.Marshal(listings)
	if strings.Contains(string(encoded), fawesomeGuestToken) || strings.Contains(string(encoded), "12345") {
		t.Fatal("guest or public configuration escaped into listing output")
	}
	// A second feed reuses the public configuration and takes a fresh token;
	// so does a typed search after it.
	if _, err := d.Feed(context.Background(), fetcher, "popular"); err != nil || calls.Load() != 6 {
		t.Fatalf("warm popular feed: calls=%d err=%v", calls.Load(), err)
	}
	if listings, err := d.Search(context.Background(), fetcher, "Beyond Known Examples", ""); err != nil || len(listings) != 1 || calls.Load() != 8 {
		t.Fatalf("search after feeds: %d listings calls=%d err=%v", len(listings), calls.Load(), err)
	}
	if _, err := d.Feed(context.Background(), fetcher, "nightly"); err == nil {
		t.Fatal("an undeclared feed must be refused")
	}
}

func TestFawesomeRecentFeedFollowsOnlyTheReturnedNewArrivalsRow(t *testing.T) {
	d := freshFawesome(t)
	var calls atomic.Int32
	fetcher := NewFetcher(fawesomeFeedTransport(t, &calls, fawesomeNewArrivalsRows, func(t *testing.T, q url.Values) {
		if q.Get("searchType") != "mixedcontent" || q.Get("keys") != "1234" || q.Get("order") != "2" || q.Get("rowType") != "mixedcontent" || q.Get("dltype") != "0" || q.Get("gridstyle") != "flat-movie" || q.Get("akp") != "1-1234" || q.Get("version") != "sv9.0" || q.Get("userType") != "new" || q.Get("highlight") != "0" {
			t.Errorf("the observed New Arrivals feed was changed: %s", q.Encode())
		}
		for _, key := range []string{"deviceId", "session-id", "deviceModel"} {
			if _, found := q[key]; found {
				t.Errorf("feed-supplied client identity %s was forwarded", key)
			}
		}
		if q.Get("auth-token") == "fixture" || q.Get("siteId") == "1" {
			t.Error("the row's identifiers were not replaced by the fresh configuration")
		}
	}, fawesomeFeedItem{fawesomeItem: fawesomeFixtureItem()}))
	listings, err := d.Feed(context.Background(), fetcher, "recent")
	if err != nil || len(listings) != 1 || calls.Load() != 5 {
		t.Fatalf("cold recent feed: %d listings calls=%d err=%v", len(listings), calls.Load(), err)
	}
	if _, err := d.Feed(context.Background(), fetcher, "recent"); err != nil || calls.Load() != 8 {
		t.Fatalf("warm recent feed must take a token, the rows and the feed: calls=%d err=%v", calls.Load(), err)
	}

	refuse := func(t *testing.T, name, rows string) {
		t.Helper()
		d := freshFawesome(t)
		var calls atomic.Int32
		fetcher := NewFetcher(fawesomeFeedTransport(t, &calls, rows, func(t *testing.T, _ url.Values) {
			t.Errorf("%s: an unidentified, off-site, ambiguous or altered feed was requested", name)
		}))
		listings, err := d.Feed(context.Background(), fetcher, "recent")
		if err == nil || len(listings) != 0 {
			t.Fatalf("%s: accepted: %+v", name, listings)
		}
		if strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "1234") {
			t.Fatalf("%s: diagnostics leaked a value: %v", name, err)
		}
	}
	observed := "https://rapi.ifood.tv/recipes.php?searchType=mixedcontent"
	for name, rows := range map[string]string{
		"off-site feed":       strings.Replace(fawesomeNewArrivalsRows, observed, "https://unknown.example/recipes.php?searchType=mixedcontent", 1),
		"private endpoint":    strings.Replace(fawesomeNewArrivalsRows, "rapi.ifood.tv/recipes.php?searchType=mixedcontent", "rapi.ifood.tv/private.php?searchType=mixedcontent", 1),
		"renamed row":         strings.Replace(fawesomeNewArrivalsRows, `"New Arrivals"`, `"Latest Releases"`, 1),
		"failed status":       strings.Replace(fawesomeNewArrivalsRows, `"status":"ok"`, `"status":"fail"`, 1),
		"ambiguous rows":      `{"status":"ok","channels":[{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=1"},{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=2"}]}`,
		"unobserved row type": strings.Replace(fawesomeNewArrivalsRows, "rowType=mixedcontent", "rowType=private", 1),
		"unobserved dltype":   strings.Replace(fawesomeNewArrivalsRows, "dltype=0", "dltype=9", 1),
		"unobserved search":   strings.Replace(fawesomeNewArrivalsRows, "searchType=mixedcontent", "searchType=nodeid", 1),
	} {
		refuse(t, name, rows)
	}
	for _, suffix := range []string{"&country=US", "&session-id=someone", "&deviceId=someone", "&redirect=https%3A%2F%2Fexample.com", "&keys=2", "&platform_id=1&platform_id=2"} {
		refuse(t, suffix, `{"status":"ok","channels":[{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=1`+suffix+`"}]}`)
	}
	// The row the site names, in its own spacing and case, with only the
	// observed controls, is followed.
	d = freshFawesome(t)
	calls.Store(0)
	fetcher = NewFetcher(fawesomeFeedTransport(t, &calls, `{"status":"ok","channels":[{"name":" new  arrivals ","url":"https://fawesome.tv/home/new/v987/api/recipes.php?searchType=new-qid&keys=222&sort_type=&order=0"}]}`, func(t *testing.T, q url.Values) {
		if q.Get("keys") != "222" || q.Get("searchType") != "new-qid" || q.Get("order") != "0" || q.Get("sort_type") != "" {
			t.Errorf("provider's named movie feed was not followed exactly: %s", q.Encode())
		}
	}, fawesomeFeedItem{fawesomeItem: fawesomeFixtureItem()}))
	if listings, err := d.Feed(context.Background(), fetcher, "recent"); err != nil || len(listings) != 1 {
		t.Fatalf("same-origin new-qid row: %+v %v", listings, err)
	}
}

func TestLookupFiltersCaptureListsDatesAndLiteralBraces(t *testing.T) {
	doc := map[string]any{
		"channels": []any{
			map[string]any{"name": " new  ARRIVALS ", "url": "u1"},
			map[string]any{"name": "Other", "url": "u2"},
			map[string]any{"name": "New Arrivals", "url": "u3"},
			"not an object",
		},
		"n": []any{1.0, 2.0},
	}
	if got, ok := lookup(doc, "channels[name=New Arrivals].url").([]any); !ok || len(got) != 2 || got[0] != "u1" || got[1] != "u3" {
		t.Fatalf("filtered lookup: %#v", lookup(doc, "channels[name=New Arrivals].url"))
	}
	if lookup(doc, "channels[name=Nothing].url") != nil || lookup(doc, "channels[name=Other].missing") != nil || lookup(doc, "channels[1].url") != "u2" || lookup(doc, "n[name=x]") != nil {
		t.Fatal("filters must return nothing for no match, a missing field, or a list of scalars")
	}
	if got := valueCandidates(lookup(doc, "n"), false); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("list candidates: %v", got)
	}
	if got := valueCandidates(lookup(doc, "channels[name=Other]"), true); len(got) != 1 || !strings.Contains(got[0], `"url":"u2"`) {
		t.Fatalf("json candidates: %v", got)
	}
	if got := valueCandidates(lookup(doc, "channels[name=Other]"), false); len(got) != 0 {
		t.Fatalf("an object is not a scalar candidate: %v", got)
	}
	if transform("date:Jan. 2, 2006", "Oct. 02, 2026") != "2026-10-02" || transform("date:Jan. 2, 2006", "Oct. 02, 1700") != "" || transform("date:Jan. 2, 2006", "yesterday") != "" || transform("date", "2026-10-02T00:00:00Z") != "2026-10-02" {
		t.Fatal("date transform")
	}
	if got := expand("a{2,5}b{x}c{24}", func(string) string { return "V" }); got != "a{2,5}bVc{24}" {
		t.Fatalf("regular expression braces must stay literal in templates: %q", got)
	}
}

func TestFetchOptionsBoundRedirectsAndBodies(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		var r *http.Response
		switch req.URL.String() {
		case "https://h.example/private":
			r = jsonResponse(302, "")
			r.Header.Set("Location", "http://127.0.0.1/x")
		case "https://h.example/plain":
			r = jsonResponse(302, "")
			r.Header.Set("Location", "http://h.example/other")
		case "https://h.example/moved":
			r = jsonResponse(302, "")
			r.Header.Set("Location", "https://h.example/other")
		case "https://h.example/away":
			r = jsonResponse(302, "")
			r.Header.Set("Location", "https://elsewhere.example/moved")
		case "https://h.example/big":
			r = jsonResponse(200, strings.Repeat("x", 1025))
		default:
			r = jsonResponse(200, `{}`)
		}
		r.Request = req
		return r, nil
	})
	f := NewFetcher(transport)
	f.DisableBackoff()
	ctx := context.Background()
	for _, target := range []string{"https://h.example/private", "https://h.example/plain"} {
		calls.Store(0)
		if _, err := f.Fetch(ctx, target, nil, FetchOptions{MaxRedirects: 5}); err == nil || calls.Load() != 1 {
			t.Fatalf("%s: a private or unencrypted redirect must be refused unfetched: calls=%d err=%v", target, calls.Load(), err)
		}
	}
	calls.Store(0)
	if _, err := f.Fetch(ctx, "https://h.example/moved", nil, FetchOptions{SamePath: true, Hosts: []string{"h.example"}}); err == nil || calls.Load() != 1 {
		t.Fatalf("a redirect to another page must be refused under same_path: calls=%d err=%v", calls.Load(), err)
	}
	if _, err := f.Fetch(ctx, "https://h.example/away", nil, FetchOptions{SamePath: true, Hosts: []string{"h.example"}}); err == nil {
		t.Fatal("a redirect to another host must be refused under hosts")
	}
	if resp, err := f.Fetch(ctx, "https://h.example/moved", nil, FetchOptions{MaxRedirects: 5}); err != nil || resp.FinalURL != "https://h.example/other" {
		t.Fatalf("a public https redirect is followed: %+v %v", resp, err)
	}
	if _, err := f.Fetch(ctx, "https://h.example/big", nil, FetchOptions{MaxBody: 1024}); err == nil {
		t.Fatal("a body over the request's limit must be an error")
	}
	if resp, err := f.Fetch(ctx, "https://h.example/big", nil, FetchOptions{}); err != nil || len(resp.Body) != 1025 {
		t.Fatalf("without a limit the default applies: %v", err)
	}
	for host, public := range map[string]bool{"example.com": true, "localhost": false, "app.localhost": false, "127.0.0.1": false, "10.1.2.3": false, "[::1]": false, "169.254.1.1": false, "8.8.8.8": true, "printer.local": false} {
		if PublicHost(host) != public {
			t.Errorf("PublicHost(%q) = %v", host, !public)
		}
	}
}
