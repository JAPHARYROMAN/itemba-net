package metrics

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", response.Code)
	}
	return response.Body.String()
}

func mustContain(t *testing.T, body string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(body, line) {
			t.Fatalf("scrape is missing %q\n%s", line, body)
		}
	}
}

func TestObserveSearchRecordsDurationSourcesProvidersAndProbes(t *testing.T) {
	m := New()
	started := "started"
	report := scout.SearchReport{
		Cached:        false,
		VerifiedCount: 2,
		ListedCount:   5,
		PrunedCount:   1,
		Providers: []scout.ProviderStatus{
			{Name: "Apple movie catalog (US)", Status: "ok"},
			{Name: "Tavily web search", Status: "not_configured"},
		},
		Sources: []scout.VerifiedSource{
			{PlaybackCheck: &scout.PlaybackCheck{Status: started}},
			{PlaybackCheck: &scout.PlaybackCheck{Status: "not_started"}},
			{},
		},
	}
	m.ObserveSearch(report, 1500*time.Millisecond)
	m.ObserveSearch(scout.SearchReport{Cached: true}, 20*time.Millisecond)

	mustContain(t, scrape(t, m),
		`itemba_search_duration_seconds_count{cached="false"} 1`,
		`itemba_search_duration_seconds_count{cached="true"} 1`,
		`itemba_search_duration_seconds_bucket{cached="false",le="2"} 1`,
		`itemba_search_sources_total{kind="verified"} 2`,
		`itemba_search_sources_total{kind="listed"} 5`,
		`itemba_search_sources_total{kind="pruned"} 1`,
		`itemba_search_provider_results_total{provider="Apple movie catalog (US)",status="ok"} 1`,
		`itemba_probe_outcomes_total{status="started"} 1`,
		`itemba_probe_outcomes_total{status="not_started"} 1`,
		`go_goroutines`,
	)
}

type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func TestMiddlewareLabelsRoutesAndStatusAndPassesHijackThrough(t *testing.T) {
	m := New()
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ws":
			if _, _, err := w.(http.Hijacker).Hijack(); err != nil {
				t.Fatal(err)
			}
		case "/api/status":
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))

	for _, path := range []string{"/api/status", "/assets/index.js", "/favicon.ico"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	recorder := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/ws", nil))
	if !recorder.hijacked {
		t.Fatal("the WebSocket upgrade needs Hijack to reach the underlying writer")
	}

	mustContain(t, scrape(t, m),
		`itemba_http_requests_total{code="200",method="GET",route="/api/status"} 1`,
		`itemba_http_requests_total{code="404",method="GET",route="static"} 2`,
		`itemba_http_requests_total{code="101",method="GET",route="/api/ws"} 1`,
	)
}

func TestCollectorsReadLiveSubsystemsAndAnUnconfiguredAtlas(t *testing.T) {
	m := New()
	sh := shield.New()
	m.RegisterShield(sh)
	m.RegisterVault(vault.NewManager(sh))
	m.RegisterAtlas(nil)

	mustContain(t, scrape(t, m),
		`itemba_shield_blocked_total{layer="dns"} 0`,
		`itemba_shield_blocked_total{layer="popup"} 0`,
		`itemba_shield_allowed_total 0`,
		`itemba_shield_rules{kind="dns_sinkhole_domains"}`,
		`itemba_vault_session_active 0`,
		`itemba_vault_frames_streamed_total 0`,
		`itemba_atlas_configured 0`,
		`itemba_atlas_connected 0`,
		`itemba_atlas_migrations_pending 0`,
	)
}
