// Package metrics exposes the measures from section 14 of the white paper as
// Prometheus metrics. Phase 0 covers search duration, probe outcomes, Shield
// counters, Vault session state, Atlas health and console request counts.
package metrics

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

// Metrics owns a private registry so tests and processes never collide.
type Metrics struct {
	registry        *prometheus.Registry
	searchDuration  prometheus.Histogram
	searchSources   *prometheus.CounterVec
	providerResults *prometheus.CounterVec
	probeOutcomes   *prometheus.CounterVec
	httpRequests    *prometheus.CounterVec
	crawlFetches    *prometheus.CounterVec
	crawlListings   *prometheus.CounterVec
}

// New creates a registry with the Go runtime, process and search metrics.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		searchDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "itemba",
			Subsystem: "search",
			Name:      "duration_seconds",
			Help:      "Time to answer a search request.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 45, 60},
		}),
		searchSources: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "search",
			Name:      "sources_total",
			Help:      "Sources in search reports, by kind: verified, listed or pruned.",
		}, []string{"kind"}),
		providerResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "search",
			Name:      "provider_results_total",
			Help:      "Provider outcomes in search reports, by provider and status.",
		}, []string{"provider", "status"}),
		probeOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "probe",
			Name:      "outcomes_total",
			Help:      "Playback probe outcomes attached to search results, by status.",
		}, []string{"status"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Console requests, by route, method and status code.",
		}, []string{"route", "method", "code"}),
		crawlFetches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "crawler",
			Name:      "fetches_total",
			Help:      "Frontier entries the crawler processed, by kind and outcome.",
		}, []string{"kind", "outcome"}),
		crawlListings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "itemba",
			Subsystem: "crawler",
			Name:      "listings_total",
			Help:      "Listings driver searches recorded in Atlas, by driver.",
		}, []string{"driver"}),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.searchDuration,
		m.searchSources,
		m.providerResults,
		m.probeOutcomes,
		m.httpRequests,
		m.crawlFetches,
		m.crawlListings,
	)
	return m
}

// Handler serves the registry in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveSearch records one completed search report.
func (m *Metrics) ObserveSearch(report scout.SearchReport, elapsed time.Duration) {
	m.searchDuration.Observe(elapsed.Seconds())
	m.searchSources.WithLabelValues("verified").Add(float64(report.VerifiedCount))
	m.searchSources.WithLabelValues("listed").Add(float64(report.ListedCount))
	m.searchSources.WithLabelValues("pruned").Add(float64(report.PrunedCount))
	for _, provider := range report.Providers {
		m.providerResults.WithLabelValues(provider.Name, provider.Status).Inc()
	}
	for _, source := range report.Sources {
		if source.PlaybackCheck != nil && source.PlaybackCheck.Status != "" {
			m.probeOutcomes.WithLabelValues(source.PlaybackCheck.Status).Inc()
		}
	}
}

// Middleware counts requests by route, method and status code. API and
// testbench paths are a fixed set, so they are used as the route label;
// everything else is the static dashboard.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(recorder, r)
		m.httpRequests.WithLabelValues(routeLabel(r.URL.Path), r.Method, strconv.Itoa(recorder.code)).Inc()
	})
}

func routeLabel(path string) string {
	if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/testbench/") || path == "/metrics" {
		return path
	}
	return "static"
}

// statusRecorder captures the status code while passing hijacking and
// flushing through, which the WebSocket upgrade and streaming need.
type statusRecorder struct {
	http.ResponseWriter
	code        int
	headerWrote bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.headerWrote {
		r.code = code
		r.headerWrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.headerWrote = true
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	r.code = http.StatusSwitchingProtocols
	r.headerWrote = true
	return hijacker.Hijack()
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// RegisterShield reads the Shield snapshot on every scrape.
func (m *Metrics) RegisterShield(sh *shield.Shield) {
	m.registry.MustRegister(&shieldCollector{
		shield:  sh,
		blocked: prometheus.NewDesc("itemba_shield_blocked_total", "Requests Shield has blocked since start, by layer.", []string{"layer"}, nil),
		allowed: prometheus.NewDesc("itemba_shield_allowed_total", "Requests Shield has allowed since start.", nil, nil),
		rules:   prometheus.NewDesc("itemba_shield_rules", "Rules loaded in the Shield engine, by kind.", []string{"kind"}, nil),
	})
}

type shieldCollector struct {
	shield                  *shield.Shield
	blocked, allowed, rules *prometheus.Desc
}

func (c *shieldCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.blocked
	ch <- c.allowed
	ch <- c.rules
}

func (c *shieldCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.shield.Snapshot()
	ch <- prometheus.MustNewConstMetric(c.blocked, prometheus.CounterValue, float64(snapshot.Layer1DNSCount), "dns")
	ch <- prometheus.MustNewConstMetric(c.blocked, prometheus.CounterValue, float64(snapshot.Layer2CDPCount), "cdp")
	ch <- prometheus.MustNewConstMetric(c.blocked, prometheus.CounterValue, float64(snapshot.Layer3DOMCount), "dom")
	ch <- prometheus.MustNewConstMetric(c.blocked, prometheus.CounterValue, float64(snapshot.Layer3PopupCount), "popup")
	ch <- prometheus.MustNewConstMetric(c.allowed, prometheus.CounterValue, float64(snapshot.TotalAllowed))
	ch <- prometheus.MustNewConstMetric(c.rules, prometheus.GaugeValue, float64(snapshot.EngineStats.DNSSinkholeDomains), "dns_sinkhole_domains")
	ch <- prometheus.MustNewConstMetric(c.rules, prometheus.GaugeValue, float64(snapshot.EngineStats.CDPFilterRules), "cdp_filter_rules")
	ch <- prometheus.MustNewConstMetric(c.rules, prometheus.GaugeValue, float64(snapshot.EngineStats.DOMCosmeticRules), "dom_cosmetic_rules")
	ch <- prometheus.MustNewConstMetric(c.rules, prometheus.GaugeValue, float64(snapshot.EngineStats.ExceptionRules), "exception_rules")
}

// RegisterVault reads the sandbox status on every scrape.
func (m *Metrics) RegisterVault(vlt *vault.Manager) {
	m.registry.MustRegister(&vaultCollector{
		vault:  vlt,
		active: prometheus.NewDesc("itemba_vault_session_active", "1 while a sandbox session is active.", nil, nil),
		frames: prometheus.NewDesc("itemba_vault_frames_streamed_total", "Viewport frames streamed by the active session.", nil, nil),
		uptime: prometheus.NewDesc("itemba_vault_session_uptime_seconds", "Age of the active sandbox session.", nil, nil),
	})
}

type vaultCollector struct {
	vault                  *vault.Manager
	active, frames, uptime *prometheus.Desc
}

func (c *vaultCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.active
	ch <- c.frames
	ch <- c.uptime
}

func (c *vaultCollector) Collect(ch chan<- prometheus.Metric) {
	status := c.vault.Status()
	active := 0.0
	if status.Active {
		active = 1
	}
	ch <- prometheus.MustNewConstMetric(c.active, prometheus.GaugeValue, active)
	ch <- prometheus.MustNewConstMetric(c.frames, prometheus.CounterValue, float64(status.FramesStreamed))
	ch <- prometheus.MustNewConstMetric(c.uptime, prometheus.GaugeValue, float64(status.UptimeSeconds))
}

// RegisterAtlas checks the database on every scrape. A nil store reports an
// unconfigured database.
func (m *Metrics) RegisterAtlas(store *atlas.Store) {
	m.registry.MustRegister(&atlasCollector{
		store:      store,
		configured: prometheus.NewDesc("itemba_atlas_configured", "1 when a database URL is configured.", nil, nil),
		connected:  prometheus.NewDesc("itemba_atlas_connected", "1 when the last health check reached the database.", nil, nil),
		applied:    prometheus.NewDesc("itemba_atlas_migrations_applied", "Migrations applied to the database.", nil, nil),
		pending:    prometheus.NewDesc("itemba_atlas_migrations_pending", "Embedded migrations not yet applied.", nil, nil),
	})
}

type atlasCollector struct {
	store                                   *atlas.Store
	configured, connected, applied, pending *prometheus.Desc
}

func (c *atlasCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.configured
	ch <- c.connected
	ch <- c.applied
	ch <- c.pending
}

func (c *atlasCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := c.store.Health(ctx)
	ch <- prometheus.MustNewConstMetric(c.configured, prometheus.GaugeValue, boolValue(health.Configured))
	ch <- prometheus.MustNewConstMetric(c.connected, prometheus.GaugeValue, boolValue(health.Connected))
	ch <- prometheus.MustNewConstMetric(c.applied, prometheus.GaugeValue, float64(health.Migrations))
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(health.Pending))
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
