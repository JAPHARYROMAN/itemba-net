package relay

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

// Handler provides a zero-trust sanitizing reverse proxy for HLS (.m3u8) manifests and MP4/WebM/TS media segments.
// It strips client cookies/referrers, blocks ad-stitched HLS segments via itemba-shield, rewrites HLS manifest URLs
// to stay inside the relay, and supports HTTP Range requests (206 Partial Content) for instant seeking.
type Handler struct {
	shield *shield.Shield
	client *http.Client
}

// NewHandler creates a new Zero-Trust Stream Relay handler.
func NewHandler(sh *shield.Shield) *Handler {
	return &Handler{
		shield: sh,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawTarget := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawTarget == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	parsedTarget, err := url.Parse(rawTarget)
	if err != nil || (parsedTarget.Scheme != "http" && parsedTarget.Scheme != "https") {
		http.Error(w, "invalid target stream url", http.StatusBadRequest)
		return
	}

	// Verify target against itemba-shield before connecting
	if match := h.shield.Engine().Inspect(rawTarget, "Media"); match.Blocked {
		h.shield.RecordEvent(shield.ThreatEvent{
			Layer:        match.Layer,
			Category:     match.Category,
			Target:       rawTarget,
			Rule:         match.Rule,
			ListSource:   match.ListSource,
			ResourceType: "MediaRelay",
		})
		http.Error(w, "blocked by itemba-shield", http.StatusForbidden)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawTarget, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Zero-trust header sanitization: never forward client cookies or tracking headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream stream fetch failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")

	contentType := resp.Header.Get("Content-Type")
	lowerURL := strings.ToLower(parsedTarget.Path)
	isM3U8 := strings.HasSuffix(lowerURL, ".m3u8") ||
		strings.Contains(strings.ToLower(contentType), "mpegurl") ||
		strings.Contains(strings.ToLower(contentType), "x-mpegurl")

	if isM3U8 && resp.StatusCode == http.StatusOK {
		h.rewriteAndServeHLS(w, resp.Body, parsedTarget)
		return
	}

	for _, hdr := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if val := resp.Header.Get(hdr); val != "" {
			w.Header().Set(hdr, val)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *Handler) rewriteAndServeHLS(w http.ResponseWriter, body io.Reader, base *url.URL) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(body)
	var out strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			out.WriteString("\n")
			continue
		}

		// Rewrite URI="..." attributes inside #EXT-X-KEY or #EXT-X-MAP or #EXT-X-MEDIA tags
		if strings.HasPrefix(line, "#") {
			if strings.Contains(line, "URI=\"") {
				line = rewriteTagURI(line, base)
			}
			out.WriteString(line + "\n")
			continue
		}

		// Resolve relative or absolute segment/sub-playlist URL
		resolved, err := base.Parse(line)
		if err != nil {
			out.WriteString(line + "\n")
			continue
		}

		// Strip stitched ad segments matching itemba-shield
		if match := h.shield.Engine().Inspect(resolved.String(), "Media"); match.Blocked {
			h.shield.RecordEvent(shield.ThreatEvent{
				Layer:        match.Layer,
				Category:     "HLS Ad Segment Stripped",
				Target:       resolved.String(),
				Rule:         match.Rule,
				ListSource:   match.ListSource,
				ResourceType: "HLS_Manifest",
			})
			continue
		}

		proxied := "/api/relay/stream?url=" + url.QueryEscape(resolved.String())
		out.WriteString(proxied + "\n")
	}

	_, _ = io.WriteString(w, out.String())
}

func rewriteTagURI(tagLine string, base *url.URL) string {
	startIdx := strings.Index(tagLine, "URI=\"")
	if startIdx == -1 {
		return tagLine
	}
	valStart := startIdx + 5
	endOffset := strings.Index(tagLine[valStart:], "\"")
	if endOffset == -1 {
		return tagLine
	}
	rawURI := tagLine[valStart : valStart+endOffset]
	resolved, err := base.Parse(rawURI)
	if err != nil {
		return tagLine
	}
	proxied := "/api/relay/stream?url=" + url.QueryEscape(resolved.String())
	return tagLine[:valStart] + proxied + tagLine[valStart+endOffset:]
}
