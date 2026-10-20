package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/shield"
)

// CandidateSource represents a seed media source discovered by federated search or direct URL input.
type CandidateSource struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Kind is the object type the source holds, video unless a driver of
	// another kind listed it; Creator is the performer, author or maker the
	// host credits, part of a recording's identity.
	Kind                    string   `json:"kind,omitempty"`
	Creator                 string   `json:"creator,omitempty"`
	URL                     string   `json:"url"`             // Target web page or embed URL for RBI Vault
	DirectStreamURL         string   `json:"directStreamUrl"` // Direct .m3u8 / .mp4 / .webm stream URL for 60fps Native Relay Player
	Provider                string   `json:"provider"`
	SourceType              string   `json:"sourceType"` // "Public Domain", "Federated PeerTube", "HLS Reference", "Open Web", "Security Benchmark"
	Description             string   `json:"description"`
	Thumbnail               string   `json:"thumbnail,omitempty"`
	Duration                string   `json:"duration,omitempty"`
	Resolution              string   `json:"resolution,omitempty"`
	KnownManifests          []string `json:"knownManifests,omitempty"`
	AccessType              string   `json:"accessType"`
	AccessNote              string   `json:"accessNote,omitempty"`
	Price                   string   `json:"price,omitempty"`
	Region                  string   `json:"region,omitempty"`
	ReleaseYear             int      `json:"releaseYear,omitempty"`
	ListingOnly             bool     `json:"listingOnly,omitempty"`
	DestinationType         string   `json:"destinationType,omitempty"`
	AccessBasis             string   `json:"accessBasis,omitempty"`
	AccessEvidenceURL       string   `json:"accessEvidenceUrl,omitempty"`
	AvailabilityStatus      string   `json:"availabilityStatus,omitempty"`
	AvailabilityNote        string   `json:"availabilityNote,omitempty"`
	AvailabilityEvidenceURL string   `json:"availabilityEvidenceUrl,omitempty"`
	// Tracks are the ordered files of a recording with several, as the host's
	// metadata lists them; DirectStreamURL is the first. The console plays
	// them in order.
	Tracks []AudioTrack `json:"tracks,omitempty"`
	// Metadata hints only guide which bounded sample to attempt first. They
	// never establish decoded playback and are not sent to the browser client.
	mediaBrowserFormat bool
	mediaBitrate       float64
	// Provider metadata binds an authorized main feature to its public page.
	// Signed stream URLs stay private until protected playback is supported.
	providerContentBound bool
	providerSSAI         bool
	providerStreamURL    string
	// providerCheck names the driver whose access check bound the media and
	// providerCheckPage the page it checked, so the binding can be verified
	// again before any sample.
	providerCheck     string
	providerCheckPage string
}

// VerifiedSource represents a candidate source after stream verification and cleanliness scoring.
type VerifiedSource struct {
	CandidateSource
	PlaybackCheck      *PlaybackCheck     `json:"playbackCheck,omitempty"`
	AvailabilityCheck  *AvailabilityCheck `json:"availabilityCheck,omitempty"`
	Verified           bool               `json:"verified"`
	PrunedReason       string             `json:"prunedReason,omitempty"`
	HasVideoDOM        bool               `json:"hasVideoDom"`
	HasIframePlayer    bool               `json:"hasIframePlayer"`
	DetectedManifests  []string           `json:"detectedManifests"`
	StreamFormat       string             `json:"streamFormat"` // "HLS (.m3u8)", "DASH (.mpd)", "Direct MP4", "Direct WebM", "HTML5 <video>"
	CleanlinessScore   int                `json:"cleanlinessScore"`
	ThreatsBlocked     int                `json:"threatsBlocked"`
	VerifyDurationMs   int64              `json:"verifyDurationMs"`
	VerificationMode   string             `json:"verificationMode"` // "CDP Headless Tab" or "Stream Manifest Probe"
	VerificationStatus string             `json:"verificationStatus"`
	CheckedAt          string             `json:"checkedAt"`
	RelevanceScore     int                `json:"relevanceScore"`
}

var (
	reURLAttr     = regexp.MustCompile(`(?i)(?:src|href|fetch)\s*(?:=|\()\s*['"]([^'"]+)['"]`)
	reManifestURL = regexp.MustCompile(`(?i)(https?://[^\s"'<>]+?\.(?:m3u8|mpd|mp4|webm|ogv)|/[^\s"'<>]+?\.(?:m3u8|mpd|mp4|webm|ogv))`)
)

// VerifyWithCDP uses a pooled headless Chromium tab target via CDP to execute JavaScript on the candidate page,
// sniff live network requests for .m3u8/.mpd/.mp4 manifests, inspect DOM <video>/<iframe> elements, and measure
// real-time ad/tracker/popup threats via itemba-shield.
func VerifyWithCDP(ctx context.Context, poolBrowser *cdp.BrowserInstance, sh *shield.Shield, cand CandidateSource) VerifiedSource {
	start := time.Now()
	if poolBrowser == nil || poolBrowser.IsClosed() {
		return VerifyFastHTTP(ctx, sh, cand)
	}

	tabCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tab, err := poolBrowser.NewTabTarget(tabCtx)
	if err != nil {
		return VerifyFastHTTP(ctx, sh, cand)
	}
	defer tab.Close()

	res := VerifiedSource{
		CandidateSource:    cand,
		DetectedManifests:  append([]string{}, cand.KnownManifests...),
		CleanlinessScore:   100,
		VerificationMode:   "CDP Headless Tab",
		VerificationStatus: "unavailable",
		CheckedAt:          time.Now().UTC().Format(time.RFC3339),
	}

	var (
		mu             sync.Mutex
		threatsBlocked int
		manifests      = append([]string{}, cand.KnownManifests...)
		manifestSet    = make(map[string]bool)
		status404      bool
	)
	for _, m := range manifests {
		manifestSet[m] = true
	}

	if err := sh.Attach(tabCtx, tab, func(ev shield.ThreatEvent) {
		mu.Lock()
		threatsBlocked++
		mu.Unlock()
	}, func(stream shield.SniffedStream) {
		mu.Lock()
		if !manifestSet[stream.URL] && len(manifests) < 8 {
			manifestSet[stream.URL] = true
			manifests = append(manifests, stream.URL)
		}
		mu.Unlock()
	}); err != nil {
		return VerifyFastHTTP(ctx, sh, cand)
	}

	tab.OnEvent(func(method string, params json.RawMessage) {
		if method == "Network.responseReceived" {
			var p struct {
				Type     string `json:"type"`
				Response struct {
					Status int `json:"status"`
				} `json:"response"`
			}
			if json.Unmarshal(params, &p) == nil && p.Type == "Document" && p.Response.Status >= 400 {
				mu.Lock()
				status404 = true
				mu.Unlock()
			}
		}
	})

	if err := tab.Call(tabCtx, "Page.navigate", map[string]any{"url": cand.URL}, nil); err != nil {
		return VerifyFastHTTP(ctx, sh, cand)
	}

	// Poll DOM for up to 2.6 seconds for <video>, <iframe>, or manifest activity
	deadline := time.Now().Add(2600 * time.Millisecond)
	var domState struct {
		VideoCount     int    `json:"videoCount"`
		IframeCount    int    `json:"iframeCount"`
		VideoSrc       string `json:"videoSrc"`
		HasFakeWall    bool   `json:"hasFakeWall"`
		DetectedHeight int    `json:"detectedHeight"`
	}

	probeJS := `(() => {
		const vids = Array.from(document.querySelectorAll('video'));
		const iframes = Array.from(document.querySelectorAll('iframe')).filter(f => {
			const s = (f.src || '').toLowerCase();
			return s && !s.includes('doubleclick') && !s.includes('ads');
		});
		let src = '';
		let h = 0;
		for (const v of vids) {
			const c = v.currentSrc || v.src || (v.querySelector('source') ? v.querySelector('source').src : '');
			if (c && !c.startsWith('blob:')) src = c;
			if (v.videoHeight > h) h = v.videoHeight;
		}
		const hasFakeWall = document.querySelector('[data-fake-play-wall]') !== null;
		return {
			videoCount: vids.length,
			iframeCount: iframes.length,
			videoSrc: src,
			hasFakeWall: hasFakeWall,
			detectedHeight: h
		};
	})()`

	for time.Now().Before(deadline) {
		time.Sleep(280 * time.Millisecond)
		var evalResp struct {
			Result struct {
				Value struct {
					VideoCount     int    `json:"videoCount"`
					IframeCount    int    `json:"iframeCount"`
					VideoSrc       string `json:"videoSrc"`
					HasFakeWall    bool   `json:"hasFakeWall"`
					DetectedHeight int    `json:"detectedHeight"`
				} `json:"value"`
			} `json:"result"`
		}
		if err := tab.Call(tabCtx, "Runtime.evaluate", map[string]any{
			"expression":    probeJS,
			"returnByValue": true,
		}, &evalResp); err == nil {
			domState = evalResp.Result.Value
			mu.Lock()
			hasMan := len(manifests) > 0
			mu.Unlock()
			if domState.VideoCount > 0 || domState.IframeCount > 0 || hasMan || domState.HasFakeWall {
				time.Sleep(180 * time.Millisecond)
				break
			}
		}
	}

	mu.Lock()
	res.ThreatsBlocked = threatsBlocked
	res.DetectedManifests = append([]string{}, manifests...)
	is404 := status404
	domSourceSeen := manifestSet[domState.VideoSrc]
	mu.Unlock()

	res.VerifyDurationMs = time.Since(start).Milliseconds()
	res.HasVideoDOM = domState.VideoCount > 0
	res.HasIframePlayer = domState.IframeCount > 0

	if domState.VideoSrc != "" && !domSourceSeen {
		res.DetectedManifests = append(res.DetectedManifests, domState.VideoSrc)
	}
	if res.DirectStreamURL == "" && len(res.DetectedManifests) > 0 {
		res.DirectStreamURL = res.DetectedManifests[0]
	}

	score := 100 - (res.ThreatsBlocked * 8)
	if score < 15 {
		score = 15
	}
	res.CleanlinessScore = score

	if is404 {
		res.Verified = false
		res.PrunedReason = "HTTP 404 / Dead Stream Endpoint"
		return res
	}
	if domState.HasFakeWall && !res.HasVideoDOM && len(res.DetectedManifests) == 0 {
		res.Verified = false
		res.PrunedReason = "Deceptive Fake Play Button / Registration Wall"
		return res
	}
	if !res.HasVideoDOM && !res.HasIframePlayer && len(res.DetectedManifests) == 0 && res.DirectStreamURL == "" {
		res.Verified = false
		res.VerificationStatus = "page_reachable"
		res.VerificationMode = "Page reached; playback not checked"
		return res
	}

	res.VerificationStatus = "page_reachable"
	res.VerificationMode = "Player markup found; playback not checked"
	if res.DirectStreamURL != "" {
		if reachable, _ := probeMedia(ctx, &http.Client{Timeout: 4 * time.Second}, sh, res.DirectStreamURL); reachable {
			res.Verified = true
			res.VerificationStatus = "media_reachable"
			res.VerificationMode = "CDP discovery and media response check"
		}
	}
	res.StreamFormat = classifyStreamFormat(res.DirectStreamURL, res.DetectedManifests, res.HasVideoDOM, res.HasIframePlayer)
	if res.Resolution == "" && domState.DetectedHeight > 0 {
		res.Resolution = fmt.Sprintf("%dp", domState.DetectedHeight)
	}
	return res
}

// VerifyFastHTTP performs fast stream manifest & DOM inspection for API-discovered catalog sources.
func VerifyFastHTTP(ctx context.Context, sh *shield.Shield, cand CandidateSource) VerifiedSource {
	return verifyFastHTTPWithClient(ctx, sh, cand, &http.Client{Timeout: 4 * time.Second})
}

func verifyFastHTTPWithClient(ctx context.Context, sh *shield.Shield, cand CandidateSource, client *http.Client) VerifiedSource {
	start := time.Now()
	res := VerifiedSource{
		CandidateSource:    cand,
		DetectedManifests:  append([]string{}, cand.KnownManifests...),
		CleanlinessScore:   100,
		VerificationMode:   "Stream Manifest Probe",
		VerificationStatus: "unavailable",
		CheckedAt:          time.Now().UTC().Format(time.RFC3339),
	}

	// If candidate already has a resolved direct stream URL (.m3u8 / .mp4 / .webm) from metadata API, verify reachability
	if cand.DirectStreamURL != "" {
		res.Verified, res.PrunedReason = probeMedia(ctx, client, sh, cand.DirectStreamURL)
		if res.Verified {
			res.VerificationStatus = "media_reachable"
			res.DetectedManifests = []string{cand.DirectStreamURL}
			res.StreamFormat = classifyStreamFormat(cand.DirectStreamURL, res.DetectedManifests, false, false)
		}
		res.VerifyDurationMs = time.Since(start).Milliseconds()
		return res
	}

	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cand.URL, nil)
	if err != nil {
		res.PrunedReason = "Invalid candidate URL"
		res.VerifyDurationMs = time.Since(start).Milliseconds()
		return res
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		res.PrunedReason = "Host unreachable or connection timed out"
		res.VerifyDurationMs = time.Since(start).Milliseconds()
		return res
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		res.PrunedReason = "HTTP " + resp.Status + " / Dead Endpoint"
		res.VerifyDurationMs = time.Since(start).Milliseconds()
		return res
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	html := string(bodyBytes)
	lowerHTML := strings.ToLower(html)

	threats := 0
	urlMatches := reURLAttr.FindAllStringSubmatch(html, -1)
	for _, m := range urlMatches {
		if len(m) >= 2 {
			match := sh.Engine().Inspect(m[1], "Subresource")
			if match.Blocked {
				threats++
				sh.RecordEvent(shield.ThreatEvent{
					Layer:        match.Layer,
					Category:     match.Category,
					Target:       m[1],
					Rule:         match.Rule,
					ListSource:   match.ListSource,
					ResourceType: "ScoutProbe",
				})
			}
		}
	}

	res.ThreatsBlocked = threats
	score := 100 - (threats * 9)
	if score < 15 {
		score = 15
	}
	res.CleanlinessScore = score

	hasFakeWall := strings.Contains(lowerHTML, "data-fake-play-wall") ||
		strings.Contains(lowerHTML, "credit card required to watch")

	res.HasVideoDOM = strings.Contains(lowerHTML, "<video") || strings.Contains(lowerHTML, "jwplayer") || strings.Contains(lowerHTML, "video-js")
	res.HasIframePlayer = strings.Contains(lowerHTML, "<iframe") || strings.Contains(cand.URL, "/embed/")

	manifestMatches := reManifestURL.FindAllString(html, 5)
	seen := make(map[string]bool)
	for _, m := range res.DetectedManifests {
		seen[m] = true
	}
	for _, m := range manifestMatches {
		if !seen[m] {
			seen[m] = true
			res.DetectedManifests = append(res.DetectedManifests, m)
		}
	}
	if res.DirectStreamURL == "" && len(res.DetectedManifests) > 0 {
		res.DirectStreamURL = res.DetectedManifests[0]
	}

	res.VerifyDurationMs = time.Since(start).Milliseconds()

	if hasFakeWall && !res.HasVideoDOM && len(res.DetectedManifests) == 0 {
		res.Verified = false
		res.PrunedReason = "Fake Play Button / Account Registration Wall Detected"
		return res
	}
	if !res.HasVideoDOM && !res.HasIframePlayer && len(res.DetectedManifests) == 0 {
		res.Verified = false
		res.VerificationStatus = "page_reachable"
		res.VerificationMode = "Page reached; playback not checked"
		return res
	}

	res.Verified = false
	res.VerificationStatus = "page_reachable"
	res.VerificationMode = "Player markup found; playback not checked"
	res.StreamFormat = classifyStreamFormat(res.DirectStreamURL, res.DetectedManifests, res.HasVideoDOM, res.HasIframePlayer)
	return res
}

// probeMedia checks an actual bounded response. A file extension or catalog URL alone is not verification.
func probeMedia(ctx context.Context, client *http.Client, sh *shield.Shield, target string) (bool, string) {
	if match := sh.Engine().Inspect(target, "Media"); match.Blocked {
		return false, "Media URL blocked by ad protection"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
	if err != nil || req.URL.Hostname() == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return false, "Invalid media URL"
	}
	req.Header.Set("Range", "bytes=0-1023")
	req.Header.Set("User-Agent", "ITEMBA-NET-Scout/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return false, "Media host unreachable or check timed out"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false, "Media returned HTTP " + resp.Status
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || len(body) == 0 {
		return false, "Empty or unreadable media response"
	}
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	sniffed := http.DetectContentType(body)
	isMedia := strings.HasPrefix(sniffed, "video/") || strings.HasPrefix(text, "#EXTM3U") || len(body) > 8 && string(body[4:8]) == "ftyp" || len(body) > 4 && string(body[:4]) == "OggS" || audioBytes(body, sniffed)
	// DASH is XML; accept only an MPD root, including documents with an XML declaration.
	if !isMedia && strings.HasPrefix(text, "<") {
		decoder := xml.NewDecoder(bytes.NewReader(body))
		for {
			token, decodeErr := decoder.Token()
			if decodeErr != nil {
				break
			}
			if root, ok := token.(xml.StartElement); ok {
				isMedia = root.Name.Local == "MPD"
				break
			}
		}
	}
	if !isMedia {
		return false, "Response did not contain recognizable media"
	}
	return true, ""
}

// audioBytes recognises the audio containers the console can play: what
// Go's sniffer names audio (ID3-tagged MP3, WAVE, AIFF), FLAC by its marker,
// and a bare MP3 frame, whose eleven-bit sync word no text or HTML starts
// with. Ogg and MP4 audio are recognised with video by their shared markers.
func audioBytes(body []byte, sniffed string) bool {
	if strings.HasPrefix(sniffed, "audio/") {
		return true
	}
	if len(body) >= 4 && string(body[:4]) == "fLaC" {
		return true
	}
	return len(body) >= 2 && body[0] == 0xFF && body[1]&0xE0 == 0xE0 && body[1]&0x18 != 0x08 && body[1]&0x06 != 0
}

func classifyStreamFormat(directURL string, manifests []string, hasVideo, hasIframe bool) string {
	all := append([]string{directURL}, manifests...)
	for _, m := range all {
		lower := strings.ToLower(m)
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
			return "Direct Ogg"
		}
	}
	if hasVideo {
		return "HTML5 <video>"
	}
	if hasIframe {
		return "Embedded Player"
	}
	return "Verified Stream"
}
