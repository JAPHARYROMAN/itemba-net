package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/scout"
)

func TestMovieDiscoveryRouteDefaultsAndDoesNotSearchAWatchProvider(t *testing.T) {
	calls := 0
	handler := movieDiscoveryHandler(func(ctx context.Context, mode, region string) scout.MovieDiscoveryReport {
		calls++
		deadline, ok := ctx.Deadline()
		if mode != "popular" || region != "ALL" || !ok || time.Until(deadline) > 10*time.Second {
			t.Fatalf("unexpected discovery request: %s %s", mode, region)
		}
		return scout.MovieDiscoveryReport{Mode: mode, Region: region, Items: []scout.MovieDiscoveryItem{{Title: "Fixture Film", Query: "Fixture Film 2026"}}}
	}, nil)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/scout/discover", nil))
	var report scout.MovieDiscoveryReport
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.Mode != "popular" || report.Region != "ALL" || len(report.Items) != 1 || calls != 1 {
		t.Fatalf("discovery route lost response/defaults: %s", response.Body)
	}
}

func TestMovieDiscoveryRouteRejectsUnsupportedInputWithoutCallingProviders(t *testing.T) {
	handler := movieDiscoveryHandler(func(context.Context, string, string) scout.MovieDiscoveryReport {
		t.Fatal("invalid input reached discovery")
		return scout.MovieDiscoveryReport{}
	}, nil)
	for _, test := range []struct {
		method, url string
		status      int
	}{
		{http.MethodPost, "/api/scout/discover", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/scout/discover?mode=free", http.StatusBadRequest},
		{http.MethodGet, "/api/scout/discover?region=worldwide", http.StatusBadRequest},
		{http.MethodGet, "/api/scout/discover?region=1A", http.StatusBadRequest},
		{http.MethodGet, "/api/scout/discover?source=other", http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(test.method, test.url, nil))
		if response.Code != test.status {
			t.Errorf("%s %s returned %d", test.method, test.url, response.Code)
		}
	}
}

func TestMovieDiscoveryFawesomeUsesGuestCatalogWithoutCallingAppleOrWebSearch(t *testing.T) {
	called := false
	handler := movieDiscoveryHandler(func(context.Context, string, string) scout.MovieDiscoveryReport {
		t.Fatal("Fawesome catalog request called Apple discovery")
		return scout.MovieDiscoveryReport{}
	}, func(ctx context.Context, mode, region string) scout.MovieDiscoveryReport {
		called = true
		if mode != "recent" || region != "ALL" {
			t.Fatalf("unexpected Fawesome request: %s %s", mode, region)
		}
		return scout.MovieDiscoveryReport{Mode: mode, Region: region, Items: []scout.MovieDiscoveryItem{{Title: "A live catalog film", Query: "A live catalog film", CatalogDate: "2026-10-18"}}}
	})
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/scout/discover?source=fawesome&mode=recent", nil))
	var report scout.MovieDiscoveryReport
	if response.Code != http.StatusOK || !called || json.Unmarshal(response.Body.Bytes(), &report) != nil || len(report.Items) != 1 || report.Items[0].ReleaseYear != 0 || report.Items[0].Query != "A live catalog film" || report.Items[0].CatalogDate != "2026-10-18" {
		t.Fatalf("Fawesome catalog route lost listing/date contract: %s", response.Body)
	}
}
