package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func movieChartFixtureEntry(id, title, date, market string) map[string]any {
	return map[string]any{
		"im:name":        map[string]string{"label": title},
		"im:releaseDate": map[string]string{"label": date},
		"im:contentType": map[string]any{"attributes": map[string]string{"term": "Movie"}},
		"id":             map[string]any{"label": "https://itunes.apple.com/" + market + "/movie/film/id" + id, "attributes": map[string]string{"im:id": id}},
		"im:image": []any{
			map[string]any{"label": "https://is1-ssl.mzstatic.com/image/thumb/movie/39x60.png", "attributes": map[string]string{"height": "60"}},
			map[string]any{"label": "https://is1-ssl.mzstatic.com/image/thumb/movie/113x170.png", "attributes": map[string]string{"height": "170"}},
		},
	}
}

func movieChartFixtureResponse(entries ...map[string]any) *http.Response {
	encoded, _ := json.Marshal(map[string]any{"feed": map[string]any{"entry": entries}})
	return providerResponse(200, string(encoded))
}

func TestDiscoverMoviesWorldwideCombinesActualChartsAndReportsPartialFailures(t *testing.T) {
	var calls, active, peak atomic.Int32
	now := time.Now().UTC()
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		if req.URL.Hostname() != "itunes.apple.com" || !strings.HasSuffix(req.URL.Path, "/rss/topmovies/limit=25/json") {
			t.Errorf("unsupported chart URL: %s", req.URL)
		}
		market := strings.Split(strings.Trim(req.URL.Path, "/"), "/")[0]
		if market == "de" {
			return providerResponse(503, "private upstream diagnostic must not be exposed"), nil
		}
		if market == "jp" {
			return movieChartFixtureResponse(), nil
		}
		shared := movieChartFixtureEntry("1", "Popular Feature", now.AddDate(0, -3, 0).Format(time.RFC3339), market)
		if market == "gb" {
			shared["im:name"] = map[string]string{"label": "Localized Feature Title"}
		}
		old := movieChartFixtureEntry("3", "Classic Feature", "1940-01-01T00:00:00Z", market)
		if market == "in" {
			return movieChartFixtureResponse(shared, old, movieChartFixtureEntry("2", "Recent Feature", now.AddDate(0, 0, -2).Format(time.RFC3339), market)), nil
		}
		return movieChartFixtureResponse(shared, old), nil
	})
	report := s.DiscoverMovies(context.Background(), "popular", "")
	if report.Region != "ALL" || report.Mode != "popular" || report.Cached || len(report.Providers) != 8 || len(report.Items) != 3 || calls.Load() != 8 || peak.Load() > 4 {
		t.Fatalf("wrong global chart coverage: %+v calls=%d peak=%d", report, calls.Load(), peak.Load())
	}
	first := report.Items[0]
	if first.Title != "Popular Feature" || len(first.Markets) != 6 || first.Markets[0] != "US" || first.Query != "Popular Feature" || first.ReleaseYear != 0 || !strings.HasSuffix(first.Thumbnail, "113x170.png") {
		t.Fatalf("chart deduplication/provenance incorrect: %+v", first)
	}
	if !strings.Contains(report.Detail, "Some requested markets") || strings.Contains(report.Providers[4].Detail, "private upstream") {
		t.Fatal("partial coverage or sanitized failure missing")
	}
	// Popular and Recent share the chart fetch, but return independent slices.
	report.Items[0].Markets[0] = "BROKEN"
	recent := s.DiscoverMovies(context.Background(), "recent", "ALL")
	if !recent.Cached || calls.Load() != 8 || len(recent.Items) != 2 || recent.Items[0].Title != "Recent Feature" || recent.Items[1].Markets[0] != "US" {
		t.Fatalf("recent cache/projection corrupted: %+v calls=%d", recent, calls.Load())
	}
	encoded, _ := json.Marshal(recent)
	for _, unsupportedClaim := range []string{"accessType", "playbackConfirmed", "chartScore", "directStreamUrl"} {
		if strings.Contains(string(encoded), unsupportedClaim) {
			t.Fatalf("chart metadata acquired watch claim or internal field %q", unsupportedClaim)
		}
	}
}

func TestMovieDiscoveryRecentUsesPast24MonthsAndSortsDates(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	base := MovieDiscoveryReport{Region: "ALL", Providers: []ProviderStatus{{Status: "ok"}}, Items: []MovieDiscoveryItem{
		{ID: "old", ReleaseDate: "2024-10-17", Markets: []string{"US"}},
		{ID: "boundary", ReleaseDate: "2024-10-18", Markets: []string{"US"}},
		{ID: "future", ReleaseDate: "2026-10-19", Markets: []string{"US"}},
		{ID: "unknown", Markets: []string{"US"}},
		{ID: "today", ReleaseDate: "2026-10-18", Markets: []string{"US"}},
	}}
	report := projectMovieDiscovery(base, "recent", now)
	if len(report.Items) != 2 || report.Items[0].ID != "today" || report.Items[1].ID != "boundary" || !strings.Contains(report.Detail, "24 months") {
		t.Fatalf("recent chart dates were misrepresented: %+v", report)
	}
	base.Items = nil
	if empty := projectMovieDiscovery(base, "recent", now); len(empty.Items) != 0 || !strings.Contains(empty.Detail, "No chart films") {
		t.Fatal("empty recent chart did not explain its date scope")
	}
}

func TestDiscoverMoviesSingleMarketAndCacheExpiration(t *testing.T) {
	var calls atomic.Int32
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Path != "/gb/rss/topmovies/limit=25/json" {
			t.Errorf("optional market request changed scope: %s", req.URL)
		}
		return movieChartFixtureResponse(movieChartFixtureEntry("1", "Film", "2025-03-01T00:00:00Z", "gb")), nil
	})
	first := s.DiscoverMovies(context.Background(), "unrecognized", "gb")
	if first.Region != "GB" || first.Mode != "popular" || len(first.Providers) != 1 || first.Items[0].Markets[0] != "GB" {
		t.Fatalf("single market normalization failed: %+v", first)
	}
	s.discoveryMu.Lock()
	entry := s.discoveryCache["GB"]
	entry.at = time.Now().Add(-16 * time.Minute)
	s.discoveryCache["GB"] = entry
	s.discoveryMu.Unlock()
	second := s.DiscoverMovies(context.Background(), "popular", "GB")
	if second.Cached || calls.Load() != 2 {
		t.Fatal("expired chart cache did not refresh")
	}
}

func TestMovieChartParsingRejectsNonMoviesAndUnsafeCatalogLinks(t *testing.T) {
	song := movieChartFixtureEntry("1", "A Song", "2026-01-01", "us")
	song["im:contentType"] = map[string]any{"attributes": map[string]string{"term": "Music"}}
	unsafe := movieChartFixtureEntry("2", "Wrong Host", "2026-01-01", "us")
	unsafe["id"] = map[string]any{"label": "https://other.site/movie/film/id2", "attributes": map[string]string{"im:id": "2"}}
	valid := movieChartFixtureEntry("3", "Movie &amp; Name", "invalid-date", "us")
	valid["im:image"] = []any{map[string]any{"label": "http://127.0.0.1/poster.png", "attributes": map[string]string{"height": "1000"}}}
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		return movieChartFixtureResponse(song, unsafe, valid), nil
	})
	items, status := s.fetchMovieChart(context.Background(), "US")
	if status.Status != "ok" || len(items) != 1 || items[0].Title != "Movie & Name" || items[0].Query != "Movie & Name" || items[0].Thumbnail != "" || items[0].ReleaseYear != 0 {
		t.Fatalf("unsupported chart entry promoted: %+v %+v", status, items)
	}
}

func TestDiscoverMoviesHonorsCancellationAndDoesNotCacheTotalFailure(t *testing.T) {
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	report := s.DiscoverMovies(ctx, "popular", "ALL")
	if time.Since(start) > time.Second || len(report.Items) != 0 || len(report.Providers) != 8 || len(s.discoveryCache) != 0 || !strings.Contains(report.Detail, "could not supply") {
		t.Fatalf("cancelled chart discovery was not bounded/truthful: %+v", report)
	}
}

func TestMovieDiscoveryDoesNotTurnStorefrontDatesIntoOriginalMovieYears(t *testing.T) {
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		return movieChartFixtureResponse(
			movieChartFixtureEntry("1", "Cleaner", "2026-10-14", "au"),
			movieChartFixtureEntry("2", "The Fix (2026)", "2027-01-01", "au"),
			movieChartFixtureEntry("3", "Cleaner", "2010-01-01", "au"),
		), nil
	})
	report := s.DiscoverMovies(context.Background(), "popular", "AU")
	if len(report.Items) != 3 {
		t.Fatal("same titles with unknown film years were merged across different catalog identities")
	}
	byTitle := map[string]MovieDiscoveryItem{}
	for _, item := range report.Items {
		byTitle[item.Title] = item
	}
	if plain := byTitle["Cleaner"]; plain.ReleaseYear != 0 || plain.Query != "Cleaner" {
		t.Fatalf("storefront date invented an original film year or constrained search: %+v", plain)
	}
	if explicit := byTitle["The Fix (2026)"]; explicit.ReleaseYear != 2026 || explicit.Query != "The Fix (2026)" {
		t.Fatalf("primary title's explicit film year was duplicated or replaced by a catalog date: %+v", explicit)
	}
	if !strings.Contains(report.Detail, "older reissues") || !strings.Contains(report.Detail, "not original film") {
		t.Fatal("catalog date scope was hidden")
	}
}

func TestMovieChartListingsSearchesBeyondShelfLimitAndPreservesPrimaryMarket(t *testing.T) {
	now := time.Now().UTC()
	base := MovieDiscoveryReport{Region: "ALL", CheckedAt: now.Format(time.RFC3339), Providers: []ProviderStatus{{Name: "Apple movie chart (GB)", Status: "ok"}}, Items: []MovieDiscoveryItem{}}
	for i := 1; i <= 25; i++ {
		base.Items = append(base.Items, MovieDiscoveryItem{ID: "apple-chart-" + strconv.Itoa(i), Title: "Shelf Film " + strconv.Itoa(i), SourceURL: "https://itunes.apple.com/gb/movie/film/id" + strconv.Itoa(i), Markets: []string{"GB"}, chartScore: 1 / float64(i)})
	}
	base.Items[24].Title = "Current Feature Film"
	base.Items[24].ReleaseDate = "2026-10-14"
	base.Items[24].ReleaseYear = 2026 // A stale/incorrect date-derived hint must never become a film year.
	base.Items[24].Markets = []string{"US", "GB", "AU"}
	base.Items[24].chartScore = 0.001
	// Keep it below the 24 visible shelf cards despite its extra chart markets.
	for i := 0; i < 24; i++ {
		base.Items[i].Markets = []string{"US", "GB", "AU", "IN"}
	}
	s := providerTestScout(func(req *http.Request) (*http.Response, error) {
		t.Fatal("fresh chart cache unexpectedly fetched again")
		return nil, nil
	})
	s.discoveryCache = map[string]cachedMovieDiscovery{"ALL": {report: base, at: now}}
	visible := s.DiscoverMovies(context.Background(), "popular", "ALL")
	for _, item := range visible.Items {
		if item.Title == "Current Feature Film" {
			t.Fatal("fixture was not outside the shelf projection")
		}
	}
	got, status := s.queryMovieChartListings(context.Background(), "Current Feature Film 2026", "ALL")
	if status.Status != "ok" || len(got) != 1 {
		t.Fatalf("manual search missed full chart cache: %+v %+v", status, got)
	}
	source := got[0]
	if source.URL != "https://itunes.apple.com/gb/movie/film/id25" || source.Region != "GB" || source.Provider != "Apple TV / iTunes" || source.SourceType != "Movie Store" || source.ReleaseYear != 0 || source.AccessType != "paid" || !source.ListingOnly || source.Price != "" || source.DirectStreamURL != "" {
		t.Fatalf("chart listing invented market/year/offer/playback: %+v", source)
	}
	if source.AccessEvidenceURL != source.URL || !strings.Contains(source.AccessNote, "unconfirmed") {
		t.Fatal("primary catalog evidence or offer boundary missing")
	}
}

func TestMovieChartListingsUsesExactTitleAndPrimaryExplicitYear(t *testing.T) {
	now := time.Now().UTC()
	base := MovieDiscoveryReport{Region: "US", CheckedAt: now.Format(time.RFC3339), Providers: []ProviderStatus{{Status: "ok"}}, Items: []MovieDiscoveryItem{
		{ID: "apple-chart-1", Title: "Current Feature Film (2025)", SourceURL: "https://itunes.apple.com/us/movie/current/id1", Markets: []string{"US"}},
		{ID: "apple-chart-2", Title: "Current Feature Film Extended", SourceURL: "https://itunes.apple.com/us/movie/extended/id2", Markets: []string{"US"}},
		{ID: "apple-chart-3", Title: "Current Feature Film Trailer", SourceURL: "https://itunes.apple.com/us/movie/trailer/id3", Markets: []string{"US"}},
	}}
	s := &Scout{discoveryCache: map[string]cachedMovieDiscovery{"US": {report: base, at: now}}}
	got, _ := s.queryMovieChartListings(context.Background(), "Current Feature Film 2025", "US")
	if len(got) != 1 || got[0].ReleaseYear != 2025 || got[0].URL != base.Items[0].SourceURL {
		t.Fatalf("exact primary title/year not preserved: %+v", got)
	}
	if wrong, _ := s.queryMovieChartListings(context.Background(), "Current Feature Film 2026", "US"); len(wrong) != 0 {
		t.Fatal("explicitly different film year or partial title was accepted")
	}
}
