package shield

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ThreatLayer identifies which of the 3 shield layers neutralized a threat.
type ThreatLayer string

const (
	Layer1DNS   ThreatLayer = "L1_DNS_SINKHOLE"
	Layer2CDP   ThreatLayer = "L2_CDP_FILTER"
	Layer3DOM   ThreatLayer = "L3_DOM_SWEEPER"
	Layer3Popup ThreatLayer = "L3_POPUP_GUILLOTINE"
)

// RuleMatch describes why a request or domain was blocked or allowed.
type RuleMatch struct {
	Blocked    bool        `json:"blocked"`
	Layer      ThreatLayer `json:"layer,omitempty"`
	Rule       string      `json:"rule,omitempty"`
	ListSource string      `json:"listSource,omitempty"`
	Category   string      `json:"category,omitempty"`
}

// EngineStats summarizes compiled filter rule counts across all layers.
type EngineStats struct {
	DNSSinkholeDomains int      `json:"dnsSinkholeDomains"`
	CDPFilterRules     int      `json:"cdpFilterRules"`
	DOMCosmeticRules   int      `json:"domCosmeticRules"`
	ExceptionRules     int      `json:"exceptionRules"`
	ActiveBlocklists   []string `json:"activeBlocklists"`
	LastSyncedAt       string   `json:"lastSyncedAt"`
}

type dnsRule struct {
	Domain     string
	ListSource string
	Category   string
}

type filterPattern struct {
	RawRule    string
	Substring  string
	ListSource string
	Category   string
	Custom     bool
}

// Engine holds the Layer 1 DNS Sinkhole domains, Layer 2 ABP/uBlock filter rules, and exception rules.
type Engine struct {
	mu           sync.RWMutex
	dnsDomains   map[string]dnsRule
	urlPatterns  []filterPattern
	exceptions   map[string]bool
	cosmeticSels []string
	lastSyncedAt string
	revision     uint64
}

// NewEngine initializes the Triple-Layer Shield rule compiler & matcher.
func NewEngine() *Engine {
	e := &Engine{
		dnsDomains: make(map[string]dnsRule, 512),
		exceptions: make(map[string]bool),
		cosmeticSels: []string{
			"[id*=\"google_ads\"]",
			"[class*=\"ad-overlay\"]",
			"[class*=\"clickjack\"]",
			"[id*=\"clickjack\"]",
			"[class*=\"popunder\"]",
			"[id*=\"popunder\"]",
			"[data-ad-slot]",
			".ad-banner",
			".preroll-overlay",
			".fake-play-ad",
			"iframe[src*=\"doubleclick\"]",
			"iframe[src*=\"exoclick\"]",
			"iframe[src*=\"adsterra\"]",
		},
		lastSyncedAt: time.Now().Format("15:04:05 UTC"),
	}
	e.loadDefaultDNSBlocklist()
	e.loadDefaultURLFilters()
	e.revision = 1
	return e
}

// Revision identifies this engine's current compiled rules. It advances under
// the same lock as rule mutations, so work begun with an earlier revision must
// not publish a reusable protection result after the rules change. Telemetry
// reads and list-sync timestamps alone do not change the revision.
func (e *Engine) Revision() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.revision
}

func (e *Engine) loadDefaultDNSBlocklist() {
	entries := []dnsRule{
		// OISD & StevenBlack Ad Networks
		{Domain: "doubleclick.net", ListSource: "OISD / StevenBlack", Category: "Ad Network"},
		{Domain: "googlesyndication.com", ListSource: "OISD / StevenBlack", Category: "Ad Network"},
		{Domain: "googleadservices.com", ListSource: "OISD / StevenBlack", Category: "Ad Network"},
		{Domain: "adservice.google.com", ListSource: "OISD / StevenBlack", Category: "Ad Network"},
		{Domain: "2mdn.net", ListSource: "OISD / StevenBlack", Category: "Ad Media CDN"},
		{Domain: "adnxs.com", ListSource: "OISD / HaGeZi Pro++", Category: "Ad Exchange"},
		{Domain: "adsrvr.org", ListSource: "OISD / HaGeZi Pro++", Category: "Ad Exchange"},
		{Domain: "rubiconproject.com", ListSource: "OISD / StevenBlack", Category: "Ad Exchange"},
		{Domain: "pubmatic.com", ListSource: "OISD / StevenBlack", Category: "Ad Exchange"},
		{Domain: "openx.net", ListSource: "OISD / StevenBlack", Category: "Ad Exchange"},
		{Domain: "criteo.com", ListSource: "OISD / StevenBlack", Category: "Retargeting Ad"},
		{Domain: "criteo.net", ListSource: "OISD / StevenBlack", Category: "Retargeting Ad"},
		{Domain: "casalemedia.com", ListSource: "OISD / StevenBlack", Category: "Ad Exchange"},
		{Domain: "smartadserver.com", ListSource: "OISD / StevenBlack", Category: "Ad Server"},
		{Domain: "advertising.com", ListSource: "OISD / StevenBlack", Category: "Ad Network"},
		{Domain: "taboola.com", ListSource: "OISD / HaGeZi Pro++", Category: "Sponsored Chumbox"},
		{Domain: "outbrain.com", ListSource: "OISD / HaGeZi Pro++", Category: "Sponsored Chumbox"},
		{Domain: "mgid.com", ListSource: "HaGeZi Pro++", Category: "Malvertising / Chumbox"},
		{Domain: "revcontent.com", ListSource: "HaGeZi Pro++", Category: "Sponsored Chumbox"},
		{Domain: "zergnet.com", ListSource: "HaGeZi Pro++", Category: "Chumbox Network"},
		{Domain: "adroll.com", ListSource: "OISD", Category: "Ad Tracker"},
		{Domain: "amazon-adsystem.com", ListSource: "OISD", Category: "Ad Network"},
		{Domain: "moatads.com", ListSource: "EasyPrivacy", Category: "Ad Verification Tracker"},
		{Domain: "adsafeprotected.com", ListSource: "EasyPrivacy", Category: "Ad Verification Tracker"},
		{Domain: "doubleverify.com", ListSource: "EasyPrivacy", Category: "Ad Telemetry"},
		{Domain: "serving-sys.com", ListSource: "OISD", Category: "Ad Server"},
		{Domain: "flashtalking.com", ListSource: "OISD", Category: "Ad Server"},
		{Domain: "adform.net", ListSource: "OISD", Category: "Ad Network"},
		{Domain: "bidswitch.net", ListSource: "OISD", Category: "RTB Exchange"},
		{Domain: "contextweb.com", ListSource: "OISD", Category: "RTB Exchange"},
		{Domain: "indexww.com", ListSource: "OISD", Category: "Header Bidding"},
		{Domain: "lijit.com", ListSource: "OISD", Category: "Sovrn Ad Exchange"},
		{Domain: "Media.net", ListSource: "OISD", Category: "Contextual Ads"},
		{Domain: "sharethrough.com", ListSource: "OISD", Category: "Native Ad Exchange"},
		{Domain: "Spotxchange.com", ListSource: "EasyList Video", Category: "Video Ad Exchange"},
		{Domain: "springserve.com", ListSource: "EasyList Video", Category: "VAST Video Ad Server"},
		{Domain: "teads.tv", ListSource: "EasyList Video", Category: "Outstream Video Ads"},
		{Domain: "innovid.com", ListSource: "EasyList Video", Category: "Interactive Video Ads"},
		{Domain: "fwmrm.net", ListSource: "EasyList Video", Category: "FreeWheel Video Ads"},

		// Aggressive Streaming Site Pop-Up / Malvertising Networks (HaGeZi Pro++ / uBlock Badware)
		{Domain: "popads.net", ListSource: "HaGeZi Pro++ / uBlock", Category: "Pop-Under Network"},
		{Domain: "popcash.net", ListSource: "HaGeZi Pro++ / uBlock", Category: "Pop-Under Network"},
		{Domain: "propellerads.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Clickjack / Push Ad"},
		{Domain: "exoclick.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Streaming Ad Network"},
		{Domain: "exosrv.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Streaming Ad Network"},
		{Domain: "juicyads.com", ListSource: "HaGeZi Pro++", Category: "Aggressive Ad Network"},
		{Domain: "trafficjunky.net", ListSource: "HaGeZi Pro++", Category: "Aggressive Ad Network"},
		{Domain: "adsterra.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Pop-Under / Malvertising"},
		{Domain: "hilltopads.net", ListSource: "HaGeZi Pro++", Category: "Streaming Pop-Under"},
		{Domain: "clickadu.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Clickjack Ad Network"},
		{Domain: "bidvertiser.com", ListSource: "HaGeZi Pro++", Category: "Malvertising"},
		{Domain: "adcash.com", ListSource: "HaGeZi Pro++", Category: "Pop-Under Network"},
		{Domain: "richpush.co", ListSource: "HaGeZi Pro++", Category: "Push Notification Spam"},
		{Domain: "galaksion.com", ListSource: "HaGeZi Pro++", Category: "Overlay Ad Network"},
		{Domain: "adhitz.com", ListSource: "HaGeZi Pro++", Category: "Ad Network"},
		{Domain: "admaven.com", ListSource: "HaGeZi Pro++ / uBlock", Category: "Pop-Under / Tab-Under"},
		{Domain: "evadav.com", ListSource: "HaGeZi Pro++", Category: "Push / Pop-Under"},
		{Domain: "mondiad.com", ListSource: "HaGeZi Pro++", Category: "Pop-Under Network"},
		{Domain: "trafficstars.com", ListSource: "HaGeZi Pro++", Category: "Streaming Ad Server"},
		{Domain: "plugrush.com", ListSource: "HaGeZi Pro++", Category: "Redirect Ad Network"},
		{Domain: "zeroredirect.com", ListSource: "uBlock Badware", Category: "Domain Parking Redirect"},

		// Telemetry & Behavioral Fingerprinting (EasyPrivacy / HaGeZi)
		{Domain: "scorecardresearch.com", ListSource: "EasyPrivacy", Category: "Behavioral Telemetry"},
		{Domain: "quantserve.com", ListSource: "EasyPrivacy", Category: "Audience Tracker"},
		{Domain: "hotjar.com", ListSource: "EasyPrivacy", Category: "Session Replay Tracker"},
		{Domain: "mouseflow.com", ListSource: "EasyPrivacy", Category: "Session Replay Tracker"},
		{Domain: "fullstory.com", ListSource: "EasyPrivacy", Category: "Session Replay Tracker"},
		{Domain: "logrocket.io", ListSource: "EasyPrivacy", Category: "Session Replay Tracker"},
		{Domain: "clarity.ms", ListSource: "EasyPrivacy", Category: "Session Replay Tracker"},
		{Domain: "newrelic.com", ListSource: "EasyPrivacy", Category: "Telemetry Beacon"},
		{Domain: "nr-data.net", ListSource: "EasyPrivacy", Category: "Telemetry Beacon"},
		{Domain: "mc.yandex.ru", ListSource: "EasyPrivacy", Category: "Cross-Site Tracker"},
		{Domain: "connect.facebook.net", ListSource: "EasyPrivacy", Category: "Social Pixel Tracker"},
		{Domain: "analytics.tiktok.com", ListSource: "EasyPrivacy", Category: "Social Pixel Tracker"},
		{Domain: "bat.bing.com", ListSource: "EasyPrivacy", Category: "Conversion Tracker"},
		{Domain: "omtrdc.net", ListSource: "EasyPrivacy", Category: "Adobe Analytics Tracker"},
		{Domain: "demdex.net", ListSource: "EasyPrivacy", Category: "Adobe Audience DMP"},
		{Domain: "krxd.net", ListSource: "EasyPrivacy", Category: "Salesforce Krux DMP"},
		{Domain: "bluekai.com", ListSource: "EasyPrivacy", Category: "Oracle Data Cloud DMP"},
		{Domain: "tapad.com", ListSource: "EasyPrivacy", Category: "Cross-Device Graph Tracker"},
		{Domain: "amplitude.com", ListSource: "EasyPrivacy", Category: "Product Telemetry"},
		{Domain: "mixpanel.com", ListSource: "EasyPrivacy", Category: "Event Telemetry"},
		{Domain: "segment.io", ListSource: "EasyPrivacy", Category: "Telemetry Pipeline"},
	}

	for _, entry := range entries {
		d := strings.ToLower(entry.Domain)
		e.dnsDomains[d] = dnsRule{
			Domain:     d,
			ListSource: entry.ListSource,
			Category:   entry.Category,
		}
	}
}

func (e *Engine) loadDefaultURLFilters() {
	e.urlPatterns = []filterPattern{
		{RawRule: "||*/ads/preroll*", Substring: "/ads/preroll", ListSource: "EasyList / uBlock", Category: "Video Pre-Roll Ad"},
		{RawRule: "||*/ads/midroll*", Substring: "/ads/midroll", ListSource: "EasyList / uBlock", Category: "Video Mid-Roll Ad"},
		{RawRule: "||*/ads/popunder*", Substring: "/ads/popunder", ListSource: "uBlock Origin", Category: "Pop-Under Script"},
		{RawRule: "||*/ads/clickjack*", Substring: "/ads/clickjack", ListSource: "uBlock Origin", Category: "Clickjacking Script"},
		{RawRule: "||*/ads/banner*", Substring: "/ads/banner", ListSource: "EasyList", Category: "Display Ad Banner"},
		{RawRule: "||*/adserver/*", Substring: "/adserver/", ListSource: "EasyList", Category: "First-Party Ad Server"},
		{RawRule: "||*/pagead/*", Substring: "/pagead/", ListSource: "EasyList", Category: "Ad Delivery Script"},
		{RawRule: "||*/vast.xml*", Substring: "/vast.xml", ListSource: "EasyList Video", Category: "VAST Video Ad Tag"},
		{RawRule: "||*ad_tag=*", Substring: "ad_tag=", ListSource: "EasyList Video", Category: "VAST Video Ad Parameter"},
		{RawRule: "||*preroll_ad*", Substring: "preroll_ad", ListSource: "EasyList Video", Category: "Video Pre-Roll Ad"},
		{RawRule: "||*/popunder.js*", Substring: "/popunder.js", ListSource: "uBlock Badware", Category: "Pop-Under Hijacker"},
		{RawRule: "||*/tabunder.js*", Substring: "/tabunder.js", ListSource: "uBlock Badware", Category: "Tab-Under Hijacker"},
		{RawRule: "||*/clickunder.js*", Substring: "/clickunder.js", ListSource: "uBlock Badware", Category: "Click-Under Script"},
		{RawRule: "||*/adframe.*", Substring: "/adframe.", ListSource: "EasyList", Category: "Ad Iframe"},
		{RawRule: "||*/sponsor_beacon*", Substring: "/sponsor_beacon", ListSource: "EasyPrivacy", Category: "Sponsor Tracking Beacon"},
		{RawRule: "||*/telemetry/pixel*", Substring: "/telemetry/pixel", ListSource: "EasyPrivacy", Category: "Tracking Pixel"},
		{RawRule: "||*/tracker/collect*", Substring: "/tracker/collect", ListSource: "EasyPrivacy", Category: "Analytics Collector"},
		{RawRule: "||*/fingerprint.js*", Substring: "/fingerprint.js", ListSource: "EasyPrivacy", Category: "Canvas Fingerprinter"},
		{RawRule: "||*popads_*", Substring: "popads_", ListSource: "uBlock Origin", Category: "Pop-Up Script"},
		{RawRule: "||*exoclick_*", Substring: "exoclick_", ListSource: "uBlock Origin", Category: "Streaming Ad Script"},
		{RawRule: "||*/ima3.js*", Substring: "/ima3.js", ListSource: "EasyList Video", Category: "Google IMA Video Ad SDK"},
		{RawRule: "||*/gpt.js*", Substring: "/gpt.js", ListSource: "EasyList", Category: "Google Publisher Tag"},
		{RawRule: "||*/prebid*.js*", Substring: "/prebid", ListSource: "EasyList", Category: "Header Bidding Script"},
	}
}

// Stats returns the current rule counts and blocklist metadata.
func (e *Engine) Stats() EngineStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return EngineStats{
		DNSSinkholeDomains: len(e.dnsDomains),
		CDPFilterRules:     len(e.urlPatterns),
		DOMCosmeticRules:   len(e.cosmeticSels),
		ExceptionRules:     len(e.exceptions),
		ActiveBlocklists: []string{
			"OISD Big (DNS Sinkhole)",
			"HaGeZi Multi Pro++",
			"StevenBlack Unified Hosts",
			"EasyList & EasyList Video (VAST/IMA)",
			"EasyPrivacy Telemetry & Replay",
			"uBlock Origin Badware & Annoyances",
		},
		LastSyncedAt: e.lastSyncedAt,
	}
}

// AddCustomRule parses standard ABP/uBlock syntax (||domain^, @@||domain^, ##selector, or hosts) and compiles it into the live engine.
func (e *Engine) AddCustomRule(raw string) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Exception rule: @@||domain.com^
	if strings.HasPrefix(line, "@@") {
		clean := strings.TrimPrefix(line, "@@")
		clean = strings.TrimPrefix(clean, "||")
		clean = strings.TrimSuffix(clean, "^")
		key := strings.ToLower(clean)
		if !e.exceptions[key] {
			e.revision++
		}
		e.exceptions[key] = true
		return
	}

	// Cosmetic rule: ##.selector
	if idx := strings.Index(line, "##"); idx != -1 {
		sel := strings.TrimSpace(line[idx+2:])
		if sel != "" {
			e.cosmeticSels = append(e.cosmeticSels, sel)
			e.revision++
		}
		return
	}

	// Hosts format: 0.0.0.0 domain.com or 127.0.0.1 domain.com
	if strings.HasPrefix(line, "0.0.0.0 ") || strings.HasPrefix(line, "127.0.0.1 ") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			d := strings.ToLower(fields[1])
			if d != "localhost" && d != "0.0.0.0" {
				rule := dnsRule{
					Domain:     d,
					ListSource: "Custom Hosts Sinkhole",
					Category:   "Custom DNS Rule",
				}
				if previous, exists := e.dnsDomains[d]; !exists || previous != rule {
					e.revision++
				}
				e.dnsDomains[d] = rule
			}
		}
		return
	}

	// ABP domain anchor: ||domain.com^
	if strings.HasPrefix(line, "||") {
		body := strings.TrimPrefix(line, "||")
		if dollarIdx := strings.Index(body, "$"); dollarIdx != -1 {
			body = body[:dollarIdx]
		}
		body = strings.TrimSuffix(body, "^")
		body = strings.ToLower(strings.TrimSpace(body))
		if !strings.Contains(body, "/") && !strings.Contains(body, "*") && strings.Contains(body, ".") {
			rule := dnsRule{
				Domain:     body,
				ListSource: "Custom ABP Domain Rule",
				Category:   "Custom DNS Sinkhole",
			}
			if previous, exists := e.dnsDomains[body]; !exists || previous != rule {
				e.revision++
			}
			e.dnsDomains[body] = rule
			return
		}
		e.urlPatterns = append(e.urlPatterns, filterPattern{
			RawRule:    raw,
			Substring:  body,
			ListSource: "Custom ABP Filter",
			Category:   "Custom CDP Rule",
			Custom:     true,
		})
		e.revision++
		return
	}

	lower := strings.ToLower(line)
	if !strings.Contains(lower, "/") && strings.Contains(lower, ".") {
		rule := dnsRule{
			Domain:     lower,
			ListSource: "Custom Domain Blocklist",
			Category:   "Custom DNS Sinkhole",
		}
		if previous, exists := e.dnsDomains[lower]; !exists || previous != rule {
			e.revision++
		}
		e.dnsDomains[lower] = rule
	} else {
		e.urlPatterns = append(e.urlPatterns, filterPattern{
			RawRule:    raw,
			Substring:  lower,
			ListSource: "Custom URL Filter",
			Category:   "Custom CDP Rule",
			Custom:     true,
		})
		e.revision++
	}
}

// SyncRemoteList fetches a live hosts/ABP blocklist over HTTPS and compiles additional domains into Layer 1.
func (e *Engine) SyncRemoteList(ctx context.Context) (int, error) {
	// Fetch Peter Lowe's curated ad/tracker hosts list (fast, standard, reliable)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://pgl.yoyo.org/adservers/serverlist.php?hostformat=hosts&showintro=0&mimetype=plaintext", nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	added := 0
	scanner := bufio.NewScanner(resp.Body)
	e.mu.Lock()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "127.0.0.1 ") || strings.HasPrefix(line, "0.0.0.0 ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				d := strings.ToLower(parts[1])
				if d != "localhost" && d != "127.0.0.1" {
					if _, exists := e.dnsDomains[d]; !exists {
						e.dnsDomains[d] = dnsRule{
							Domain:     d,
							ListSource: "Peter Lowe / OISD Live Sync",
							Category:   "Ad / Tracker Server",
						}
						added++
					}
				}
			}
		}
	}
	e.lastSyncedAt = time.Now().Format("15:04:05 UTC")
	if added > 0 {
		e.revision++
	}
	e.mu.Unlock()
	return added, nil
}

// HostResolverRules builds Chromium's --host-resolver-rules string for the core high-frequency ad domains.
func (e *Engine) HostResolverRules() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var rules []string
	count := 0
	for domain := range e.dnsDomains {
		rules = append(rules, "MAP "+domain+" 0.0.0.0", "MAP *."+domain+" 0.0.0.0")
		count++
		if count >= 85 {
			// Keep command-line argument length well within Windows CreateProcess limits;
			// all remaining domains are still enforced at the CDP Fetch pre-request layer!
			break
		}
	}
	return strings.Join(rules, ", ")
}

// Inspect evaluates a candidate request URL and resource type against Layer 1 (DNS Sinkhole) and Layer 2 (CDP Filter Engine).
func (e *Engine) Inspect(rawURL string, resourceType string) RuleMatch {
	lowerURL := strings.ToLower(strings.TrimSpace(rawURL))
	if lowerURL == "" || strings.HasPrefix(lowerURL, "data:") || strings.HasPrefix(lowerURL, "blob:") {
		return RuleMatch{Blocked: false}
	}

	e.mu.RLock()
	for exc := range e.exceptions {
		if strings.Contains(lowerURL, exc) {
			e.mu.RUnlock()
			return RuleMatch{Blocked: false}
		}
	}
	e.mu.RUnlock()

	parsed, err := url.Parse(lowerURL)
	if err == nil && parsed.Hostname() != "" {
		host := parsed.Hostname()
		e.mu.RLock()
		// Fast O(1) domain + parent suffix lookup
		parts := strings.Split(host, ".")
		for i := 0; i < len(parts)-1; i++ {
			cand := strings.Join(parts[i:], ".")
			if rule, ok := e.dnsDomains[cand]; ok {
				e.mu.RUnlock()
				return RuleMatch{
					Blocked:    true,
					Layer:      Layer1DNS,
					Rule:       "0.0.0.0 " + rule.Domain,
					ListSource: rule.ListSource,
					Category:   rule.Category,
				}
			}
		}
		e.mu.RUnlock()
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	// Explicit user URL rules apply to media too. File extensions cannot
	// override a domain sinkhole or a deliberately blocked movie/ad URL.
	for _, pat := range e.urlPatterns {
		if pat.Custom && strings.Contains(lowerURL, pat.Substring) {
			return RuleMatch{Blocked: true, Layer: Layer2CDP, Rule: pat.RawRule, ListSource: pat.ListSource, Category: pat.Category}
		}
	}

	// Preserve the existing content exemption from broad default URL
	// heuristics, after enforcing explicit domain and custom URL blocks.
	if isProtectedMediaStream(lowerURL) &&
		!strings.Contains(lowerURL, "/ads/") &&
		!strings.Contains(lowerURL, "preroll") &&
		!strings.Contains(lowerURL, "doubleclick") &&
		!strings.Contains(lowerURL, "googlesyndication") {
		return RuleMatch{Blocked: false}
	}

	// Layer 2: Check URL path & query against EasyList / EasyPrivacy / uBlock patterns
	for _, pat := range e.urlPatterns {
		if strings.Contains(lowerURL, pat.Substring) {
			return RuleMatch{
				Blocked:    true,
				Layer:      Layer2CDP,
				Rule:       pat.RawRule,
				ListSource: pat.ListSource,
				Category:   pat.Category,
			}
		}
	}

	return RuleMatch{Blocked: false}
}

func isProtectedMediaStream(u string) bool {
	for _, ext := range []string{".m3u8", ".mpd", ".ts", ".m4s", ".mp4", ".webm", ".ogv", ".mkv"} {
		if strings.Contains(u, ext) {
			return true
		}
	}
	return false
}
