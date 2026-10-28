package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/relay"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

func newTestHandler(t *testing.T, staticDir string) http.Handler {
	t.Helper()
	sh := shield.New()
	sc := scout.New(sh, "http://127.0.0.1:0")
	t.Cleanup(sc.Close)
	vlt := vault.NewManager(sh)
	t.Cleanup(vlt.Destroy)
	return New(Dependencies{
		Shield:    sh,
		Scout:     sc,
		Vault:     vlt,
		Relay:     relay.NewHandler(sh),
		Metrics:   metrics.New(),
		StaticDir: staticDir,
	}).Handler()
}

func TestHandlerServesStatusTestbenchMetricsAndCORSWithoutADashboard(t *testing.T) {
	handler := newTestHandler(t, t.TempDir())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status route returned %d: %s", response.Code, response.Body.String())
	}
	var status struct {
		Vault  json.RawMessage `json:"vault"`
		Shield json.RawMessage `json:"shield"`
		Atlas  struct {
			Configured bool `json:"configured"`
			Connected  bool `json:"connected"`
		} `json:"atlas"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil || len(status.Vault) == 0 || len(status.Shield) == 0 {
		t.Fatalf("status route must report vault and shield: %v %s", err, response.Body.String())
	}
	if status.Atlas.Configured || status.Atlas.Connected {
		t.Fatal("without a database the status route must report atlas as unconfigured")
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("request without an Origin received CORS origin header %q", got)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodOptions, "/api/scout/search", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight returned %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/testbench/hls/master.m3u8", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "#EXTM3U") {
		t.Fatalf("testbench manifest returned %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("an empty static directory must not be served, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `itemba_http_requests_total{code="200",method="GET",route="/api/status"} 1`) {
		t.Fatalf("metrics route returned %d or did not count the status request:\n%s", response.Code, body)
	}
}

func TestHandlerAllowsOnlyItsOwnBrowserOrigin(t *testing.T) {
	handler := newTestHandler(t, t.TempDir())
	for _, test := range []struct {
		name, method, origin, allowOrigin string
		wantStatus                        int
	}{
		{"non-browser client", http.MethodGet, "", "", http.StatusOK},
		{"same origin", http.MethodGet, "http://example.com", "http://example.com", http.StatusOK},
		{"TLS terminated upstream", http.MethodGet, "https://example.com", "https://example.com", http.StatusOK},
		{"foreign origin", http.MethodGet, "https://foreign.example", "", http.StatusForbidden},
		{"malformed origin", http.MethodGet, "null", "", http.StatusForbidden},
		{"same-origin preflight", http.MethodOptions, "https://example.com", "https://example.com", http.StatusNoContent},
		{"foreign preflight", http.MethodOptions, "https://foreign.example", "", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "/api/status", nil)
			req.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.wantStatus || response.Header().Get("Access-Control-Allow-Origin") != test.allowOrigin {
				t.Fatalf("status=%d allow-origin=%q, want %d %q", response.Code, response.Header().Get("Access-Control-Allow-Origin"), test.wantStatus, test.allowOrigin)
			}
		})
	}
}

func TestHandlerServesTheBuiltDashboardWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>ITEMBA NET</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t, dir)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ITEMBA NET") {
		t.Fatalf("dashboard returned %d: %s", response.Code, response.Body.String())
	}
}

func TestSearchRouteValidatesInputBeforeTouchingProviders(t *testing.T) {
	handler := newTestHandler(t, "")

	for _, body := range []string{"", "{}", `{"query":"Sintel","region":"zzz"}`, `{"query":"` + strings.Repeat("a", 501) + `"}`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/scout/search", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q returned %d, want 400", body, response.Code)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/scout/search", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET search returned %d, want 405", response.Code)
	}
}

func TestVaultRoutesReportNoActiveSession(t *testing.T) {
	handler := newTestHandler(t, "")

	for _, route := range []string{"/api/vault/control", "/api/vault/webrtc/offer"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{"action":"reload","sdp":"v=0"}`))
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "no active vault session") {
			t.Fatalf("%s returned %d: %s", route, response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/vault/destroy", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("destroy without a session returned %d", response.Code)
	}
}

func TestHandlerWorksWithoutMetrics(t *testing.T) {
	sh := shield.New()
	sc := scout.New(sh, "http://127.0.0.1:0")
	t.Cleanup(sc.Close)
	handler := New(Dependencies{Shield: sh, Scout: sc, Vault: vault.NewManager(sh)}).Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("metrics must not be served when not configured, got %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status returned %d", response.Code)
	}
}
