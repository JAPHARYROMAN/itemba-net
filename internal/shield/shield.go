package shield

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/cdp"
)

// ThreatEvent represents a single neutralized ad, tracker, clickjack overlay, or pop-up hijack.
type ThreatEvent struct {
	ID           int64       `json:"id"`
	Timestamp    string      `json:"timestamp"`
	Layer        ThreatLayer `json:"layer"`
	Category     string      `json:"category"`
	Target       string      `json:"target"`
	Rule         string      `json:"rule"`
	ListSource   string      `json:"listSource"`
	ResourceType string      `json:"resourceType,omitempty"`
}

// SniffedStream represents a playable media manifest or direct video stream discovered inside a page.
type SniffedStream struct {
	URL          string `json:"url"`
	Format       string `json:"format"` // "HLS (.m3u8)", "DASH (.mpd)", "MP4 Video", "WebM Video", "HTML5 <video>"
	Resolution   string `json:"resolution,omitempty"`
	DiscoveredAt string `json:"discoveredAt"`
	SourceLayer  string `json:"sourceLayer"` // "CDP_NETWORK" or "DOM_VIDEO"
}

// TelemetrySnapshot holds the aggregated counts, engine stats, sniffed streams, and recent threat log.
type TelemetrySnapshot struct {
	TotalBlocked     int64           `json:"totalBlocked"`
	TotalAllowed     int64           `json:"totalAllowed"`
	Layer1DNSCount   int64           `json:"layer1DnsCount"`
	Layer2CDPCount   int64           `json:"layer2CdpCount"`
	Layer3DOMCount   int64           `json:"layer3DomCount"`
	Layer3PopupCount int64           `json:"layer3PopupCount"`
	EngineStats      EngineStats     `json:"engineStats"`
	SniffedStreams   []SniffedStream `json:"sniffedStreams"`
	RecentEvents     []ThreatEvent   `json:"recentEvents"`
}

// Shield coordinates all three protection layers and media stream sniffing across active sandbox sessions.
type Shield struct {
	engine *Engine

	nextID       atomic.Int64
	allowedCount atomic.Int64
	l1Count      atomic.Int64
	l2Count      atomic.Int64
	l3DomCount   atomic.Int64
	l3PopCount   atomic.Int64

	eventsMu sync.RWMutex
	events   []ThreatEvent

	streamsMu sync.RWMutex
	streams   []SniffedStream
	streamSet map[string]bool

	subMu       sync.RWMutex
	subscribers map[chan ThreatEvent]struct{}
}

// New creates a new Triple-Layer Shield controller.
func New() *Shield {
	return &Shield{
		engine:      NewEngine(),
		events:      make([]ThreatEvent, 0, 250),
		streams:     make([]SniffedStream, 0, 32),
		streamSet:   make(map[string]bool),
		subscribers: make(map[chan ThreatEvent]struct{}),
	}
}

// Engine returns the underlying rule compiler & matcher.
func (s *Shield) Engine() *Engine {
	return s.engine
}

// Subscribe registers a channel to receive live ThreatEvents as they are intercepted.
func (s *Shield) Subscribe() (chan ThreatEvent, func()) {
	ch := make(chan ThreatEvent, 64)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()

	unsubscribe := func() {
		s.subMu.Lock()
		delete(s.subscribers, ch)
		s.subMu.Unlock()
	}
	return ch, unsubscribe
}

// ClearStreams resets the sniffed media streams for a new vault navigation.
func (s *Shield) ClearStreams() {
	s.streamsMu.Lock()
	s.streams = make([]SniffedStream, 0, 32)
	s.streamSet = make(map[string]bool)
	s.streamsMu.Unlock()
}

// RecordStream logs a discovered media manifest or direct video stream URL.
func (s *Shield) RecordStream(rawURL, format, resolution, sourceLayer string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.HasPrefix(rawURL, "blob:") || strings.HasPrefix(rawURL, "data:") {
		return
	}
	if format == "" {
		format = ClassifyMediaURL(rawURL)
	}

	s.streamsMu.Lock()
	if s.streamSet[rawURL] {
		s.streamsMu.Unlock()
		return
	}
	s.streamSet[rawURL] = true
	entry := SniffedStream{
		URL:          rawURL,
		Format:       format,
		Resolution:   resolution,
		DiscoveredAt: time.Now().Format("15:04:05.000"),
		SourceLayer:  sourceLayer,
	}
	s.streams = append([]SniffedStream{entry}, s.streams...)
	if len(s.streams) > 30 {
		s.streams = s.streams[:30]
	}
	s.streamsMu.Unlock()
}

// ClassifyMediaURL determines the human-readable format for a media URL.
func ClassifyMediaURL(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, ".m3u8"):
		return "HLS (.m3u8)"
	case strings.Contains(lower, ".mpd"):
		return "DASH (.mpd)"
	case strings.Contains(lower, ".mp4"):
		return "Direct MP4"
	case strings.Contains(lower, ".webm"):
		return "Direct WebM"
	case strings.Contains(lower, ".ogv"):
		return "Direct Ogg Video"
	default:
		return "HTML5 <video>"
	}
}

// Snapshot returns the current counters, engine stats, sniffed streams, and recent threat log.
func (s *Shield) Snapshot() TelemetrySnapshot {
	l1 := s.l1Count.Load()
	l2 := s.l2Count.Load()
	l3d := s.l3DomCount.Load()
	l3p := s.l3PopCount.Load()

	s.eventsMu.RLock()
	recent := make([]ThreatEvent, len(s.events))
	copy(recent, s.events)
	s.eventsMu.RUnlock()

	s.streamsMu.RLock()
	streams := make([]SniffedStream, len(s.streams))
	copy(streams, s.streams)
	s.streamsMu.RUnlock()

	return TelemetrySnapshot{
		TotalBlocked:     l1 + l2 + l3d + l3p,
		TotalAllowed:     s.allowedCount.Load(),
		Layer1DNSCount:   l1,
		Layer2CDPCount:   l2,
		Layer3DOMCount:   l3d,
		Layer3PopupCount: l3p,
		EngineStats:      s.engine.Stats(),
		SniffedStreams:   streams,
		RecentEvents:     recent,
	}
}

// RecordEvent logs a neutralized threat and broadcasts it to all UI telemetry subscribers.
func (s *Shield) RecordEvent(ev ThreatEvent) {
	ev.ID = s.nextID.Add(1)
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().Format("15:04:05.000")
	}

	switch ev.Layer {
	case Layer1DNS:
		s.l1Count.Add(1)
	case Layer2CDP:
		s.l2Count.Add(1)
	case Layer3DOM:
		s.l3DomCount.Add(1)
	case Layer3Popup:
		s.l3PopCount.Add(1)
	}

	s.eventsMu.Lock()
	s.events = append([]ThreatEvent{ev}, s.events...)
	if len(s.events) > 150 {
		s.events = s.events[:150]
	}
	s.eventsMu.Unlock()

	s.subMu.RLock()
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
	s.subMu.RUnlock()
}

type browserController interface {
	Call(context.Context, string, any, any) error
	CallAsync(string, any) error
	OnEvent(cdp.EventHandler)
}

// Attach confirms every protection command before allowing callers to navigate.
func (s *Shield) Attach(ctx context.Context, browser browserController, onThreat func(ThreatEvent), onStream func(SniffedStream)) error {

	browser.OnEvent(func(method string, params json.RawMessage) {
		switch method {
		case "Fetch.requestPaused":
			var p struct {
				RequestID    string `json:"requestId"`
				ResourceType string `json:"resourceType"`
				Request      struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return
			}

			match := s.engine.Inspect(p.Request.URL, p.ResourceType)
			if match.Blocked {
				_ = browser.CallAsync("Fetch.failRequest", map[string]any{
					"requestId":   p.RequestID,
					"errorReason": "BlockedByClient",
				})
				ev := ThreatEvent{
					Layer:        match.Layer,
					Category:     match.Category,
					Target:       p.Request.URL,
					Rule:         match.Rule,
					ListSource:   match.ListSource,
					ResourceType: p.ResourceType,
				}
				s.RecordEvent(ev)
				if onThreat != nil {
					onThreat(ev)
				}
				return
			}

			s.allowedCount.Add(1)

			// Check if this allowed request is a playable media manifest or video stream
			lowerU := strings.ToLower(p.Request.URL)
			if p.ResourceType == "Media" ||
				strings.Contains(lowerU, ".m3u8") ||
				strings.Contains(lowerU, ".mpd") ||
				strings.Contains(lowerU, ".mp4") ||
				strings.Contains(lowerU, ".webm") ||
				strings.Contains(lowerU, ".ogv") {
				fmtName := ClassifyMediaURL(p.Request.URL)
				s.RecordStream(p.Request.URL, fmtName, "", "CDP_NETWORK")
				if onStream != nil {
					onStream(SniffedStream{
						URL:         p.Request.URL,
						Format:      fmtName,
						SourceLayer: "CDP_NETWORK",
					})
				}
			}

			_ = browser.CallAsync("Fetch.continueRequest", map[string]any{
				"requestId": p.RequestID,
			})

		case "Runtime.bindingCalled":
			var b struct {
				Name    string `json:"name"`
				Payload string `json:"payload"`
			}
			if err := json.Unmarshal(params, &b); err != nil {
				return
			}

			if b.Name == "__itembaShieldReport" {
				var domReport struct {
					Layer    ThreatLayer `json:"layer"`
					Category string      `json:"category"`
					Target   string      `json:"target"`
					Rule     string      `json:"rule"`
				}
				if err := json.Unmarshal([]byte(b.Payload), &domReport); err != nil {
					return
				}

				listSource := "itemba-shield DOM Sweeper"
				if domReport.Layer == Layer3Popup {
					listSource = "itemba-shield Window Lock"
				}

				ev := ThreatEvent{
					Layer:        domReport.Layer,
					Category:     domReport.Category,
					Target:       domReport.Target,
					Rule:         domReport.Rule,
					ListSource:   listSource,
					ResourceType: "DOM",
				}
				s.RecordEvent(ev)
				if onThreat != nil {
					onThreat(ev)
				}
			} else if b.Name == "__itembaMediaReport" {
				var mediaReport struct {
					URL        string `json:"url"`
					Format     string `json:"format"`
					Resolution string `json:"resolution"`
				}
				if err := json.Unmarshal([]byte(b.Payload), &mediaReport); err == nil && mediaReport.URL != "" {
					fmtName := ClassifyMediaURL(mediaReport.URL)
					s.RecordStream(mediaReport.URL, fmtName, mediaReport.Resolution, "DOM_VIDEO")
					if onStream != nil {
						onStream(SniffedStream{
							URL:         mediaReport.URL,
							Format:      fmtName,
							Resolution:  mediaReport.Resolution,
							SourceLayer: "DOM_VIDEO",
						})
					}
				}
			}
		}
	})

	// Install the listener before enabling interception, so the first paused
	// request always has a handler. A failed setup must never authorize navigation.
	commands := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Network.enable", nil},
		{"Runtime.addBinding", map[string]any{"name": "__itembaShieldReport"}},
		{"Runtime.addBinding", map[string]any{"name": "__itembaMediaReport"}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": PreInjectedShieldScript}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*"}}}},
	}
	for _, command := range commands {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("shield setup canceled: %w", err)
		}
		if err := browser.Call(ctx, command.method, command.params, nil); err != nil {
			return fmt.Errorf("shield setup %s: %w", command.method, err)
		}
	}
	return nil
}

// IsolateTheater executes the Layer 3 Theater Mode isolator inside the active browser instance.
func (s *Shield) IsolateTheater(ctx context.Context, browser *cdp.BrowserInstance) (map[string]any, error) {
	var evalResp struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
	}
	err := browser.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    IsolateTheaterScript,
		"returnByValue": true,
	}, &evalResp)
	if err != nil {
		return nil, fmt.Errorf("failed to execute theater isolator: %w", err)
	}
	return evalResp.Result.Value, nil
}
