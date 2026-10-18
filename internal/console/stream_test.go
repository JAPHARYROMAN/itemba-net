package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

func fixtureReport(query, region string) scout.SearchReport {
	now := time.Now().UTC().Format(time.RFC3339)
	return scout.SearchReport{
		Query:         query,
		Region:        region,
		VerifiedCount: 1,
		ListedCount:   2,
		Providers:     []scout.ProviderStatus{{Name: "Internet Archive", Status: "ok"}},
		Sources: []scout.VerifiedSource{
			{
				CandidateSource:   scout.CandidateSource{Title: "Sintel", URL: "https://archive.org/details/sintel-dvd", Provider: "Internet Archive", AccessType: "free", ReleaseYear: 2010},
				AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", CheckedAt: now, Method: "media_probe"},
				PlaybackCheck:     &scout.PlaybackCheck{Status: "started", PlaybackConfirmed: true, Country: "TZ", CheckedAt: now, Method: "isolated_video_sample"},
			},
			{
				CandidateSource: scout.CandidateSource{Title: "Sintel (2010)", URL: "https://example.org/sintel", Provider: "Open Web", AccessType: "unknown"},
			},
		},
	}
}

func newStreamHandler(t *testing.T, search SearchFunc) http.Handler {
	t.Helper()
	sh := shield.New()
	sc := scout.New(sh, "http://127.0.0.1:0")
	t.Cleanup(sc.Close)
	return New(Dependencies{Shield: sh, Scout: sc, Vault: vault.NewManager(sh), Metrics: metrics.New(), Search: search}).Handler()
}

func TestSearchStreamSendsKnownReportMergedAndDone(t *testing.T) {
	var seen struct {
		query, region string
		fullLength    bool
	}
	handler := newStreamHandler(t, func(ctx context.Context, query, region string, options scout.SearchOptions) scout.SearchReport {
		seen.query, seen.region, seen.fullLength = query, region, options.FullLengthOnly
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("the burst must run under a deadline")
		}
		return fixtureReport(query, region)
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/scout/search/stream?query=Sintel+2010&region=tz&fullLengthOnly=false", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream returned %d %s", response.Code, response.Header().Get("Content-Type"))
	}
	if seen.query != "Sintel 2010" || seen.region != "TZ" || seen.fullLength {
		t.Fatalf("burst received %+v", seen)
	}
	body := response.Body.String()
	events := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(events) != 4 {
		t.Fatalf("expected four events, got %d:\n%s", len(events), body)
	}
	for i, name := range []string{"known", "report", "merged", "done"} {
		if !strings.HasPrefix(events[i], "event: "+name+"\ndata: ") {
			t.Fatalf("event %d should be %s:\n%s", i, name, events[i])
		}
	}
	if !strings.Contains(events[0], `"configured":false`) || !strings.Contains(events[0], `"titleKey":"sintel"`) || !strings.Contains(events[0], `"year":2010`) || !strings.Contains(events[0], `"locations":[]`) {
		t.Fatalf("known event without a database must carry the parsed query and no locations:\n%s", events[0])
	}
	if !strings.Contains(events[1], `"verifiedCount":1`) || strings.Contains(events[1], `"atlas"`) {
		t.Fatalf("report event must carry the report and no atlas summary without a database:\n%s", events[1])
	}
	merged := events[2]
	if !strings.Contains(merged, `"url":"https://archive.org/details/sintel-dvd","host":"archive.org","title":"Sintel","level":3,"levelName":"rendered"`) {
		t.Fatalf("the rendered source must lead the merged list:\n%s", merged)
	}
	if !strings.Contains(merged, `"egress":"TZ"`) || !strings.Contains(merged, `"origin":"fresh"`) || !strings.Contains(merged, `"relevance":1`) {
		t.Fatalf("merged entries carry egress, origin and relevance:\n%s", merged)
	}
	if !strings.Contains(merged, `"levelName":"listed"`) || !strings.Contains(merged, `"known":0,"fresh":2`) {
		t.Fatalf("the unchecked source is listed and the counts are reported:\n%s", merged)
	}
	if !strings.Contains(events[3], `"durationMs"`) {
		t.Fatalf("done event:\n%s", events[3])
	}
}

func TestSearchStreamValidatesLikeTheSearchRoute(t *testing.T) {
	handler := newStreamHandler(t, func(context.Context, string, string, scout.SearchOptions) scout.SearchReport {
		t.Fatal("an invalid request must never reach the burst")
		return scout.SearchReport{}
	})
	for _, target := range []string{"/api/scout/search/stream", "/api/scout/search/stream?query=", "/api/scout/search/stream?query=x&region=zzz", "/api/scout/search/stream?query=" + strings.Repeat("a", 501)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d, want 400", target, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/scout/search/stream?query=x", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST returned %d, want 405", response.Code)
	}
}

func TestSearchRouteUsesTheInjectedBurstAndReportsNoAtlas(t *testing.T) {
	handler := newStreamHandler(t, func(ctx context.Context, query, region string, options scout.SearchOptions) scout.SearchReport {
		return fixtureReport(query, region)
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/scout/search", strings.NewReader(`{"query":"Sintel"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"verifiedCount":1`) || strings.Contains(response.Body.String(), `"atlas"`) {
		t.Fatalf("search returned %d: %s", response.Code, response.Body.String())
	}
}

func TestMergeLocationsCombinesIndexAndFreshByURL(t *testing.T) {
	q := lens.Parse("Sintel 2010")
	report := fixtureReport("Sintel", "ALL")
	merged := mergeLocations(q, nil, report)
	if merged.Known != 0 || merged.Fresh != 2 || len(merged.Locations) != 2 || merged.Locations[0].Origin != "fresh" {
		t.Fatalf("fresh-only merge: %+v", merged)
	}
}
