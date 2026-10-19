package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// Fetcher is fetch tier one: plain HTTP with rotating identity profiles, a
// body limit, and per-host backoff when a host starts refusing. Tiers two
// (headless rendering) and three (a Vault session) arrive later in Phase 2.
type Fetcher struct {
	client   *http.Client
	profiles []identity
	next     atomic.Uint64
	maxBody  int64

	mu        sync.Mutex
	hosts     map[string]*hostHealth
	noBackoff bool
}

type identity struct {
	userAgent      string
	acceptLanguage string
}

type hostHealth struct {
	failures     int
	backoffUntil time.Time
}

// Response is what a fetch returned.
type Response struct {
	Status      int
	Body        []byte
	ContentType string
	FinalURL    string
}

// ErrHostBackoff is returned while a host is being left alone after refusals.
var ErrHostBackoff = errors.New("host is in backoff")

const (
	defaultMaxBody   = 2 << 20
	fetchTimeout     = 12 * time.Second
	maxBackoff       = 6 * time.Hour
	backoffBaseDelay = time.Minute
)

var defaultProfiles = []identity{
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "en-US,en;q=0.9"},
	{"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0", "en-GB,en;q=0.8"},
	{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15", "en-US,en;q=0.7"},
}

// NewFetcher creates a tier-one fetcher. A nil transport uses the default.
func NewFetcher(transport http.RoundTripper) *Fetcher {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Fetcher{
		client:   &http.Client{Transport: transport, Timeout: fetchTimeout},
		profiles: defaultProfiles,
		maxBody:  defaultMaxBody,
		hosts:    map[string]*hostHealth{},
	}
}

// FetchOptions adjust one request.
type FetchOptions struct {
	// NoRedirect returns a redirect as its own 3xx response instead of
	// following it, so request headers never travel to another origin.
	NoRedirect bool
	// MaxRedirects, SamePath and Hosts bound the redirects that are followed.
	// When any is set, every hop must be an https URL at a public host: a
	// redirect to a private address or a plain-http URL is refused without
	// being fetched. SamePath refuses a hop that changes the path, so a host
	// cannot answer a movie page with its homepage; Hosts limits where a hop
	// may go. MaxRedirects defaults to 10.
	MaxRedirects int
	SamePath     bool
	Hosts        []string
	// MaxBody, when set, makes a longer body an error rather than a silent
	// truncation: a check reads bounded documents and must know when a host
	// sends more.
	MaxBody int64
}

// Get fetches a URL with the next identity profile. It returns ErrHostBackoff
// without a request while the host is in backoff, and records a refusal
// (403, 429, 503) so later calls back off exponentially up to six hours.
func (f *Fetcher) Get(ctx context.Context, rawURL string, headers map[string]string) (*Response, error) {
	return f.Fetch(ctx, rawURL, headers, FetchOptions{})
}

// Fetch is Get with options.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, headers map[string]string, opts FetchOptions) (*Response, error) {
	host := lens.Hostname(rawURL)
	if host == "" {
		return nil, fmt.Errorf("crawler: %q is not a fetchable URL", rawURL)
	}
	if until, ok := f.inBackoff(host); ok {
		return nil, fmt.Errorf("%w until %s: %s", ErrHostBackoff, until.Format(time.RFC3339), host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("crawler: %w", err)
	}
	profile := f.profiles[int(f.next.Add(1)-1)%len(f.profiles)]
	req.Header.Set("User-Agent", profile.userAgent)
	req.Header.Set("Accept-Language", profile.acceptLanguage)
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.5")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := f.client
	switch {
	case opts.NoRedirect:
		copied := *f.client
		copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &copied
	case opts.MaxRedirects > 0 || opts.SamePath || len(opts.Hosts) > 0:
		copied := *f.client
		copied.CheckRedirect = redirectPolicy(req.URL, opts)
		client = &copied
	}
	resp, err := client.Do(req)
	if err != nil {
		f.recordOutcome(host, 0)
		return nil, fmt.Errorf("crawler: fetch %s: %w", host, err)
	}
	defer resp.Body.Close()
	limit := f.maxBody
	if opts.MaxBody > 0 {
		limit = opts.MaxBody
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("crawler: read %s: %w", host, err)
	}
	if int64(len(body)) > limit {
		if opts.MaxBody > 0 {
			return nil, fmt.Errorf("crawler: %s answered with more than %d bytes", host, limit)
		}
		body = body[:limit]
	}
	f.recordOutcome(host, resp.StatusCode)
	final := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return &Response{Status: resp.StatusCode, Body: body, ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")), FinalURL: final}, nil
}

// redirectPolicy is the CheckRedirect for bounded redirects: so many hops,
// each https and public, optionally on the same path and within the hosts.
func redirectPolicy(origin *url.URL, opts FetchOptions) func(*http.Request, []*http.Request) error {
	limit := opts.MaxRedirects
	if limit <= 0 {
		limit = 10
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= limit {
			return fmt.Errorf("more than %d redirects", limit)
		}
		if req.URL.Scheme != "https" || !PublicHost(req.URL.Hostname()) {
			return errors.New("redirect to a private or unencrypted destination refused")
		}
		if opts.SamePath && req.URL.Path != origin.Path {
			return errors.New("redirect changed the page")
		}
		if len(opts.Hosts) > 0 && !hostAllowed(req.URL.Hostname(), opts.Hosts) {
			return errors.New("redirect left the host")
		}
		return nil
	}
}

// PublicHost reports whether a hostname can be a public destination: not
// localhost or a local suffix, and not a literal loopback, private,
// link-local or unspecified address. Names that resolve to private
// addresses are the transport's concern.
func PublicHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	return true
}

func hostAllowed(host string, hosts []string) bool {
	host = strings.ToLower(host)
	for _, allowed := range hosts {
		if host == strings.ToLower(allowed) {
			return true
		}
	}
	return false
}

func (f *Fetcher) inBackoff(host string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	health, ok := f.hosts[host]
	if !ok || time.Now().After(health.backoffUntil) {
		return time.Time{}, false
	}
	return health.backoffUntil, true
}

// DisableBackoff makes refusals informational only. The search burst uses
// it: one storefront answering 503 must not silence the other markets of the
// same request, while the long-lived crawler keeps backing off.
func (f *Fetcher) DisableBackoff() {
	f.mu.Lock()
	f.noBackoff = true
	f.mu.Unlock()
}

func (f *Fetcher) recordOutcome(host string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noBackoff {
		return
	}
	health := f.hosts[host]
	if health == nil {
		health = &hostHealth{}
		f.hosts[host] = health
	}
	switch {
	case status == http.StatusForbidden, status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable, status == 0:
		health.failures++
		delay := backoffBaseDelay << uint(min(health.failures-1, 12))
		if delay > maxBackoff {
			delay = maxBackoff
		}
		health.backoffUntil = time.Now().Add(delay)
	case status >= 200 && status < 400:
		health.failures = 0
		health.backoffUntil = time.Time{}
	}
}

// Backoffs reports hosts currently in backoff, for the status route.
func (f *Fetcher) Backoffs() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]time.Time{}
	now := time.Now()
	for host, health := range f.hosts {
		if health.backoffUntil.After(now) {
			out[host] = health.backoffUntil
		}
	}
	return out
}
