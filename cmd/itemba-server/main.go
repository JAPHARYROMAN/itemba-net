package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/itemba-net/core/internal/relay"
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

func main() {
	if err := loadLocalConfig(".env.local"); err != nil {
		log.Fatalf("local configuration: %v", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	sh := shield.New()
	sc := scout.New(sh, baseURL)
	vlt := vault.NewManager(sh)
	relayHandler := relay.NewHandler(sh)

	defer func() {
		vlt.Destroy()
		sc.Close()
	}()

	mux := http.NewServeMux()
	registerTestbenchRoutes(mux)

	// Zero-Trust HLS (.m3u8) & MP4/WebM Media Stream Relay Proxy
	mux.Handle("/api/relay/stream", relayHandler)

	// 1. Unified System Status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"vault":  vlt.Status(),
			"shield": sh.Snapshot(),
		})
	})

	// 2. Pillar 1: itemba-scout Federated Search & Stream Verification
	mux.HandleFunc("/api/scout/discover", movieDiscoveryHandler(sc.DiscoverMovies, sc.DiscoverFawesomeMovies))
	mux.HandleFunc("/api/scout/fawesome/playback", fawesomeShelfPlaybackHandler(sc))
	mux.HandleFunc("/api/scout/search", func(w http.ResponseWriter, r *http.Request) {
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
		report := sc.SearchWithOptions(ctx, req.Query, req.Region, scout.SearchOptions{FullLengthOnly: fullLengthOnly})
		writeJSON(w, http.StatusOK, report)
	})

	// 3. Pillar 2: itemba-vault Launch / Navigate Ephemeral Sandbox
	mux.HandleFunc("/api/vault/launch", func(w http.ResponseWriter, r *http.Request) {
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

		status, err := vlt.LaunchOrNavigate(ctx, req.URL, req.TheaterMode, req.PreferDocker)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": err.Error(),
				"vault": status,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"vault":  status,
			"shield": sh.Snapshot(),
		})
	})

	// 4. Vault Browser Navigation Controls (Back, Forward, Reload)
	mux.HandleFunc("/api/vault/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess := vlt.ActiveSession()
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
			"vault": vlt.Status(),
		})
	})

	// 5. Milestone 4: Unified Theater Mode Isolation Trigger
	mux.HandleFunc("/api/vault/theater", func(w http.ResponseWriter, r *http.Request) {
		sess := vlt.ActiveSession()
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
			"vault":  vlt.Status(),
		})
	})

	// 6. Ephemeral Sandbox Destruction (Zero-Residue Teardown)
	mux.HandleFunc("/api/vault/destroy", func(w http.ResponseWriter, r *http.Request) {
		vlt.Destroy()
		writeJSON(w, http.StatusOK, map[string]any{
			"vault": vlt.Status(),
		})
	})

	// 7. Pion WebRTC SDP Offer/Answer Signaling Endpoint
	mux.HandleFunc("/api/vault/webrtc/offer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess := vlt.ActiveSession()
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
	})

	// 8. Pillar 3: itemba-shield Custom Rule Addition, Rule Tester, Live Sync & Security Benchmark
	mux.HandleFunc("/api/shield/rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Pattern string `json:"pattern"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sh.Engine().AddCustomRule(req.Pattern)
		writeJSON(w, http.StatusOK, sh.Snapshot())
	})

	mux.HandleFunc("/api/shield/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			URL          string `json:"url"`
			ResourceType string `json:"resourceType"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		match := sh.Engine().Inspect(req.URL, req.ResourceType)
		writeJSON(w, http.StatusOK, match)
	})

	mux.HandleFunc("/api/shield/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		added, err := sh.Engine().SyncRemoteList(ctx)
		resp := map[string]any{
			"addedDomains": added,
			"shield":       sh.Snapshot(),
		}
		if err != nil {
			resp["warning"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/shield/benchmark", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		report := sc.RunSecurityBenchmark(ctx)
		writeJSON(w, http.StatusOK, map[string]any{
			"report": report,
			"shield": sh.Snapshot(),
		})
	})

	// 9. Real-Time WebSocket Multiplexer (Shield Telemetry + Viewport Stream + Input Events)
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
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

		threatCh, unsubThreat := sh.Subscribe()
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
					if sess := vlt.ActiveSession(); sess != nil {
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
			curSess := vlt.ActiveSession()
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
					"shield": sh.Snapshot(),
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
	})

	if _, err := os.Stat("web/dist/index.html"); err == nil {
		fs := http.FileServer(http.Dir("web/dist"))
		mux.Handle("/", fs)
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: corsMiddleware(mux),
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("[ITEMBA-NET] Shutting down and destroying active sandboxes...")
		vlt.Destroy()
		sc.Close()
		_ = server.Close()
	}()

	log.Printf("================================================================")
	log.Printf(" ITEMBA NET Control Plane Online at %s", baseURL)
	log.Printf(" • Pillar 1: itemba-scout (Federated Crawler & Pooled CDP Verifier)")
	log.Printf(" • Pillar 2: itemba-vault (Hybrid RBI Sandbox & Zero-Trust Stream Relay)")
	log.Printf(" • Pillar 3: itemba-shield (Triple-Layer DNS/CDP/DOM Sanitizer)")
	log.Printf("================================================================")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
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
