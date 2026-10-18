package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestArchiveFeatureSearchKeepsTitleSeparateFromYear(t *testing.T) {
	u, err := url.Parse(archiveMovieSearchURL("watch His Girl Friday (1940) full movie"))
	if err != nil {
		t.Fatal(err)
	}
	values := u.Query()
	if values.Get("q") != `title:("His Girl Friday") AND mediatype:movies` || values.Get("rows") != "30" || values.Get("sort[]") != "downloads desc" || !strings.Contains(values.Get("fl[]"), "collection") {
		t.Fatalf("feature search lost its metadata window/title: %s", u)
	}
	if q := archiveMovieSearchURL(`Charade" OR mediatype:texts`); strings.Contains(q, `Charade%22`) {
		t.Fatal("untrusted title broke out of literal search phrase")
	}
}

func TestArchiveFeatureCandidatesRejectClipsMenusAndWrongYear(t *testing.T) {
	docs := []archiveMovieDocument{
		{Identifier: "trailer", Title: "Charade (1963) Trailer"},
		{Identifier: "wrong", Title: "Charade", Year: "1953", Collection: "feature_films"},
		{Identifier: "menu", Title: "Charade (1963) DVD Menu"},
		{Identifier: "collection", Title: "Charade Collection", Year: "1963"},
		{Identifier: "other", Title: "Charade Circuit 1963"},
		{Identifier: "community", Title: "Charade", Year: "1963"},
		{Identifier: "feature", Title: "Charade", Year: "1963", Runtime: "1:53:00", Collection: []any{"feature_films", "moviesandfilms"}},
	}
	got := archiveFeatureCandidates("Charade 1963", docs)
	if len(got) != 2 || got[0].ID != "ia-discovered-feature" || got[1].ID != "ia-discovered-community" {
		t.Fatalf("wrong feature candidates: %+v", got)
	}
	if got[0].DirectStreamURL != "" || !got[0].ListingOnly {
		t.Fatal("search metadata alone claimed playable media")
	}
}

func TestArchiveFeatureSearchResolvesOnlyBoundedRelevantMetadata(t *testing.T) {
	var metadataRequests atomic.Int32
	var docs []archiveMovieDocument
	for i := 0; i < 8; i++ {
		docs = append(docs, archiveMovieDocument{Identifier: fmt.Sprintf("film-%d", i), Title: "His Girl Friday", Year: "1940", Collection: "feature_films"})
	}
	docs = append([]archiveMovieDocument{{Identifier: "trailer", Title: "His Girl Friday Trailer"}}, docs...)
	encoded, _ := json.Marshal(map[string]any{"response": map[string]any{"docs": docs}})
	s := &Scout{httpClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/advancedsearch.php" {
			t.Errorf("unexpected discovery request: %s", req.URL)
		}
		return searchFixtureResponse(200, "application/json", string(encoded)), nil
	})}}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		metadataRequests.Add(1)
		id := strings.TrimPrefix(req.URL.Path, "/metadata/")
		if id == "trailer" || !strings.HasPrefix(id, "film-") {
			t.Errorf("irrelevant item reached metadata: %s", id)
		}
		body := fmt.Sprintf(`{"metadata":{"identifier":%q,"title":"His Girl Friday","mediatype":"movies","year":"1940","runtime":"5520"},"files":[{"name":"trailer.mp4","length":"90","size":"100000"},{"name":"feature.mp4","length":"5520","size":"120000000"},{"name":"original.mp4","length":"5520","size":"900000000"}]}`, id)
		response := searchFixtureResponse(200, "application/json", body)
		response.Request = req
		return response, nil
	})}
	got, err := s.queryInternetArchive(context.Background(), "His Girl Friday 1940")
	if err != nil || len(got) != maxArchiveMovieMetadata || metadataRequests.Load() != maxArchiveMovieMetadata {
		t.Fatalf("unexpected bounded archive discovery: %d, %d, %v", len(got), metadataRequests.Load(), err)
	}
	for _, source := range got {
		if !strings.HasSuffix(source.DirectStreamURL, "/feature.mp4") || source.AccessBasis != "archive_metadata" || source.Duration != "5520" || source.ListingOnly {
			t.Fatalf("unmatched/short edition chosen: %+v", source)
		}
	}
}

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
