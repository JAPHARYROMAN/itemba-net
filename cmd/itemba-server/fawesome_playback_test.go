package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/scout"
)

const shelfFixtureJobID = "0123456789abcdef0123456789abcdef"

type shelfPlaybackFixtureService struct {
	starts, reads, cancels int
	fail                   bool
}

func (s *shelfPlaybackFixtureService) StartFawesomeShelfPlayback(ctx context.Context, mode string) (scout.FawesomeShelfPlaybackReport, error) {
	s.starts++
	if s.fail {
		return scout.FawesomeShelfPlaybackReport{}, errors.New("private provider session token must not be returned")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 4*time.Second || mode != "recent" {
		return scout.FawesomeShelfPlaybackReport{}, errors.New("invalid bounded start")
	}
	return scout.FawesomeShelfPlaybackReport{JobID: shelfFixtureJobID, Mode: mode, Region: "ALL", State: "running", AdBlockingEnabled: true, Total: 2}, nil
}
func (s *shelfPlaybackFixtureService) FawesomeShelfPlaybackStatus(id string) (scout.FawesomeShelfPlaybackReport, bool) {
	s.reads++
	return scout.FawesomeShelfPlaybackReport{JobID: id, State: "complete", Region: "ALL", AdBlockingEnabled: true}, id == shelfFixtureJobID
}
func (s *shelfPlaybackFixtureService) CancelFawesomeShelfPlayback(id string) (scout.FawesomeShelfPlaybackReport, bool) {
	s.cancels++
	return scout.FawesomeShelfPlaybackReport{JobID: id, State: "cancelled"}, id == shelfFixtureJobID
}

func TestFawesomeShelfRouteStartPollCancelAreSeparateAndGlobal(t *testing.T) {
	service := &shelfPlaybackFixtureService{}
	handler := fawesomeShelfPlaybackHandler(service)
	for _, item := range []struct{ method, path, body, want string }{
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent"}`, "running"},
		{http.MethodGet, "/api/scout/fawesome/playback?job=" + shelfFixtureJobID, "", "complete"},
		{http.MethodDelete, "/api/scout/fawesome/playback?job=" + shelfFixtureJobID, "", "cancelled"},
	} {
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		response := httptest.NewRecorder()
		handler(response, req)
		var report scout.FawesomeShelfPlaybackReport
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.State != item.want || report.JobID != shelfFixtureJobID {
			t.Fatalf("%s failed: %d %s", item.method, response.Code, response.Body)
		}
		if item.method != http.MethodDelete && (report.Region != "ALL" || !report.AdBlockingEnabled) {
			t.Fatal("shelf checks lost global/protected semantics")
		}
	}
	if service.starts != 1 || service.reads != 1 || service.cancels != 1 {
		t.Fatal("status or cancellation started another check round")
	}
}

func TestFawesomeShelfRouteRejectsUnboundedForeignAndMalformedStarts(t *testing.T) {
	for _, item := range []struct {
		method, path, body, origin, contentType string
		status                                  int
	}{
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent","url":"https://example.com"}`, "", "application/json", 400},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent","country":"US"}`, "", "application/json", 400},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent"} {}`, "", "application/json", 400},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"other"}`, "", "application/json", 400},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent"}`, "https://foreign.example", "application/json", 403},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"recent"}`, "", "text/plain", 415},
		{http.MethodPost, "/api/scout/fawesome/playback", `{"mode":"` + strings.Repeat("x", 600) + `"}`, "", "application/json", 400},
		{http.MethodGet, "/api/scout/fawesome/playback?job=other", "", "", "", 400},
		{http.MethodDelete, "/api/scout/fawesome/playback?job=" + shelfFixtureJobID, "", "https://foreign.example", "", 403},
		{http.MethodPut, "/api/scout/fawesome/playback", "", "", "", 405},
	} {
		service := &shelfPlaybackFixtureService{}
		handler := fawesomeShelfPlaybackHandler(service)
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		req.Header.Set("Origin", item.origin)
		req.Header.Set("Content-Type", item.contentType)
		response := httptest.NewRecorder()
		handler(response, req)
		if response.Code != item.status || service.starts+service.reads+service.cancels != 0 {
			t.Fatalf("invalid request reached background work: %s status=%d body=%s", item.method, response.Code, response.Body)
		}
	}
}

func TestFawesomeShelfRouteDoesNotExposePrivateStartErrors(t *testing.T) {
	handler := fawesomeShelfPlaybackHandler(&shelfPlaybackFixtureService{fail: true})
	req := httptest.NewRequest(http.MethodPost, "/api/scout/fawesome/playback", strings.NewReader(`{"mode":"recent"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler(response, req)
	if response.Code != 503 || strings.Contains(response.Body.String(), "token") {
		t.Fatal("private error escaped the background route")
	}
}
