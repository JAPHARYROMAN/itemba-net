// Package console serves the operator interface: the HTTP API, the real-time
// WebSocket multiplexer and the built dashboard. It owns no state of its own;
// every request is routed to the scout, vault, shield, relay or atlas
// subsystem, and metrics observe the traffic.
package console

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 1048576,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Dependencies are the subsystems the console exposes to the operator.
type Dependencies struct {
	Shield *shield.Shield
	Scout  *scout.Scout
	Vault  *vault.Manager
	// Relay serves the zero-trust media proxy. It is optional.
	Relay http.Handler
	// Atlas is the persistent index. Nil means no database is configured,
	// which the status route reports as such.
	Atlas *atlas.Store
	// Metrics, when set, serves /metrics and observes requests and searches.
	Metrics *metrics.Metrics
	// StaticDir holds the built dashboard. It is served at the root path only
	// when its index.html exists, so an API-only deployment needs no frontend.
	StaticDir string
}

// Server routes operator requests to the subsystems.
type Server struct {
	shield    *shield.Shield
	scout     *scout.Scout
	vault     *vault.Manager
	relay     http.Handler
	atlas     *atlas.Store
	metrics   *metrics.Metrics
	staticDir string
}

// New creates a console over the given subsystems.
func New(deps Dependencies) *Server {
	return &Server{
		shield:    deps.Shield,
		scout:     deps.Scout,
		vault:     deps.Vault,
		relay:     deps.Relay,
		atlas:     deps.Atlas,
		metrics:   deps.Metrics,
		staticDir: deps.StaticDir,
	}
}

// Handler returns the complete HTTP handler: API routes, testbenches, the
// WebSocket multiplexer, metrics, the dashboard and permissive CORS for local
// tooling.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	registerTestbenchRoutes(mux)

	// Zero-Trust HLS (.m3u8) & MP4/WebM Media Stream Relay Proxy
	if s.relay != nil {
		mux.Handle("/api/relay/stream", s.relay)
	}

	// Unified System Status
	mux.HandleFunc("/api/status", s.status)

	// Pillar 1: itemba-scout Federated Search & Stream Verification
	mux.HandleFunc("/api/scout/discover", movieDiscoveryHandler(s.scout.DiscoverMovies, s.scout.DiscoverFawesomeMovies))
	mux.HandleFunc("/api/scout/fawesome/playback", fawesomeShelfPlaybackHandler(s.scout))
	mux.HandleFunc("/api/scout/search", s.search)

	// Pillar 2: itemba-vault Launch / Navigate Ephemeral Sandbox
	mux.HandleFunc("/api/vault/launch", s.vaultLaunch)
	mux.HandleFunc("/api/vault/control", s.vaultControl)
	mux.HandleFunc("/api/vault/theater", s.vaultTheater)
	mux.HandleFunc("/api/vault/destroy", s.vaultDestroy)
	mux.HandleFunc("/api/vault/webrtc/offer", s.vaultWebRTCOffer)

	// Pillar 3: itemba-shield Custom Rule Addition, Rule Tester, Live Sync & Security Benchmark
	mux.HandleFunc("/api/shield/rules", s.shieldRules)
	mux.HandleFunc("/api/shield/test", s.shieldTest)
	mux.HandleFunc("/api/shield/sync", s.shieldSync)
	mux.HandleFunc("/api/shield/benchmark", s.shieldBenchmark)

	// Real-Time WebSocket Multiplexer (Shield Telemetry + Viewport Stream + Input Events)
	mux.HandleFunc("/api/ws", s.websocket)

	if s.metrics != nil {
		mux.Handle("/metrics", s.metrics.Handler())
	}

	if s.staticDir != "" {
		if _, err := os.Stat(filepath.Join(s.staticDir, "index.html")); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
		}
	}

	var handler http.Handler = mux
	if s.metrics != nil {
		handler = s.metrics.Middleware(handler)
	}
	return corsMiddleware(handler)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"vault":  s.vault.Status(),
		"shield": s.shield.Snapshot(),
		"atlas":  s.atlas.Health(ctx),
	})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Query          string `json:"query"`
		Region         string `json:"region"`
		FullLengthOnly *bool  `json:"fullLengthOnly"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "A JSON movie search request is required."})
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	req.Region = strings.ToUpper(strings.TrimSpace(req.Region))
	if req.Region == "" {
		req.Region = "ALL"
	}
	if req.Query == "" || len([]rune(req.Query)) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Enter a movie title or URL using at most 500 characters."})
		return
	}
	if req.Region != "ALL" && (len(req.Region) != 2 || req.Region[0] < 'A' || req.Region[0] > 'Z' || req.Region[1] < 'A' || req.Region[1] > 'Z') {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Choose Worldwide or a two-letter country code."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 44*time.Second)
	defer cancel()

	fullLengthOnly := req.FullLengthOnly == nil || *req.FullLengthOnly
	started := time.Now()
	report := s.scout.SearchWithOptions(ctx, req.Query, req.Region, scout.SearchOptions{FullLengthOnly: fullLengthOnly})
	if s.metrics != nil {
		s.metrics.ObserveSearch(report, time.Since(started))
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) vaultLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL          string `json:"url"`
		TheaterMode  bool   `json:"theaterMode"`
		PreferDocker bool   `json:"preferDocker"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		http.Error(w, "valid target url required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()

	status, err := s.vault.LaunchOrNavigate(ctx, req.URL, req.TheaterMode, req.PreferDocker)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
			"vault": status,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vault":  status,
		"shield": s.shield.Snapshot(),
	})
}

// vaultControl handles browser navigation: back, forward and reload.
func (s *Server) vaultControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess := s.vault.ActiveSession()
	if sess == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no active vault session"})
		return
	}
	var req struct {
		Action string `json:"action"` // "back", "forward", "reload"
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	switch req.Action {
	case "back":
		_ = sess.GoBack(ctx)
	case "forward":
		_ = sess.GoForward(ctx)
	case "reload":
		_ = sess.Reload(ctx)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vault": s.vault.Status(),
	})
}

// vaultTheater triggers unified theater-mode isolation of the active player.
func (s *Server) vaultTheater(w http.ResponseWriter, r *http.Request) {
	sess := s.vault.ActiveSession()
	if sess == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no active vault session"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	res, err := sess.IsolateTheater(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result": res,
		"vault":  s.vault.Status(),
	})
}

// vaultDestroy tears the ephemeral sandbox down with zero residue.
func (s *Server) vaultDestroy(w http.ResponseWriter, r *http.Request) {
	s.vault.Destroy()
	writeJSON(w, http.StatusOK, map[string]any{
		"vault": s.vault.Status(),
	})
}

// vaultWebRTCOffer answers a Pion WebRTC SDP offer for the active session.
func (s *Server) vaultWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess := s.vault.ActiveSession()
	if sess == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no active vault session"})
		return
	}
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SDP == "" {
		http.Error(w, "sdp offer required", http.StatusBadRequest)
		return
	}
	answerSDP, err := sess.NegotiateWebRTC(req.SDP)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "answer",
		"sdp":  answerSDP,
	})
}

func (s *Server) shieldRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Pattern string `json:"pattern"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.shield.Engine().AddCustomRule(req.Pattern)
	writeJSON(w, http.StatusOK, s.shield.Snapshot())
}

func (s *Server) shieldTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL          string `json:"url"`
		ResourceType string `json:"resourceType"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	match := s.shield.Engine().Inspect(req.URL, req.ResourceType)
	writeJSON(w, http.StatusOK, match)
}

func (s *Server) shieldSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	added, err := s.shield.Engine().SyncRemoteList(ctx)
	resp := map[string]any{
		"addedDomains": added,
		"shield":       s.shield.Snapshot(),
	}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) shieldBenchmark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	report := s.scout.RunSecurityBenchmark(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"report": report,
		"shield": s.shield.Snapshot(),
	})
}

// websocket multiplexes shield telemetry and sandbox viewport frames to the
// operator, and forwards the operator's input events to the active session.
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	sendRaw := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return conn.WriteMessage(websocket.TextMessage, payload)
	}

	threatCh, unsubThreat := s.shield.Subscribe()
	defer unsubThreat()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ev vault.InputEvent
			if json.Unmarshal(msg, &ev) == nil && ev.Type != "" {
				if sess := s.vault.ActiveSession(); sess != nil {
					sess.DispatchInput(ev)
				}
			}
		}
	}()

	var (
		attachedSession *vault.Session
		frameCh         chan []byte
		unsubFrames     func()
	)
	defer func() {
		if unsubFrames != nil {
			unsubFrames()
		}
	}()

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		curSess := s.vault.ActiveSession()
		if curSess != attachedSession {
			if unsubFrames != nil {
				unsubFrames()
				unsubFrames = nil
				frameCh = nil
			}
			attachedSession = curSess
			if attachedSession != nil {
				frameCh, unsubFrames = attachedSession.SubscribeFrames()
			}
		}

		select {
		case <-done:
			return
		case ev := <-threatCh:
			pkt, _ := json.Marshal(map[string]any{
				"type":   "threat",
				"threat": ev,
				"shield": s.shield.Snapshot(),
			})
			if sendRaw(pkt) != nil {
				return
			}
		case frameBytes := <-frameCh:
			if len(frameBytes) > 0 {
				if sendRaw(frameBytes) != nil {
					return
				}
			}
		case <-ticker.C:
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
