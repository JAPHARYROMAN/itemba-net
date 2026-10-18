package crawler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// liveStore opens the disposable database named by ITEMBA_TEST_DATABASE_URL
// with a fresh atlas schema.
func liveStore(t *testing.T) (*atlas.Store, context.Context) {
	t.Helper()
	url := os.Getenv("ITEMBA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ITEMBA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	store, err := atlas.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.ResetForTests(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

type countingObserver struct{ crawls, listings int }

func (o *countingObserver) ObserveCrawl(string, string)     { o.crawls++ }
func (o *countingObserver) ObserveListings(_ string, n int) { o.listings += n }

func TestCrawlerSeedsSearchesHostsAndWritesListingsToAtlas(t *testing.T) {
	store, ctx := liveStore(t)
	drivers, err := LoadDir(filepath.Join("..", "..", "drivers"))
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.UpsertObject(ctx, atlas.Object{Title: "Fixture Film", Year: 2014})
	if err != nil {
		t.Fatal(err)
	}
	// A location with stale evidence should be refreshed as a page fetch.
	host, _ := store.UpsertHost(ctx, "pages.example", "Pages")
	stale, err := store.UpsertLocation(ctx, atlas.Location{ObjectID: object.ID, HostID: host.ID, URL: "https://pages.example/fixture-film", AccessType: "free"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvidence(ctx, atlas.Evidence{LocationID: stale.ID, Level: lens.LevelReachable, Method: "http_page", Status: "page_reachable", ObservedAt: time.Now().Add(-10 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var appleCalls, archiveCalls, pageCalls int
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "itunes.apple.com":
			appleCalls++
			country := strings.ToLower(req.URL.Query().Get("country"))
			if req.URL.Query().Get("term") != "Fixture Film" {
				t.Errorf("apple term: %s", req.URL)
			}
			return jsonResponse(200, `{"results":[{"kind":"feature-movie","trackId":7,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/`+country+`/movie/fixture-film/id7","releaseDate":"2014-11-07T08:00:00Z","currency":"USD","trackPrice":9.99}]}`), nil
		case "archive.org":
			archiveCalls++
			return jsonResponse(200, `{"response":{"docs":[{"identifier":"fixture-film","title":"Fixture Film (2014)"},{"identifier":"fixture-film-trailer","title":"Fixture Film Trailer"}]}}`), nil
		case "pages.example":
			pageCalls++
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: http.NoBody, Request: req}, nil
		}
		t.Errorf("unexpected request %s", req.URL)
		return jsonResponse(500, `{}`), nil
	})
	observer := &countingObserver{}
	c, err := New(Options{Store: store, Drivers: drivers, Transport: transport, Observer: observer, Batch: 50, Workers: 4, SeedEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	report, err := c.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Eight Apple markets, one Archive search and one stale page.
	if report.Seeded != 10 || report.Leased != 10 || report.Searches != 9 || report.Pages != 1 || report.Errors != 0 || report.Backoffs != 0 {
		t.Fatalf("unexpected cycle: %+v", report)
	}
	if appleCalls != 8 || archiveCalls != 1 || pageCalls != 1 {
		t.Fatalf("host calls: apple=%d archive=%d page=%d", appleCalls, archiveCalls, pageCalls)
	}
	if report.Listings != 10 || observer.listings != 10 || observer.crawls != 10 {
		t.Fatalf("eight store listings and two archive docs should be recorded: %+v observer=%+v", report, observer)
	}

	known, err := store.Lookup(ctx, lens.Parse("Fixture Film 2014"), lens.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	// Lookup also returns related objects by full text (the trailer), so count
	// only the locations that belong to the seeded object.
	var storefronts, archive, pages int
	for _, k := range known {
		if k.Object.ID != object.ID {
			continue
		}
		switch k.Host.Hostname {
		case "itunes.apple.com":
			storefronts++
			if k.Location.AccessType != "paid" || k.Location.Price != "Buy USD 9.99" || k.Evidence == nil || k.Evidence.Method != "driver:apple-movies" || k.EffectiveLevel != lens.LevelListed {
				t.Fatalf("store listing evidence: %+v", k)
			}
		case "archive.org":
			archive++
		case "pages.example":
			pages++
			if k.Evidence == nil || k.Evidence.Method != "http_page_fetch" || k.Evidence.Status != "http_200" || k.EffectiveLevel != lens.LevelReachable {
				t.Fatalf("the stale page must have fresh reachable evidence: %+v", k)
			}
		}
	}
	if storefronts != 8 || archive != 1 || pages != 1 {
		t.Fatalf("known locations for the object: storefronts=%d archive=%d pages=%d (%d total)", storefronts, archive, pages, len(known))
	}
	trailer, err := store.Lookup(ctx, lens.Parse("Fixture Film Trailer"), lens.DefaultLimit)
	if err != nil || len(trailer) != 1 || trailer[0].Object.ID == object.ID {
		t.Fatalf("a differently titled listing gets its own object: %v %+v", err, trailer)
	}

	again, err := c.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Leased != 0 || appleCalls != 8 {
		t.Fatalf("completed entries rest until their refresh time: %+v", again)
	}
	status := c.Status(ctx)
	if !status.Enabled || status.Cycles != 2 || status.Frontier.Total != 10 || status.Frontier.Due != 0 || status.Frontier.Leased != 0 || len(status.Drivers) != 2 {
		t.Fatalf("status: %+v", status)
	}
}

func TestCrawlerNeedsAStoreAndDrivers(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("a crawler without a store must be refused")
	}
	if _, err := New(Options{Store: &atlas.Store{}}); err == nil {
		t.Fatal("a crawler without drivers must be refused")
	}
}
