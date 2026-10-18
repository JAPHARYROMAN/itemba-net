package scout

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/shield"
)

// testDrivers loads the shipped drivers so a search in tests fans out to the
// same hosts the binary does; fixture transports answer for those hosts.
func testDrivers(t *testing.T) []*crawler.Driver {
	t.Helper()
	drivers, err := crawler.LoadDir(filepath.Join("..", "..", "drivers"))
	if err != nil {
		t.Fatal(err)
	}
	return drivers
}

func testDriver(t *testing.T, name string) *crawler.Driver {
	t.Helper()
	for _, d := range testDrivers(t) {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("driver %s is not shipped", name)
	return nil
}

func driverScout(fn func(*http.Request) (*http.Response, error)) *Scout {
	client := &http.Client{Transport: searchFixtureTransport(fn)}
	return &Scout{shield: shield.New(), httpClient: client, availabilityHTTPClient: client}
}

// The Blender provider's own filter cases, now split between the driver
// (private, live, password, short, invalid id) and the search pipeline's
// relevance rule (trailers and tutorials).
func TestBlenderDriverSearchKeepsTheOfficialMovieAndDropsDerivatives(t *testing.T) {
	const id = "bf1f3fb5-b119-4f9f-9930-8e20e892b898"
	d := testDriver(t, "peertube-blender")
	for name, tc := range map[string]struct {
		title string
		want  int
	}{
		"official":      {"Big Buck Bunny", 1},
		"branded title": {"Big Buck Bunny - Blender animated cartoon", 1},
		"trailer":       {"Big Buck Bunny Trailer", 0},
		"tutorial":      {"Blender animation tutorial using Big Buck Bunny as an example", 0},
	} {
		t.Run(name, func(t *testing.T) {
			s := driverScout(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "video.blender.org" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
					t.Fatalf("unexpected official catalog request: %s", req.URL)
				}
				return searchFixtureResponse(200, "application/json", fmt.Sprintf(`{"data":[{"uuid":%q,"name":%q,"duration":596,"privacy":{"id":1},"url":"https://video.blender.org/videos/watch/%s"}]}`, id, tc.title, id)), nil
			})
			items, status := s.queryDriver(context.Background(), s.driverFetcher(), d, "Big Buck Bunny 2008", "Big Buck Bunny", "")
			if len(items) != tc.want {
				t.Fatalf("got %d items, want %d: %+v %+v", len(items), tc.want, items, status)
			}
			if tc.want == 0 {
				if status.Status != "empty" || status.Name != "Blender open movies" {
					t.Fatalf("an empty catalog is reported as empty: %+v", status)
				}
				return
			}
			item := items[0]
			if item.URL != "https://video.blender.org/videos/watch/"+id || item.DirectStreamURL != "" || !item.ListingOnly || item.AccessType != "unknown" || item.AccessBasis != "" || item.Duration != "9m56s" || item.Provider != "PeerTube (video.blender.org)" || item.Title != "Big Buck Bunny" {
				t.Fatalf("search result prematurely claimed movie access or kept branding: %#v", item)
			}
			if kind, resolvedID, ok := freeResolutionTarget(item); !ok || kind != "peertube" || resolvedID != id {
				t.Fatal("discovered movie did not enter the existing metadata-resolution flow")
			}
		})
	}
}

// The Archive provider's own edition test, now over driver listings with
// their collection tags.
func TestArchiveFeatureCandidatesRejectClipsMenusAndWrongYear(t *testing.T) {
	make := func(id, title, year string) CandidateSource {
		candidate := CandidateSource{ID: "ia-discovered-" + id, Title: title, URL: "https://archive.org/details/" + id, Provider: "Internet Archive", SourceType: "Archive video", AccessType: "unknown", ListingOnly: true}
		if year != "" {
			fmt.Sscanf(year, "%d", &candidate.ReleaseYear)
		}
		return candidate
	}
	feature := make("feature", "Charade", "1963")
	feature.Duration = "1:53:00"
	candidates := []CandidateSource{
		make("trailer", "Charade (1963) Trailer", ""),
		make("wrong", "Charade", "1953"),
		make("menu", "Charade (1963) DVD Menu", ""),
		make("collection", "Charade Collection", "1963"),
		make("other", "Charade Circuit 1963", ""),
		make("community", "Charade", "1963"),
		feature,
	}
	tags := map[string][]string{
		"https://archive.org/details/wrong":   {"feature_films"},
		"https://archive.org/details/feature": {"feature_films", "moviesandfilms"},
	}
	got := archiveFeatureCandidates("Charade 1963", candidates, tags)
	if len(got) != 2 || got[0].ID != "ia-discovered-feature" || got[1].ID != "ia-discovered-community" {
		t.Fatalf("wrong feature candidates: %+v", got)
	}
	if got[0].DirectStreamURL != "" || !got[0].ListingOnly {
		t.Fatal("search metadata alone claimed playable media")
	}
}

// The Archive provider's bounded-resolution test through the driver path.
func TestArchiveDriverSearchResolvesOnlyBoundedRelevantMetadata(t *testing.T) {
	var metadataRequests atomic.Int32
	var docs []string
	for i := 0; i < 8; i++ {
		docs = append(docs, fmt.Sprintf(`{"identifier":"film-%d","title":"His Girl Friday","year":"1940","collection":"feature_films"}`, i))
	}
	docs = append([]string{`{"identifier":"trailer","title":"His Girl Friday Trailer"}`}, docs...)
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/advancedsearch.php":
			return searchFixtureResponse(200, "application/json", `{"response":{"docs":[`+strings.Join(docs, ",")+`]}}`), nil
		case strings.HasPrefix(req.URL.Path, "/metadata/"):
			metadataRequests.Add(1)
			id := strings.TrimPrefix(req.URL.Path, "/metadata/")
			if id == "trailer" || !strings.HasPrefix(id, "film-") {
				t.Errorf("irrelevant item reached metadata: %s", id)
			}
			body := fmt.Sprintf(`{"metadata":{"identifier":%q,"title":"His Girl Friday","mediatype":"movies","year":"1940","runtime":"5520"},"files":[{"name":"trailer.mp4","length":"90","size":"100000"},{"name":"feature.mp4","length":"5520","size":"120000000"},{"name":"original.mp4","length":"5520","size":"900000000"}]}`, id)
			response := searchFixtureResponse(200, "application/json", body)
			response.Request = req
			return response, nil
		}
		t.Errorf("unexpected discovery request: %s", req.URL)
		return searchFixtureResponse(500, "text/plain", ""), nil
	})
	got, status := s.queryDriver(context.Background(), s.driverFetcher(), testDriver(t, "internet-archive"), "His Girl Friday 1940", "His Girl Friday", "")
	if status.Status != "ok" || len(got) != maxArchiveMovieMetadata || metadataRequests.Load() != maxArchiveMovieMetadata {
		t.Fatalf("unexpected bounded archive discovery: %d, %d, %+v", len(got), metadataRequests.Load(), status)
	}
	for _, source := range got {
		if !strings.HasSuffix(source.DirectStreamURL, "/feature.mp4") || source.AccessBasis != "archive_metadata" || source.Duration != "5520" || source.ListingOnly {
			t.Fatalf("unmatched/short edition chosen: %+v", source)
		}
	}
}

func TestDriversReportCancellationAsErrorsWithoutResults(t *testing.T) {
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	s.UseDrivers(testDrivers(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, statuses := s.queryDrivers(ctx, "Film", "US")
	if len(got) != 0 || len(statuses) == 0 {
		t.Fatalf("cancellation produced results: %+v %+v", got, statuses)
	}
	for _, status := range statuses {
		if status.Status != "error" {
			t.Fatalf("cancellation was hidden: %+v", status)
		}
	}
}

// Storefront drivers fan out per market worldwide and to one market for a
// country preference, with the legacy status names.
func TestDriversFanOutStorefrontsPerMarketAndSurfaceFailures(t *testing.T) {
	var mu sync.Mutex
	var markets []string
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "itunes.apple.com":
			country := req.URL.Query().Get("country")
			mu.Lock()
			markets = append(markets, country)
			mu.Unlock()
			if country == "JP" {
				return searchFixtureResponse(503, "text/plain", "down"), nil
			}
			return searchFixtureResponse(200, "application/json", fmt.Sprintf(`{"results":[{"kind":"feature-movie","trackId":7,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/%s/movie/fixture-film/id7","releaseDate":"2014-11-07T08:00:00Z","currency":"USD","trackPrice":9.99}]}`, strings.ToLower(country))), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}},"items":[]}`), nil
	})
	s.UseDrivers(testDrivers(t))

	candidates, statuses := s.queryDrivers(context.Background(), "Fixture Film 2014", "ALL")
	if len(markets) != 8 {
		t.Fatalf("worldwide must search the eight storefronts, saw %v", markets)
	}
	var stores, failures int
	for _, status := range statuses {
		if strings.HasPrefix(status.Name, "Apple movie catalog (") {
			stores++
			if status.Name == "Apple movie catalog (JP)" && status.Status != "error" {
				t.Fatalf("a failing storefront must be visible: %+v", status)
			}
			if status.Status == "error" {
				failures++
			}
		}
	}
	if stores != 8 || failures != 1 {
		t.Fatalf("statuses: %+v", statuses)
	}
	listed := 0
	for _, candidate := range candidates {
		if candidate.Provider == "Apple TV / iTunes" {
			listed++
			if candidate.Price != "Buy USD 9.99" || candidate.AccessType != "paid" || !candidate.ListingOnly || candidate.Region == "" || !strings.HasPrefix(candidate.ID, "apple-") {
				t.Fatalf("store listing: %+v", candidate)
			}
		}
	}
	if listed != 7 {
		t.Fatalf("seven storefronts answered, got %d listings", listed)
	}

	markets = nil
	_, statuses = s.queryDrivers(context.Background(), "Fixture Film 2014", "ke")
	if len(markets) != 1 || markets[0] != "KE" {
		t.Fatalf("a country preference searches that storefront only, saw %v", markets)
	}
	for _, status := range statuses {
		if strings.HasPrefix(status.Name, "Apple movie catalog (") && status.Name != "Apple movie catalog (KE)" {
			t.Fatalf("unexpected storefront status %+v", status)
		}
	}
}
