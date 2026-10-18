package crawler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func shippedDrivers(t *testing.T) map[string]*Driver {
	t.Helper()
	drivers, err := LoadDir(filepath.Join("..", "..", "drivers"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Driver{}
	for _, d := range drivers {
		byName[d.Name] = d
	}
	return byName
}

func TestShippedDriversLoadAndValidate(t *testing.T) {
	drivers := shippedDrivers(t)
	for _, name := range []string{"apple-movies", "internet-archive"} {
		d, ok := drivers[name]
		if !ok {
			t.Fatalf("driver %s is missing", name)
		}
		if d.Provider == "" || d.Spec == nil || d.Rate.PerMinute <= 0 || d.Rate.Concurrency <= 0 || d.Spec.Limit <= 0 {
			t.Fatalf("driver %s did not validate with defaults: %+v", name, d)
		}
	}
	if got := drivers["apple-movies"].Markets(); len(got) != 8 || got[0] != "US" {
		t.Fatalf("apple markets: %v", got)
	}
	if got := drivers["internet-archive"].Markets(); len(got) != 1 || got[0] != "" {
		t.Fatalf("archive has no storefront markets: %v", got)
	}
	if drivers["apple-movies"].Host() != "itunes.apple.com" || drivers["internet-archive"].Host() != "archive.org" {
		t.Fatal("search hosts")
	}
}

// The fixture and expectations are the legacy scout provider's own test, so
// the driver is proven to reproduce the provider it replaces.
func TestAppleDriverReproducesTheLegacyListing(t *testing.T) {
	d := shippedDrivers(t)["apple-movies"]
	var requests atomic.Int32
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		q := req.URL.Query()
		if req.URL.Host != "itunes.apple.com" || req.URL.Path != "/search" || q.Get("term") != "Interstellar" || q.Get("country") != "ZA" || q.Get("entity") != "movie" || q.Get("limit") != "20" {
			t.Errorf("unexpected store query: %s", req.URL)
		}
		return jsonResponse(200, `{"results":[
			{"kind":"feature-movie","trackId":123,"trackName":"Interstellar","trackViewUrl":"https://itunes.apple.com/za/movie/interstellar/id123","previewUrl":"https://cdn.example/trailer.m4v","releaseDate":"2014-11-07T08:00:00Z","trackTimeMillis":10140000,"currency":"ZAR","trackPrice":99.99,"trackRentalPrice":29.99,"shortDescription":"A team of explorers."},
			{"kind":"song","trackName":"Interstellar","trackViewUrl":"https://itunes.apple.com/za/movie/song/id456"},
			{"kind":"feature-movie","trackName":"Interstellar","trackViewUrl":"javascript:alert(1)"},
			{"kind":"feature-movie","trackName":"Interstellar","trackViewUrl":"https://evil.example/movie/id789"},
			{"kind":"feature-movie","trackId":123,"trackName":"Interstellar","trackViewUrl":"https://itunes.apple.com/za/movie/interstellar/id123","currency":"ZAR","trackPrice":99.99}
		]}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Interstellar", "ZA")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || len(listings) != 1 {
		t.Fatalf("got %d listings from %d requests: %+v", len(listings), requests.Load(), listings)
	}
	got := listings[0]
	if got.URL != "https://itunes.apple.com/za/movie/interstellar/id123" || got.Title != "Interstellar" || got.Year != 2014 || got.Market != "ZA" {
		t.Fatalf("identity fields: %+v", got)
	}
	if got.Price != "Rent ZAR 29.99 · Buy ZAR 99.99" || got.AccessType != "paid" || got.Duration != "2h 49m" {
		t.Fatalf("offer fields: %+v", got)
	}
	if got.Provider != "Apple TV / iTunes" || got.SourceType != "Movie Store" || got.Description != "A team of explorers." || got.MediaURL != "" {
		t.Fatalf("provider fields: %+v", got)
	}
}

func TestAppleDriverRetriesWithoutEntityWhenTheStoreReturnsNothing(t *testing.T) {
	d := shippedDrivers(t)["apple-movies"]
	var seen []string
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.Query().Encode())
		if req.URL.Query().Get("entity") == "movie" {
			return jsonResponse(200, `{"results":[]}`), nil
		}
		return jsonResponse(200, `{"results":[{"kind":"song","trackName":"Film"},{"kind":"feature-movie","trackId":1,"trackName":"Film","trackViewUrl":"https://itunes.apple.com/us/movie/film/id1"}]}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Film", "US")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !strings.Contains(seen[0], "entity=movie") || strings.Contains(seen[1], "entity=") {
		t.Fatalf("expected one retry without entity, saw %v", seen)
	}
	if len(listings) != 1 || listings[0].AccessType != "unknown" || listings[0].Price != "" || listings[0].Year != 0 {
		t.Fatalf("a listing without prices is unknown access: %+v", listings)
	}
}

func TestArchiveDriverBuildsTheDocumentedQueryAndReadsDocs(t *testing.T) {
	d := shippedDrivers(t)["internet-archive"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "archive.org" || req.URL.Path != "/advancedsearch.php" || q.Get("q") != `title:("Fixture Film") AND mediatype:movies` || q.Get("fl[]") != "identifier,title,year,collection,runtime" || q.Get("rows") != "30" || q.Get("output") != "json" || q.Get("sort[]") != "downloads desc" {
			t.Errorf("unexpected archive query: %s", req.URL)
		}
		return jsonResponse(200, `{"response":{"docs":[
			{"identifier":"fixture-film","title":"Fixture Film (2014)","runtime":"1:40:00"},
			{"identifier":"fixture film with spaces","title":"Fixture Film"},
			{"identifier":"fixture-film-2","title":["Fixture Film"],"year":["2014"],"collection":["feature_films"]},
			{"identifier":"fixture-film-3","title":""}
		]}}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, `Fixture \"Film"`, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 2 {
		t.Fatalf("expected the two valid docs, got %+v", listings)
	}
	first := listings[0]
	if first.URL != "https://archive.org/details/fixture-film" || first.Title != "Fixture Film (2014)" || first.Duration != "1:40:00" || first.Year != 0 {
		t.Fatalf("first doc: %+v", first)
	}
	if first.Thumbnail != "https://archive.org/services/img/fixture-film" || first.Provider != "Internet Archive" || first.SourceType != "Archive video" || first.AccessType != "unknown" || first.Market != "" {
		t.Fatalf("first doc provider fields: %+v", first)
	}
	if listings[1].URL != "https://archive.org/details/fixture-film-2" || listings[1].Title != "Fixture Film" || listings[1].Year != 2014 {
		t.Fatalf("array metadata must read as scalars: %+v", listings[1])
	}
}

func TestDriverErrorsAreExplicit(t *testing.T) {
	d := shippedDrivers(t)["internet-archive"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(503, `{}`), nil
	}))
	if _, err := d.Search(context.Background(), fetcher, "x", ""); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("a failing host must be an error, got %v", err)
	}
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<html>not json"))}, nil
	}))
	if _, err := d.Search(context.Background(), fetcher, "x", ""); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("a non-JSON response must be an error, got %v", err)
	}
}

func TestBrokenDriverFilesAreRefused(t *testing.T) {
	cases := map[string]string{
		"unknown key": "name: a-b\nprovider: P\nhosts: [h.example]\nbogus: 1\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t}, url: {path: u}}}\n",
		"http url":    "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: http://h.example/s, items: r, fields: {title: {path: t}, url: {path: u}}}\n",
		"no title":    "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {url: {path: u}}}\n",
		"bad field":   "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t}, url: {path: u}, rating: {path: r}}}\n",
		"bad xform":   "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t, transform: shout}, url: {path: u}}}\n",
		"bad name":    "name: Apple Movies\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t}, url: {path: u}}}\n",
		"bad retry":   "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, params: {q: '{query}'}, retry_without: [entity], items: r, fields: {title: {path: t}, url: {path: u}}}\n",
		"two sources": "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t, template: x}, url: {path: u}}}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if drivers, err := LoadDir(dir); err == nil {
				t.Fatalf("broken driver was accepted: %+v", drivers[0])
			}
		})
	}
	dir := t.TempDir()
	good := "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {path: t}, url: {path: u}}}\n"
	for _, file := range []string{"one.yaml", "two.yml"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(good), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "both claim the name") {
		t.Fatalf("duplicate names must be refused, got %v", err)
	}
}

func TestTemplatesPathsAndTransforms(t *testing.T) {
	vars := map[string]string{"market": "ZA", "id": "a b/c"}
	resolve := func(name string) string { return vars[name] }
	if got := expand("apple-{market|lower}-{id|path}-{missing}-{open", resolve); got != "apple-za-a%20b%2Fc--{open" {
		t.Fatalf("expand: %q", got)
	}
	if got := expand("q={id|query}", resolve); got != "q="+url.QueryEscape("a b/c") {
		t.Fatalf("query filter: %q", got)
	}
	document := map[string]any{"response": map[string]any{"docs": []any{map[string]any{"title": []any{"One"}, "n": 2.0}}}}
	if scalarString(lookup(document, "response.docs[0].title")) != "One" || scalarString(lookup(document, "response.docs[0].n")) != "2" || lookup(document, "response.docs[1]") != nil || lookup(document, "nope.x") != nil {
		t.Fatal("lookup")
	}
	if transform("year_of_date", "2014-11-07T08:00:00Z") != "2014" || transform("year_of_date", "released 1968 in cinemas") != "1968" || transform("year_in_text", "no year") != "" {
		t.Fatal("year transforms")
	}
	if transform("duration_from_millis", "10140000") != "2h 49m" || transform("duration_from_millis", "0") != "" || transform("duration_from_millis", "x") != "" {
		t.Fatal("duration transform")
	}
}
