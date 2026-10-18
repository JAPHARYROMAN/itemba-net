package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/shield"
	"github.com/pion/webrtc/v4"
)

// SessionStatus describes the current state of the active RBI sandbox for the dashboard header and controls.
type SessionStatus struct {
	Active            bool                   `json:"active"`
	AdBlockingEnabled bool                   `json:"adBlockingEnabled"`
	SessionID         string                 `json:"sessionId,omitempty"`
	Mode              string                 `json:"mode,omitempty"` // "docker-container" or "local-isolated"
	ContainerID       string                 `json:"containerId,omitempty"`
	CurrentURL        string                 `json:"currentUrl,omitempty"`
	PageTitle         string                 `json:"pageTitle,omitempty"`
	TheaterActive     bool                   `json:"theaterActive"`
	WebRTCState       string                 `json:"webrtcState"`
	FramesStreamed    int64                  `json:"framesStreamed"`
	UptimeSeconds     int64                  `json:"uptimeSeconds"`
	ExtractedStreams  []shield.SniffedStream `json:"extractedStreams"`
}

// InputEvent represents a mouse, wheel, or keyboard event sent from the client viewport over WebRTC DataChannel / WS.
type InputEvent struct {
	Type       string  `json:"type"` // "mousedown", "mouseup", "mousemove", "wheel", "keydown", "keyup", "text"
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button,omitempty"` // "left", "right", "middle"
	ClickCount int     `json:"clickCount,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Key        string  `json:"key,omitempty"`
	Code       string  `json:"code,omitempty"`
	Text       string  `json:"text,omitempty"`
	KeyCode    int     `json:"keyCode,omitempty"`
}

// FramePacket is sent to the frontend viewport over WebRTC DataChannel or WebSocket.
type FramePacket struct {
	Type      string `json:"type"` // "frame"
	Data      string `json:"data"` // base64 JPEG
	Timestamp int64  `json:"ts"`
	URL       string `json:"url,omitempty"`
}

// Manager manages the lifecycle of the isolated browser sandbox and its WebRTC/WebSocket streams.
type Manager struct {
	launchMu sync.Mutex
	mu       sync.Mutex
	shield   *shield.Shield
	session  *Session
	seq      atomic.Int64
}

// Session represents an active ephemeral RBI browser container/instance.
type Session struct {
	ID            string
	CreatedAt     time.Time
	Browser       *cdp.BrowserInstance
	Shield        *shield.Shield
	CurrentURL    string
	PageTitle     string
	TheaterActive bool
	UseDocker     bool

	streamsMu        sync.RWMutex
	extractedStreams []shield.SniffedStream
	streamSet        map[string]bool

	webrtcMu    sync.RWMutex
	peerConn    *webrtc.PeerConnection
	frameDC     *webrtc.DataChannel
	webrtcState string

	frameListenersMu  sync.RWMutex
	frameListeners    map[chan []byte]struct{}
	lastFrame         []byte
	lastFrameTime     atomic.Int64
	framesCount       atomic.Int64
	closed            atomic.Bool
	adBlockingEnabled atomic.Bool
	navigationSeq     atomic.Uint64
	closeCh           chan struct{}
}

// NewManager creates a new Vault Sandbox Manager.
func NewManager(sh *shield.Shield) *Manager {
	return &Manager{
		shield: sh,
	}
}

// Status returns the current active sandbox status.
func (m *Manager) Status() SessionStatus {
	m.mu.Lock()
	s := m.session
	m.mu.Unlock()

	if s == nil || s.ready() != nil {
		return SessionStatus{
			Active:           false,
			WebRTCState:      "disconnected",
			ExtractedStreams: []shield.SniffedStream{},
		}
	}

	s.webrtcMu.RLock()
	rtcState := s.webrtcState
	s.webrtcMu.RUnlock()
	if rtcState == "" {
		rtcState = "ready"
	}

	s.streamsMu.RLock()
	streams := make([]shield.SniffedStream, len(s.extractedStreams))
	copy(streams, s.extractedStreams)
	s.streamsMu.RUnlock()

	return SessionStatus{
		Active:            true,
		AdBlockingEnabled: s.adBlockingEnabled.Load(),
		SessionID:         s.ID,
		Mode:              s.Browser.Mode,
		ContainerID:       s.Browser.ContainerID,
		CurrentURL:        s.CurrentURL,
		PageTitle:         s.PageTitle,
		TheaterActive:     s.TheaterActive,
		WebRTCState:       rtcState,
		FramesStreamed:    s.framesCount.Load(),
		UptimeSeconds:     int64(time.Since(s.CreatedAt).Seconds()),
		ExtractedStreams:  streams,
	}
}

// LaunchOrNavigate spawns a fresh ephemeral sandbox (or navigates existing) to targetURL.
func (m *Manager) LaunchOrNavigate(ctx context.Context, targetURL string, autoTheater bool, preferDocker bool) (SessionStatus, error) {
	if err := validateTargetURL(targetURL); err != nil {
		return m.Status(), err
	}
	if err := ctx.Err(); err != nil {
		return m.Status(), err
	}
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return m.Status(), err
	}

	m.mu.Lock()
	if m.session != nil && !m.session.closed.Load() {
		sess := m.session
		m.mu.Unlock()
		if sess.Browser != nil && !sess.Browser.IsClosed() {
			err := sess.Navigate(ctx, targetURL, autoTheater)
			return m.Status(), err
		}
		sess.Close()
		m.mu.Lock()
		m.session = nil
	}
	m.mu.Unlock()

	id := fmt.Sprintf("vlt-%d", m.seq.Add(1))
	browser, err := cdp.Launch(ctx, cdp.LaunchOptions{
		ID:           id,
		Width:        1280,
		Height:       720,
		MuteAudio:    false,
		HostResolver: m.shield.Engine().HostResolverRules(),
		UseDocker:    preferDocker,
	})
	if err != nil {
		return SessionStatus{}, err
	}

	sess := &Session{
		ID:               id,
		CreatedAt:        time.Now(),
		Browser:          browser,
		Shield:           m.shield,
		CurrentURL:       targetURL,
		UseDocker:        preferDocker,
		webrtcState:      "signaling-ready",
		extractedStreams: []shield.SniffedStream{},
		streamSet:        make(map[string]bool),
		frameListeners:   make(map[chan []byte]struct{}),
		closeCh:          make(chan struct{}),
	}
	err = m.shield.Attach(ctx, browser, nil, func(stream shield.SniffedStream) {
		sess.recordExtractedStream(stream)
	})
	if err != nil {
		sess.Close()
		return SessionStatus{}, err
	}
	sess.adBlockingEnabled.Store(true)

	sess.setupScreencast(ctx)

	if err := sess.Navigate(ctx, targetURL, autoTheater); err != nil {
		sess.Close()
		return SessionStatus{}, err
	}
	m.mu.Lock()
	m.session = sess
	m.mu.Unlock()

	return m.Status(), nil
}

func (s *Session) recordExtractedStream(stream shield.SniffedStream) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if s.streamSet[stream.URL] {
		return
	}
	s.streamSet[stream.URL] = true
	if stream.DiscoveredAt == "" {
		stream.DiscoveredAt = time.Now().Format("15:04:05")
	}
	s.extractedStreams = append([]shield.SniffedStream{stream}, s.extractedStreams...)
	if len(s.extractedStreams) > 20 {
		s.extractedStreams = s.extractedStreams[:20]
	}
}

// ActiveSession returns the currently running sandbox session, if any.
func (m *Manager) ActiveSession() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil && m.session.ready() == nil {
		return m.session
	}
	return nil
}

// Destroy terminates the active sandbox in milliseconds and wipes all ephemeral state.
func (m *Manager) Destroy() {
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	m.mu.Lock()
	s := m.session
	m.session = nil
	m.mu.Unlock()

	if s != nil {
		s.Close()
	}
}

func (s *Session) setupScreencast(ctx context.Context) {
	s.Browser.OnEvent(func(method string, params json.RawMessage) {
		switch method {
		case "Page.screencastFrame":
			var f struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if json.Unmarshal(params, &f) == nil && f.Data != "" {
				_ = s.Browser.CallAsync("Page.screencastFrameAck", map[string]any{
					"sessionId": f.SessionID,
				})
				s.broadcastFrame(f.Data)
			}
		case "Page.frameNavigated":
			var nav struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(params, &nav) == nil && nav.Frame.ParentID == "" {
				s.CurrentURL = nav.Frame.URL
			}
		}
	})

	_ = s.Browser.Call(ctx, "Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       80,
		"maxWidth":      1280,
		"maxHeight":     720,
		"everyNthFrame": 1,
	}, nil)

	// Only capture fallback screenshot when screencast is idle (>900ms since last frame)
	go func() {
		ticker := time.NewTicker(600 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.closeCh:
				return
			case <-ticker.C:
				last := s.lastFrameTime.Load()
				if time.Now().UnixMilli()-last < 850 {
					continue
				}
				cCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
				var snap struct {
					Data string `json:"data"`
				}
				if err := s.Browser.Call(cCtx, "Page.captureScreenshot", map[string]any{
					"format":  "jpeg",
					"quality": 78,
				}, &snap); err == nil && snap.Data != "" {
					s.broadcastFrame(snap.Data)
				}
				cancel()
			}
		}
	}()
}

func (s *Session) broadcastFrame(base64JPEG string) {
	nowMs := time.Now().UnixMilli()
	s.lastFrameTime.Store(nowMs)
	s.framesCount.Add(1)

	pkt, err := json.Marshal(FramePacket{
		Type:      "frame",
		Data:      base64JPEG,
		Timestamp: nowMs,
		URL:       s.CurrentURL,
	})
	if err != nil {
		return
	}

	s.frameListenersMu.Lock()
	s.lastFrame = pkt
	for ch := range s.frameListeners {
		select {
		case ch <- pkt:
		default:
		}
	}
	s.frameListenersMu.Unlock()

	s.webrtcMu.RLock()
	dc := s.frameDC
	s.webrtcMu.RUnlock()
	if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
		if rawBytes, err := base64.StdEncoding.DecodeString(base64JPEG); err == nil && len(rawBytes) < 60000 {
			_ = dc.Send(rawBytes)
		}
	}
}

// SubscribeFrames subscribes a listener channel to receive real-time serialized FramePackets.
func (s *Session) SubscribeFrames() (chan []byte, func()) {
	ch := make(chan []byte, 8)
	s.frameListenersMu.Lock()
	s.frameListeners[ch] = struct{}{}
	if len(s.lastFrame) > 0 {
		ch <- s.lastFrame
	}
	s.frameListenersMu.Unlock()

	unsub := func() {
		s.frameListenersMu.Lock()
		delete(s.frameListeners, ch)
		s.frameListenersMu.Unlock()
	}
	return ch, unsub
}

// validateTargetURL restricts remote source opening to absolute web URLs.
func validateTargetURL(targetURL string) error {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || strings.TrimSpace(parsed.Hostname()) != parsed.Hostname() {
		return fmt.Errorf("target must be an absolute HTTP or HTTPS URL with a host")
	}
	return nil
}

func (s *Session) ready() error {
	if s == nil || s.closed.Load() || s.Browser == nil || s.Browser.IsClosed() {
		return fmt.Errorf("vault session is closed")
	}
	if !s.adBlockingEnabled.Load() {
		return fmt.Errorf("vault protection is not ready")
	}
	return nil
}

type navigationCaller interface {
	Call(context.Context, string, any, any) error
}

func navigateBrowser(ctx context.Context, browser navigationCaller, targetURL string) error {
	if err := validateTargetURL(targetURL); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var result struct {
		ErrorText string `json:"errorText"`
	}
	if err := browser.Call(ctx, "Page.navigate", map[string]any{"url": targetURL}, &result); err != nil {
		return err
	}
	if result.ErrorText != "" {
		return fmt.Errorf("vault navigation failed: %s", result.ErrorText)
	}
	return nil
}

// Navigate directs the protected browser to a web URL and optionally isolates its player.
func (s *Session) Navigate(ctx context.Context, targetURL string, autoTheater bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := navigateBrowser(ctx, s.Browser, targetURL); err != nil {
		return err
	}
	s.CurrentURL = targetURL
	s.TheaterActive = autoTheater
	navigationSeq := s.navigationSeq.Add(1)

	_ = s.Browser.CallAsync("Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       80,
		"maxWidth":      1280,
		"maxHeight":     720,
		"everyNthFrame": 1,
	})

	if autoTheater {
		go func() {
			select {
			case <-s.closeCh:
				return
			case <-time.After(1100 * time.Millisecond):
			}
			if s.navigationSeq.Load() != navigationSeq || s.ready() != nil {
				return
			}
			tCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.Shield.IsolateTheater(tCtx, s.Browser)
		}()
	}
	return nil
}

// GoBack navigates the isolated browser back one entry in history.
func (s *Session) GoBack(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	s.navigationSeq.Add(1)
	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := s.Browser.Call(ctx, "Page.getNavigationHistory", nil, &hist); err != nil {
		return err
	}
	if hist.CurrentIndex > 0 && hist.CurrentIndex-1 < len(hist.Entries) {
		targetEntry := hist.Entries[hist.CurrentIndex-1]
		s.CurrentURL = targetEntry.URL
		return s.Browser.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": targetEntry.ID}, nil)
	}
	return nil
}

// GoForward navigates the isolated browser forward one entry in history.
func (s *Session) GoForward(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	s.navigationSeq.Add(1)
	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := s.Browser.Call(ctx, "Page.getNavigationHistory", nil, &hist); err != nil {
		return err
	}
	if hist.CurrentIndex+1 < len(hist.Entries) {
		targetEntry := hist.Entries[hist.CurrentIndex+1]
		s.CurrentURL = targetEntry.URL
		return s.Browser.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": targetEntry.ID}, nil)
	}
	return nil
}

// Reload reloads the active page inside the isolated browser.
func (s *Session) Reload(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	s.navigationSeq.Add(1)
	return s.Browser.Call(ctx, "Page.reload", nil, nil)
}

// IsolateTheater promotes the active <video> or player iframe inside the sandbox to full-screen theater mode.
func (s *Session) IsolateTheater(ctx context.Context) (map[string]any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	s.TheaterActive = true
	res, err := s.Shield.IsolateTheater(ctx, s.Browser)
	if err == nil {
		if src, ok := res["src"].(string); ok && src != "" {
			s.recordExtractedStream(shield.SniffedStream{
				URL:         src,
				Format:      shield.ClassifyMediaURL(src),
				SourceLayer: "DOM_THEATER",
			})
		}
	}
	return res, err
}

// DispatchInput translates remote viewport coordinate and key events into native Chromium input events.
func (s *Session) DispatchInput(ev InputEvent) {
	if s.ready() != nil {
		return
	}
	switch ev.Type {
	case "mousemove":
		_ = s.Browser.CallAsync("Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved",
			"x":    ev.X,
			"y":    ev.Y,
		})
	case "mousedown":
		btn := ev.Button
		if btn == "" {
			btn = "left"
		}
		clicks := ev.ClickCount
		if clicks <= 0 {
			clicks = 1
		}
		_ = s.Browser.CallAsync("Input.dispatchMouseEvent", map[string]any{
			"type":       "mousePressed",
			"x":          ev.X,
			"y":          ev.Y,
			"button":     btn,
			"clickCount": clicks,
		})
	case "mouseup":
		btn := ev.Button
		if btn == "" {
			btn = "left"
		}
		clicks := ev.ClickCount
		if clicks <= 0 {
			clicks = 1
		}
		_ = s.Browser.CallAsync("Input.dispatchMouseEvent", map[string]any{
			"type":       "mouseReleased",
			"x":          ev.X,
			"y":          ev.Y,
			"button":     btn,
			"clickCount": clicks,
		})
	case "wheel":
		_ = s.Browser.CallAsync("Input.dispatchMouseEvent", map[string]any{
			"type":   "mouseWheel",
			"x":      ev.X,
			"y":      ev.Y,
			"deltaX": ev.DeltaX,
			"deltaY": ev.DeltaY,
		})
	case "keydown":
		if ev.Key == "Backspace" || ev.Key == "Enter" || ev.Key == "Tab" || ev.Key == "Escape" || ev.Key == " " ||
			ev.Key == "ArrowLeft" || ev.Key == "ArrowRight" || ev.Key == "ArrowUp" || ev.Key == "ArrowDown" {
			_ = s.Browser.CallAsync("Input.dispatchKeyEvent", map[string]any{
				"type":                  "rawKeyDown",
				"key":                   ev.Key,
				"code":                  ev.Code,
				"windowsVirtualKeyCode": ev.KeyCode,
			})
			if ev.Key == "Enter" {
				_ = s.Browser.CallAsync("Input.dispatchKeyEvent", map[string]any{
					"type": "char",
					"text": "\r",
				})
			}
			_ = s.Browser.CallAsync("Input.dispatchKeyEvent", map[string]any{
				"type":                  "keyUp",
				"key":                   ev.Key,
				"code":                  ev.Code,
				"windowsVirtualKeyCode": ev.KeyCode,
			})
		} else if len(ev.Key) == 1 {
			_ = s.Browser.CallAsync("Input.insertText", map[string]any{
				"text": ev.Key,
			})
		}
	case "text":
		if ev.Text != "" {
			_ = s.Browser.CallAsync("Input.insertText", map[string]any{
				"text": ev.Text,
			})
		}
	}
}

// NegotiateWebRTC accepts an SDP Offer from the browser client and establishes a Pion WebRTC PeerConnection
// with DataChannels for sub-100ms input multiplexing and binary frame transport.
func (s *Session) NegotiateWebRTC(offerSDP string) (string, error) {
	if err := s.ready(); err != nil {
		return "", err
	}
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.webrtcMu.Lock()
		s.webrtcState = state.String()
		s.webrtcMu.Unlock()
	})

	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "itemba-input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				var ev InputEvent
				if json.Unmarshal(msg.Data, &ev) == nil {
					s.DispatchInput(ev)
				}
			})
		case "itemba-frames":
			dc.OnOpen(func() {
				s.webrtcMu.Lock()
				s.frameDC = dc
				s.webrtcState = "connected"
				s.webrtcMu.Unlock()
			})
		}
	})

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}
	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		_ = peerConnection.Close()
		return "", err
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		_ = peerConnection.Close()
		return "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		_ = peerConnection.Close()
		return "", err
	}

	select {
	case <-gatherComplete:
	case <-time.After(2 * time.Second):
	}

	s.webrtcMu.Lock()
	if s.peerConn != nil {
		_ = s.peerConn.Close()
	}
	s.peerConn = peerConnection
	s.webrtcState = "connected"
	s.webrtcMu.Unlock()

	return peerConnection.LocalDescription().SDP, nil
}

// Close terminates the WebRTC PeerConnection and destroys the isolated browser sandbox.
func (s *Session) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.adBlockingEnabled.Store(false)
	close(s.closeCh)

	s.webrtcMu.Lock()
	if s.peerConn != nil {
		_ = s.peerConn.Close()
		s.peerConn = nil
	}
	s.webrtcMu.Unlock()

	if s.Browser != nil {
		s.Browser.Close()
	}
}
