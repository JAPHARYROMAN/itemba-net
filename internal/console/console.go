// Package console serves the operator interface: the HTTP API, the real-time
// WebSocket multiplexer and the built dashboard. It owns no state of its own;
// every request is routed to the scout, vault, shield, relay or atlas
// subsystem, and metrics observe the traffic.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

// Connection bounds for the console's listener. A client that opens a
// connection and never finishes its request headers is closed after
// ReadHeaderTimeout; one that finishes the headers but not the body it
// announced is closed after ReadTimeout, counted from the request's first
// byte; a kept-alive connection that sends no further request is closed after
// IdleTimeout. The dashboard polls /api/status every 2.2 seconds, so a live
// connection stays reused. Before these bounds such a connection was held,
// and its goroutine with it, until the client went away.
//
// ReadTimeout never reaches a running handler: net/http clears the request's
// read deadline when the body has been consumed (or at once for a request
// without one) and before it starts the background read that cancels the
// request context on a lost client. The search's 44-second burst and the
// vault's 18-second launch therefore run to their own deadlines.
//
// WriteTimeout stays unset on purpose. It would start when the headers are
// read and cut every response slower than it: the search routes run for up to
// 44 seconds plus the Atlas write, the media relay copies a stream for as
// long as the player reads it, and the search stream flushes events for the
// whole burst. Each handler bounds its own work with a context instead. The
// websocket is hijacked out of the server's deadlines entirely and keeps its
// own: see Server.websocket.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 5 * time.Second
	IdleTimeout       = 5 * time.Second
)

// Websocket liveness. The server pings every third of wsPongWait and drops a
// peer that sends nothing, not even a pong, for wsPongWait. Browsers answer
// pings on their own, so a live dashboard is never cut, while a peer whose
// machine vanished is closed within wsPongWait instead of being held until
// TCP gives up. Every write, the handshake included, has wsWriteWait.
const (
	defaultWSPongWait = 30 * time.Second
	wsWriteWait       = 3 * time.Second
)

// Request body caps, in bytes. ReadTimeout bounds how long a body may take to
// arrive; these bound how large it may be, so a client cannot push an
// unbounded body into the JSON decoder. Every JSON route reads its body
// through http.MaxBytesReader (see decodeJSONBody), answers 413 when the cap
// is hit and then closes the connection, since the rest of the body is never
// read.
//
// The search, launch, control, rules and test bodies carry a short query, a
// URL, an action word or one filter rule, for which 8 KB is generous. The
// WebRTC offer carries an SDP. The dashboard's offer (two data channels, no
// media, sent before ICE gathering: web/src/components/VaultViewport.tsx)
// measured 502 bytes of JSON in Chrome 152 and 632 bytes once gathered; the
// same connection with a recvonly video and audio pair measured 9.0 KB and
// nine m-lines 34.5 KB. 64 KB therefore leaves room for media the vault does
// not stream today while staying far below anything a browser emits.
const (
	SearchBodyLimit           = 8 << 10
	VaultLaunchBodyLimit      = 8 << 10
	VaultControlBodyLimit     = 8 << 10
	VaultWebRTCOfferBodyLimit = 64 << 10
	ShieldRulesBodyLimit      = 8 << 10
	ShieldTestBodyLimit       = 8 << 10
)

var wsUpgrader = websocket.Upgrader{
	HandshakeTimeout: wsWriteWait,
	ReadBufferSize:   65536,
	WriteBufferSize:  1048576,
	CheckOrigin:      sameOrigin,
}

// dashboardCSP is the content policy for the built dashboard only. The
// testbench pages are exempt: they exist to run inline scripts and inline
// handlers on purpose, and a policy strict enough to protect the dashboard
// would disable the very behaviour they are there to exercise.
//
// Scripts come from the bundle alone, which is the protection that matters:
// an injected inline script or a third-party script host is refused. Styles
// need 'unsafe-inline' because React and the player set style attributes.
// Images, media, frames and connections allow http(s) hosts because the
// operator can play a source directly instead of through the relay, and
// provider artwork is loaded from the provider. Workers and blobs cover
// hls.js, and data:/blob: cover the vault's frame images.
const dashboardCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https: http:; " +
	"media-src 'self' data: blob: https: http:; " +
	"frame-src 'self' https: http:; " +
	"connect-src 'self' ws: wss: https: http:; " +
	"font-src 'self' data:; " +
	"worker-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'"

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
	// Crawler, when set, reports the continuous crawl on /api/crawler/status.
	Crawler *crawler.Crawler
	// Search runs the discovery burst. It defaults to Scout's search and is
	// replaceable so tests can run the search routes offline.
	Search SearchFunc
	// StaticDir holds the built dashboard. It is served at the root path only
	// when its index.html exists, so an API-only deployment needs no frontend.
	StaticDir string
}

// SearchFunc is the discovery burst behind the search routes.
type SearchFunc func(ctx context.Context, query, region string, options scout.SearchOptions) scout.SearchReport

// Server routes operator requests to the subsystems.
type Server struct {
	shield    *shield.Shield
	scout     *scout.Scout
	vault     *vault.Manager
	relay     http.Handler
	atlas     *atlas.Store
	metrics   *metrics.Metrics
	crawler   *crawler.Crawler
	search    SearchFunc
	staticDir string
	// wsPongWait is how long the websocket waits for a frame from the
	// operator before treating the peer as gone. Tests shorten it.
	wsPongWait time.Duration
}

// New creates a console over the given subsystems.
func New(deps Dependencies) *Server {
	search := deps.Search
	if search == nil && deps.Scout != nil {
		search = deps.Scout.SearchWithOptions
	}
	return &Server{
		shield:     deps.Shield,
		scout:      deps.Scout,
		vault:      deps.Vault,
		relay:      deps.Relay,
		atlas:      deps.Atlas,
		metrics:    deps.Metrics,
		crawler:    deps.Crawler,
		search:     search,
		staticDir:  deps.StaticDir,
		wsPongWait: defaultWSPongWait,
	}
}

// HTTPServer returns the server that listens on addr with the console's
// connection bounds. The handler is Handler.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		IdleTimeout:       IdleTimeout,
	}
}

// Handler returns the complete HTTP handler: API routes, testbenches, the
// WebSocket multiplexer, metrics and the dashboard, restricted to its own
// browser origin.
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
	mux.HandleFunc("/api/convert", s.convertRoute)
	mux.HandleFunc("/api/formats", s.formatsRoute)
	mux.HandleFunc("/api/atlas/resolve", s.resolveRoute)
	mux.HandleFunc("/api/scout/search", s.searchRoute)
	mux.HandleFunc("/api/scout/search/stream", s.searchStream)

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

	// Phase 2: the continuous crawl's status
	mux.HandleFunc("/api/crawler/status", s.crawlerStatus)

	if s.metrics != nil {
		mux.Handle("/metrics", s.metrics.Handler())
	}

	if s.staticDir != "" {
		if _, err := os.Stat(filepath.Join(s.staticDir, "index.html")); err == nil {
			mux.Handle("/", dashboardHandler(http.FileServer(http.Dir(s.staticDir))))
		}
	}

	var handler http.Handler = mux
	if s.metrics != nil {
		handler = s.metrics.Middleware(handler)
	}
	return securityHeaders(corsMiddleware(handler))
}

// dashboardHandler serves the built dashboard with the dashboard content
// policy. Assets receive the policy too; it costs nothing there and keeps one
// rule for everything the bundle serves.
func dashboardHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", dashboardCSP)
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies the console's browser-boundary defaults to every
// response, including the CORS refusal and the relay's. They are deliberately
// conservative: nothing in the console is meant to be framed by another site,
// sniffed into another type, send a referrer to a provider, or use a device
// permission. HSTS is set only when the request arrived over TLS, directly or
// through a proxy that says it terminated TLS, so a plain-HTTP development
// host never teaches a browser to upgrade it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "SAMEORIGIN")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
			header.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
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

// searchRoute answers POST /api/scout/search with the full discovery report,
// writing it back to Atlas when a database is configured.
func (s *Server) searchRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Query          string `json:"query"`
		Region         string `json:"region"`
		Kind           string `json:"kind"`
		FullLengthOnly *bool  `json:"fullLengthOnly"`
	}
	rejected, err := decodeJSONBody(w, r, SearchBodyLimit, &req)
	if rejected {
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "A JSON search request is required."})
		return
	}
	query, region, kind, errMsg := validateSearch(req.Query, req.Region, req.Kind)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), searchDeadline(kind))
	defer cancel()

	fullLengthOnly := req.FullLengthOnly == nil || *req.FullLengthOnly
	started := time.Now()
	report := s.search(ctx, query, region, scout.SearchOptions{FullLengthOnly: fullLengthOnly, Kind: kind})
	if s.metrics != nil {
		s.metrics.ObserveSearch(report, time.Since(started))
	}
	event := reportEvent{SearchReport: report}
	event.Atlas, event.AtlasError = s.record(r.Context(), lens.ParseAs(query, kind), report)
	writeJSON(w, http.StatusOK, event)
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
	rejected, err := decodeJSONBody(w, r, VaultLaunchBodyLimit, &req)
	if rejected {
		return
	}
	if err != nil || strings.TrimSpace(req.URL) == "" {
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

// vaultControl handles browser navigation: back, forward and reload. The body
// is read, within its cap, before the session is looked up, so an oversized
// body is refused whether or not a sandbox is running.
func (s *Server) vaultControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string `json:"action"` // "back", "forward", "reload"
	}
	if rejected, _ := decodeJSONBody(w, r, VaultControlBodyLimit, &req); rejected {
		return
	}
	sess := s.vault.ActiveSession()
	if sess == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no active vault session"})
		return
	}
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

// vaultWebRTCOffer answers a Pion WebRTC SDP offer for the active session. As
// in vaultControl, the body is read within its cap before the session is
// looked up.
func (s *Server) vaultWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SDP string `json:"sdp"`
	}
	rejected, err := decodeJSONBody(w, r, VaultWebRTCOfferBodyLimit, &req)
	if rejected {
		return
	}
	if err != nil || req.SDP == "" {
		http.Error(w, "sdp offer required", http.StatusBadRequest)
		return
	}
	sess := s.vault.ActiveSession()
	if sess == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no active vault session"})
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
	if rejected, _ := decodeJSONBody(w, r, ShieldRulesBodyLimit, &req); rejected {
		return
	}
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
	if rejected, _ := decodeJSONBody(w, r, ShieldTestBodyLimit, &req); rejected {
		return
	}
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
//
// The connection is hijacked from the HTTP server, so the server's deadlines
// no longer apply and the handler keeps its own: every write has wsWriteWait,
// and the read side expects a frame (an input event or the pong to the
// server's ping) within wsPongWait, else the peer is gone and the connection
// is closed.
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	write := func(messageType int, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return conn.WriteMessage(messageType, payload)
	}
	sendRaw := func(payload []byte) error {
		return write(websocket.TextMessage, payload)
	}

	pongWait := s.wsPongWait
	if pongWait <= 0 {
		pongWait = defaultWSPongWait
	}
	alive := func() error { return conn.SetReadDeadline(time.Now().Add(pongWait)) }
	_ = alive()
	conn.SetPongHandler(func(string) error { return alive() })

	threatCh, unsubThreat := s.shield.Subscribe()
	defer unsubThreat()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					log.Printf("[console] websocket %s sent nothing for %s; closing", r.RemoteAddr, pongWait)
				}
				return
			}
			_ = alive()
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
	pings := time.NewTicker(pongWait / 3)
	defer pings.Stop()

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
		case <-pings.C:
			if write(websocket.PingMessage, nil) != nil {
				return
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

// decodeJSONBody decodes one JSON value from a request body of at most limit
// bytes into v. A larger body is answered here with 413 in the console's
// error shape, and rejected tells the caller to return; the server then
// closes the connection, since the rest of the body is never read. Any other
// decode error is returned for the caller to judge, because some routes
// accept an empty body.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, v any) (rejected bool, err error) {
	err = json.NewDecoder(http.MaxBytesReader(rootResponseWriter(w), r.Body, limit)).Decode(v)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": fmt.Sprintf("The request body may be at most %d KB.", limit>>10),
		})
		return true, err
	}
	return false, err
}

// rootResponseWriter follows Unwrap through middleware wrappers, such as the
// metrics recorder, to net/http's own writer. MaxBytesReader tells only that
// writer when a cap is hit, with no unwrapping of its own; told, the server
// sends Connection: close with the 413 and stops reading the body, where
// otherwise it would reuse the connection after draining up to 256 KB of the
// rest. MaxBytesReader never writes through the writer it is given, so the
// response itself still goes through the wrappers.
func rootResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		inner := wrapper.Unwrap()
		if inner == nil {
			return w
		}
		w = inner
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !sameOrigin(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "Use ITEMBA NET from its own origin."})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
