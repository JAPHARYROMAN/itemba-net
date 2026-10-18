package scout

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicDomainFeaturePageUsesObservedCardsAndPagination(t *testing.T) {
	page, _ := url.Parse("https://publicdomainmovie.net/feature_movies")
	body := `<nav><a href="/movie/his-girl-friday">Unrelated navigation</a></nav><div class="view-content"><div class="details"><div class="date"><a href="/year?v=1940">1940</a></div><h1><a href="/movie/his-girl-friday">His Girl Friday</a></h1></div><div class="details"><a href="/year?v=1950">1950</a><h1><a href="/movie/wrong-edition">His Girl Friday</a></h1></div><h1><a href="/movie/his-girl-friday-trailer">His Girl Friday Trailer</a></h1><h1><a href="https://evil.example/movie/his-girl-friday">His Girl Friday</a></h1><h1><a href="/movie/his-girl-friday"><img src="/image"></a></h1></div><a href="/feature_movies?page=1">2</a><a href="/feature_movies?page=1">Next</a><a href="/feature_movies?page=2">3</a><a href="/feature_movies?page=134">Last</a><a href="https://evil.example/feature_movies?page=1">Other</a>`
	got, pages := publicDomainFeaturePage([]byte(body), page, "His Girl Friday 1940")
	if len(got) != 1 || got[0].Title != "His Girl Friday" || got[0].ReleaseYear != 1940 || !got[0].ListingOnly || len(pages) != 2 || !strings.HasSuffix(pages[0], "page=1") || !strings.HasSuffix(pages[1], "page=2") {
		t.Fatalf("wrong observed catalog: %+v %+v", got, pages)
	}
}

func TestPublicDomainFeatureCatalogBoundsPagesAndRejectsHomepage(t *testing.T) {
	var calls atomic.Int32
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `<div class="view-content"><div class="details"><a href="/year?v=1940">1940</a><h1><a href="/movie/his-girl-friday">His Girl Friday</a></h1></div></div><a href="/feature_movies?page=1">2</a><a href="/feature_movies?page=2">3</a><a href="/feature_movies?page=3">4</a>`
		response := searchFixtureResponse(200, "text/html", body)
		response.Request = req
		return response, nil
	})}}
	got, err := s.queryPublicDomainFeatureCatalog(context.Background(), "His Girl Friday 1940")
	if err != nil || len(got) != 1 || calls.Load() != 3 {
		t.Fatalf("catalog request/candidate bounds lost: %d %d %v", len(got), calls.Load(), err)
	}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		response := searchFixtureResponse(200, "text/html", `<a href="/movie/his-girl-friday">His Girl Friday</a>`)
		copy := req.Clone(req.Context())
		copy.URL, _ = url.Parse("https://publicdomainmovie.net/")
		response.Request = copy
		return response, nil
	})}
	if _, err := s.queryPublicDomainFeatureCatalog(context.Background(), "His Girl Friday"); err == nil {
		t.Fatal("homepage substituted for feature catalog")
	}
}
