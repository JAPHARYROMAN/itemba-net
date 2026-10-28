package crawler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func htmlResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// The fixtures are the legacy scout walkers' own test pages. At the driver
// level every well-formed listing is returned; relevance belongs to the
// search pipeline and is tested in scout.

func TestPublicDomainMoviesDriverReadsCardsPagesAndYears(t *testing.T) {
	d := shippedDrivers(t)["public-domain-movies"]
	var calls atomic.Int32
	first := `<nav><a href="/movie/his-girl-friday">Unrelated navigation</a></nav><div class="view-content"><div class="details"><div class="date"><a href="/year?v=1940">1940</a></div><h1><a href="/movie/his-girl-friday">His Girl Friday</a></h1></div><div class="details"><a href="/year?v=1950">1950</a><h1><a href="/movie/wrong-edition">His Girl Friday</a></h1></div><h1><a href="/movie/his-girl-friday-trailer">His Girl Friday Trailer</a></h1><h1><a href="https://evil.example/movie/his-girl-friday">His Girl Friday</a></h1><h1><a href="/movie/his-girl-friday"><img src="/image"></a></h1></div><a href="/feature_movies?page=1">2</a><a href="/feature_movies?page=2">3</a><a href="/feature_movies?page=3">4</a>`
	later := `<div class="view-content"><div class="details"><a href="/year?v=1933">1933</a><h1><a href="/movie/duck-soup">Duck Soup</a></h1></div></div><a href="/feature_movies?page=9">next</a>`
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Host != "publicdomainmovie.net" || req.URL.Path != "/feature_movies" {
			t.Errorf("unexpected catalog request: %s", req.URL)
		}
		if req.URL.Query().Get("page") == "" {
			return htmlResponse(req, first), nil
		}
		return htmlResponse(req, later), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "His Girl Friday", "")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("the catalog and its two advertised pages are read, not more: %d calls", calls.Load())
	}
	var urls []string
	for _, l := range listings {
		urls = append(urls, l.URL+"|"+l.Title+"|"+strconv.Itoa(l.Year))
		if l.AccessType != "free" || !l.ListingOnly || l.SourceType != "Public Domain" || l.Provider != "Public Domain Movies" {
			t.Fatalf("catalog listing fields: %+v", l)
		}
	}
	want := []string{
		"https://publicdomainmovie.net/movie/his-girl-friday|His Girl Friday|1940",
		"https://publicdomainmovie.net/movie/wrong-edition|His Girl Friday|1950",
		"https://publicdomainmovie.net/movie/his-girl-friday-trailer|His Girl Friday Trailer|0",
		"https://publicdomainmovie.net/movie/duck-soup|Duck Soup|1933",
	}
	if strings.Join(urls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("listings:\n%s\nwant:\n%s", strings.Join(urls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPublicDomainMoviesDriverRefusesASubstitutedPage(t *testing.T) {
	d := shippedDrivers(t)["public-domain-movies"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		moved := req.Clone(req.Context())
		moved.URL, _ = url.Parse("https://publicdomainmovie.net/")
		return htmlResponse(moved, `<div class="view-content"><a href="/movie/his-girl-friday">His Girl Friday</a></div>`), nil
	}))
	if listings, err := d.Search(context.Background(), fetcher, "His Girl Friday", ""); err == nil || len(listings) != 0 || !strings.Contains(err.Error(), "different page") {
		t.Fatalf("a homepage substituted for the catalog must be refused: %+v %v", listings, err)
	}
}

func TestFeatureCatalogDriversUseObservedFormsAndSkipHiddenAndForeignLinks(t *testing.T) {
	drivers := shippedDrivers(t)
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Method != http.MethodGet {
			t.Fatal("public catalog search must not send account state")
		}
		switch req.URL.String() {
		case "https://www.arcoiris.tv/search/s/Night%20of%20the%20Living%20Dead/type/all/":
			return htmlResponse(req, `<h2><a href="/scheda/en/766/">Night of the Living Dead</a></h2><h2><a href="/scheda/en/999/">Night of the Living Dead Trailer</a></h2><div hidden><h2><a href="/scheda/en/123/">Night of the Living Dead</a></h2></div><h2><a href="https://other.example/scheda/en/5/">Elsewhere</a></h2>`), nil
		case "https://publicdomainmovie.org/?s=Night+of+the+Living+Dead":
			return htmlResponse(req, `<h3><a href="/night-of-the-living-dead-1968/">Night of the Living Dead (1968) – Watch free</a></h3><h3><a href="/night-of-the-living-dead-1990/">Night of the Living Dead (1990)</a></h3><h3><a href="https://publicdomainmovie.org.evil.net/night-of-the-living-dead-1968/">Night of the Living Dead (1968)</a></h3><footer><h3><a href="/night-of-the-living-dead-1968-hidden/">Night of the Living Dead (1968)</a></h3></footer><h3><a href="/about/">About</a></h3>`), nil
		}
		t.Fatalf("unexpected discovery URL: %s", req.URL)
		return nil, nil
	}))
	arcoiris, err := drivers["arcoiris-tv"].Search(context.Background(), fetcher, "Night of the Living Dead", "")
	if err != nil || len(arcoiris) != 2 || arcoiris[0].URL != "https://www.arcoiris.tv/scheda/en/766/" || arcoiris[1].URL != "https://www.arcoiris.tv/scheda/en/999/" {
		t.Fatalf("arcoiris listings: %+v %v", arcoiris, err)
	}
	if arcoiris[0].Title != "Night of the Living Dead" || arcoiris[0].AccessType != "unknown" || !arcoiris[0].ListingOnly || arcoiris[0].SourceType != "Feature film catalog" {
		t.Fatalf("arcoiris fields: %+v", arcoiris[0])
	}
	pdm, err := drivers["publicdomainmovie-org"].Search(context.Background(), fetcher, "Night of the Living Dead", "")
	if err != nil || len(pdm) != 2 || pdm[0].URL != "https://publicdomainmovie.org/night-of-the-living-dead-1968/" || pdm[1].URL != "https://publicdomainmovie.org/night-of-the-living-dead-1990/" {
		t.Fatalf("publicdomainmovie.org listings: %+v %v", pdm, err)
	}
	if pdm[0].Title != "Night of the Living Dead (1968) – Watch free" || pdm[0].Provider != "PublicDomainMovie.org" {
		t.Fatalf("publicdomainmovie.org keeps the listed title for the pipeline to read: %+v", pdm[0])
	}
}

func TestArcoirisDriverUsesWordsForSlashesInSearchPaths(t *testing.T) {
	d := shippedDrivers(t)["arcoiris-tv"]
	const query = "Joe Jackson/The Playhouse"
	const wantURL = "https://www.arcoiris.tv/search/s/Joe%20Jackson%20The%20Playhouse/type/all/"
	if got := d.SearchURL(query, ""); got != wantURL {
		t.Fatalf("SearchURL() = %q, want %q", got, wantURL)
	}
	const spacedWant = "https://www.arcoiris.tv/search/s/No%20Way%20Out%20Magnets/type/all/"
	if got := d.SearchURL("No Way Out / Magnets", ""); got != spacedWant {
		t.Fatalf("SearchURL() with surrounding spaces = %q, want %q", got, spacedWant)
	}
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != wantURL {
			t.Fatalf("search URL = %q, want %q", req.URL, wantURL)
		}
		return htmlResponse(req, ""), nil
	}))
	if listings, err := d.Search(context.Background(), fetcher, query, ""); err != nil || len(listings) != 0 {
		t.Fatalf("slash-containing title search: %+v, %v", listings, err)
	}
}

func TestHTMLFieldReadersAndRules(t *testing.T) {
	page, _ := url.Parse("https://h.example/list")
	doc := `<div class="details"><span class="date"><a href="/year?v=1940">1940</a></span><h1><a href="/movie/x">  His   Girl <b>Friday</b> </a></h1><script>ignored()</script></div>`
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) { return htmlResponse(req, doc), nil }))
	d := &Driver{Name: "t-html", Provider: "T", Hosts: []string{"h.example"}, Spec: &SearchSpec{
		URL: "https://h.example/list", Response: "html", Items: "h1 > a",
		Fields: map[string]FieldSpec{
			"title": {Select: "."},
			"url":   {Select: ".", Attr: "href"},
			"year":  {Closest: ".details", Select: "a[href^='/year?v=']", Attr: "href", Transform: "url_query:v", Range: []float64{1880, 2100}, Optional: true},
		},
	}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	listings, err := d.Search(context.Background(), fetcher, "x", "")
	if err != nil || len(listings) != 1 {
		t.Fatalf("%+v %v", listings, err)
	}
	if listings[0].Title != "His Girl Friday" || listings[0].URL != "https://h.example/movie/x" || listings[0].Year != 1940 {
		t.Fatalf("html readers: %+v", listings[0])
	}
	if nodeText(nil) != "" || attribute(nil, "x") != "" {
		t.Fatal("nil nodes read as empty")
	}
	if transform("url_query:v", "not a url at all\x00") != "" || transform("url_query:v", "/year?v=1940") != "1940" || transform("url_query:missing", "/year?v=1940") != "" {
		t.Fatal("url_query transform")
	}
	if expand("{query|strip_article|lower}", func(string) string { return "The Secret Wife" }) != "secret wife" || expand("{q|strip_article}", func(string) string { return "A" }) != "A" {
		t.Fatal("strip_article filter")
	}
	_ = page
}

func TestDriverRulesForHTMLAndJSONAreKeptApart(t *testing.T) {
	cases := map[string]string{
		"html with offers":  "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, response: html, items: a, fields: {title: {select: '.'}, url: {select: '.', attr: href}}, offers: {currency: c, prices: [{label: Buy, paths: [p]}]}}\n",
		"json with pages":   "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, pages: {select: a, max: 1}, fields: {title: {path: t}, url: {path: u}}}\n",
		"html path field":   "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, response: html, items: a, fields: {title: {path: t}, url: {select: '.', attr: href}}}\n",
		"json select field": "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, fields: {title: {select: h1}, url: {path: u}}}\n",
		"bad selector":      "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, response: html, items: 'a[', fields: {title: {select: '.'}, url: {select: '.', attr: href}}}\n",
		"bad relevance":     "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, require_relevance: maybe, fields: {title: {path: t}, url: {path: u}}}\n",
		"step without use":  "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, steps: [{url: https://h.example/c}], fields: {title: {path: t}, url: {path: u}}}\n",
		"capture no source": "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, steps: [{url: https://h.example/c, capture: {x: {unique: true}}}], fields: {title: {path: t}, url: {path: u}}}\n",
		"bad group":         "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, items: r, steps: [{url: https://h.example/c, response: text, capture: {x: {regex: 'a(b)', group: 2}}}], fields: {title: {path: t}, url: {path: u}}}\n",
		"text search":       "name: a-b\nprovider: P\nhosts: [h.example]\nsearch: {url: https://h.example/s, response: text, items: r, fields: {title: {path: t}, url: {path: u}}}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := loadDriverText(t, body)
			if err == nil {
				t.Fatalf("broken driver was accepted: %+v", d)
			}
		})
	}
}
