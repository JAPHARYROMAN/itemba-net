package scout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/itemba-net/core/internal/shield"
)

const (
	moviePlaybackByteLimit     = 6 << 20
	moviePlaybackResponseLimit = 1 << 20
	moviePlaybackPlaylistLimit = 1 << 20
	moviePlaybackScriptLimit   = 600 << 10
	moviePlaybackTargetLimit   = 128
	moviePlaybackLifetime      = 14 * time.Second
	moviePlaybackUnused        = "/resource/00000000000000000000000000000000"
)

var (
	moviePlaybackHLSScript = readMoviePlaybackHLSScript("web/dist/playback-hls.min.js")
	errPlaybackBudget      = errors.New("playback sample byte limit reached")
	playbackTagURI         = regexp.MustCompile(`(?i)\bURI\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	playbackBandwidth      = regexp.MustCompile(`(?:[:,])BANDWIDTH=([0-9]+)`)
	playbackContentRange   = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+|\*)$`)
	playbackRequestRange   = regexp.MustCompile(`^bytes=([0-9]*)-([0-9]*)$`)
	playbackURIAttribute   = regexp.MustCompile(`(?i)\bURI\s*=`)
	playbackAttribute      = regexp.MustCompile(`(?i)(?:[:,])\s*([A-Z0-9-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^,\s]+))`)
	playbackAdClass        = regexp.MustCompile(`(?i)(?:^|[._:/\s-])(?:ad|ads|adbreak|advert|advertisement|advertising|ssai|interstitial|preroll|midroll|postroll)(?:$|[._:/\s-])`)
)

// Load only the installed, locally built player artifact, once at server startup.
func readMoviePlaybackHLSScript(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > moviePlaybackScriptLimit {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, moviePlaybackScriptLimit+1))
	if err != nil || len(data) == 0 || len(data) > moviePlaybackScriptLimit || !utf8.Valid(data) {
		return nil
	}
	return data
}

type moviePlaybackBudget struct {
	mu        sync.Mutex
	remaining int64
}

type moviePlaybackReader struct {
	body   io.Reader
	budget *moviePlaybackBudget
}

func (r moviePlaybackReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.budget.mu.Lock()
	allowed := int64(len(p))
	if allowed > r.budget.remaining {
		allowed = r.budget.remaining
	}
	if allowed <= 0 {
		r.budget.mu.Unlock()
		return 0, errPlaybackBudget
	}
	r.budget.remaining -= allowed
	r.budget.mu.Unlock()
	n, err := r.body.Read(p[:int(allowed)])
	r.budget.mu.Lock()
	r.budget.remaining += allowed - int64(n)
	r.budget.mu.Unlock()
	return n, err
}

type moviePlaybackHarness struct {
	client        *http.Client
	budget        moviePlaybackBudget
	slots         chan struct{}
	mu            sync.Mutex
	targets       map[string]string
	ids           map[string]string
	shield        *shield.Shield
	outcomeReason string
}

func (h *moviePlaybackHarness) outcome() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outcomeReason
}

func (h *moviePlaybackHarness) stopWithReason(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.outcomeReason == "" {
		h.outcomeReason = reason
	}
}

func (h *moviePlaybackHarness) inspectTarget(target *url.URL) error {
	h.mu.Lock()
	if h.shield == nil {
		h.shield = shield.New()
	}
	protection := h.shield
	h.mu.Unlock()
	match := protection.Engine().Inspect(target.String(), "Media")
	if !match.Blocked {
		return nil
	}
	h.stopWithReason("blocked_with_protection")
	redacted := *target
	redacted.RawQuery, redacted.Fragment = "", ""
	protection.RecordEvent(shield.ThreatEvent{Layer: match.Layer, Category: match.Category, Target: redacted.String(), Rule: match.Rule, ListSource: match.ListSource, ResourceType: "Playback relay"})
	return errors.New("playback resource blocked by protection")
}

func (h *moviePlaybackHarness) resolveTarget(base *url.URL, raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("empty playback resource")
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("invalid playback resource")
	}
	target, safe := safeAvailabilityURL(base.ResolveReference(reference).String())
	if !safe {
		return nil, errors.New("playback resource is not a public web destination")
	}
	if err := h.inspectTarget(target); err != nil {
		return nil, err
	}
	return target, nil
}

func (h *moviePlaybackHarness) register(base *url.URL, raw string) (string, error) {
	target, err := h.resolveTarget(base, raw)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if id, exists := h.ids[target.String()]; exists {
		return "/resource/" + id, nil
	}
	if len(h.targets) >= moviePlaybackTargetLimit {
		return "", errors.New("playback resource limit reached")
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", errors.New("playback resource could not be registered")
	}
	id := hex.EncodeToString(entropy[:])
	h.targets[id], h.ids[target.String()] = target.String(), id
	return "/resource/" + id, nil
}

// Refuse explicit stitched-ad cues rather than cutting advertisements out of
// a feature timeline and reporting that a movie played. Ordinary date ranges
// and clear AES-128 identity keys remain supported; DRM license formats do not.
func playbackPlaylistProtectionReason(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.ToUpper(strings.TrimSpace(raw))
		if strings.Contains(line, "SCTE35") || strings.Contains(line, "SCTE-35") || strings.HasPrefix(line, "#EXT-X-CUE-") || strings.HasPrefix(line, "#EXT-X-CONTENT-STEERING:") {
			return "content_unconfirmed"
		}
		if strings.HasPrefix(line, "#EXT-X-DATERANGE:") {
			for _, match := range playbackAttribute.FindAllStringSubmatch(raw, -1) {
				name, value := strings.ToUpper(match[1]), match[2]+match[3]+match[4]
				if strings.HasPrefix(name, "X-AD-") || name == "X-ASSET-URI" || name == "X-ASSET-LIST" || (name == "CLASS" || name == "ID") && playbackAdClass.MatchString(value) {
					return "content_unconfirmed"
				}
			}
		}
		if strings.HasPrefix(line, "#EXT-X-KEY:") || strings.HasPrefix(line, "#EXT-X-SESSION-KEY:") {
			method := ""
			for _, match := range playbackAttribute.FindAllStringSubmatch(raw, -1) {
				name, value := strings.ToUpper(match[1]), strings.TrimSpace(match[2]+match[3]+match[4])
				switch name {
				case "METHOD":
					value = strings.ToUpper(value)
					if value != "NONE" && value != "AES-128" {
						return "drm_required"
					}
					if method != "" && method != value {
						return "content_unconfirmed"
					}
					method = value
				case "KEYFORMAT":
					if !strings.EqualFold(value, "identity") {
						return "drm_required"
					}
				case "URI":
					if strings.HasPrefix(strings.ToLower(value), "skd:") {
						return "drm_required"
					}
				}
			}
			if method != "NONE" && method != "AES-128" {
				return "drm_required"
			}
		}
	}
	return ""
}

func playbackVariantVideoClass(tag string) int {
	// Prefer an explicitly declared video track over untyped playlists. A
	// cheap audio-only rendition must never consume a movie's sample budget.
	codecs, resolution := "", ""
	for _, attr := range playbackAttribute.FindAllStringSubmatch(tag, -1) {
		value := strings.ToLower(attr[2] + attr[3] + attr[4])
		switch strings.ToUpper(attr[1]) {
		case "CODECS":
			codecs = value
		case "RESOLUTION":
			resolution = value
		}
	}
	if dimensions := strings.Split(resolution, "x"); len(dimensions) == 2 {
		w, _ := strconv.Atoi(dimensions[0])
		h, _ := strconv.Atoi(dimensions[1])
		if w > 0 && h > 0 {
			return 2
		}
	}
	if codecs == "" {
		return 1
	}
	audioOnly := true
	for _, codec := range strings.Split(codecs, ",") {
		codec = strings.TrimSpace(codec)
		for _, video := range []string{"avc1", "avc3", "hvc1", "hev1", "av01", "vp09", "vp8", "vp9", "dvhe", "dvh1"} {
			if strings.HasPrefix(codec, video) {
				return 2
			}
		}
		knownAudio := false
		for _, audio := range []string{"mp4a", "ac-3", "ec-3", "opus", "vorbis", "flac"} {
			knownAudio = knownAudio || strings.HasPrefix(codec, audio)
		}
		audioOnly = audioOnly && knownAudio
	}
	if audioOnly {
		return 0
	}
	return 1
}

func lowestPlaybackVariant(lines []string) []string {
	bestTag, bestURI, bestBandwidth, bestClass := -1, -1, int64(0), -1
	variantLines := make(map[int]bool)
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF:") {
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || strings.HasPrefix(strings.TrimSpace(lines[j]), "#") {
			continue
		}
		bandwidth := int64(1<<63 - 1)
		if match := playbackBandwidth.FindStringSubmatch(line); len(match) == 2 {
			if parsed, err := strconv.ParseInt(match[1], 10, 64); err == nil && parsed > 0 {
				bandwidth = parsed
			}
		}
		variantLines[i], variantLines[j] = true, true
		class := playbackVariantVideoClass(line)
		if class > 0 && (class > bestClass || class == bestClass && bandwidth < bestBandwidth) {
			bestTag, bestURI, bestBandwidth, bestClass = i, j, bandwidth, class
		}
	}
	if bestTag < 0 {
		if len(variantLines) == 0 {
			return lines
		}
		return nil // Only explicit audio renditions were offered.
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if !variantLines[i] || i == bestTag || i == bestURI {
			out = append(out, line)
		}
	}
	return out
}

func (h *moviePlaybackHarness) rewritePlaylist(body []byte, base *url.URL) ([]byte, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff"))
	if len(body) > moviePlaybackPlaylistLimit || !strings.HasPrefix(text, "#EXTM3U") || !utf8.Valid(body) {
		return nil, errors.New("invalid or oversized playback playlist")
	}
	if reason := playbackPlaylistProtectionReason(text); reason != "" {
		h.stopWithReason(reason)
		return nil, errors.New("playback playlist content or license could not be verified")
	}
	lines := lowestPlaybackVariant(strings.Split(text, "\n"))
	if len(lines) == 0 {
		h.stopWithReason("content_unconfirmed")
		return nil, errors.New("playback playlist did not offer a movie video track")
	}
	master := strings.Contains(text, "#EXT-X-STREAM-INF:")
	segments := 0
	register := func(raw string) (string, error) {
		if !master && segments >= 4 {
			// Preserve the complete playlist's duration while preventing the
			// bounded sample from loading the rest of a full-length movie.
			if _, err := h.resolveTarget(base, raw); err != nil {
				return "", err
			}
			return moviePlaybackUnused, nil
		}
		return h.register(base, raw)
	}
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			registered, err := register(line)
			if err != nil {
				return nil, err
			}
			lines[i] = registered
			segments++
			continue
		}
		matches := playbackTagURI.FindAllStringSubmatchIndex(line, -1)
		if playbackURIAttribute.MatchString(line) && len(matches) == 0 {
			return nil, errors.New("unsupported playback playlist resource")
		}
		var rewritten strings.Builder
		end := 0
		for _, match := range matches {
			start, finish := match[2], match[3]
			if start < 0 {
				start, finish = match[4], match[5]
			}
			registered, err := register(line[start:finish])
			if err != nil {
				return nil, err
			}
			rewritten.WriteString(line[end:match[0]])
			rewritten.WriteString(`URI="` + registered + `"`)
			end = match[1]
		}
		if len(matches) > 0 {
			rewritten.WriteString(line[end:])
			lines[i] = rewritten.String()
		} else {
			lines[i] = line
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func playbackResponseType(header string, prefix []byte) (string, bool) {
	typeName := strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	text := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(prefix), "\ufeff")))
	if strings.Contains(typeName, "html") || strings.Contains(typeName, "javascript") || strings.HasPrefix(text, "<!doctype") || strings.HasPrefix(text, "<html") || strings.HasPrefix(text, "<!--") {
		return "", false
	}
	if strings.HasPrefix(typeName, "video/") || strings.HasPrefix(typeName, "audio/") || typeName == "application/mp4" || typeName == "application/octet-stream" || typeName == "binary/octet-stream" {
		return typeName, true
	}
	sniffed := http.DetectContentType(prefix)
	if strings.HasPrefix(sniffed, "video/") || strings.HasPrefix(sniffed, "audio/") {
		return sniffed, true
	}
	// HLS keys and segments can be opaque binary data. They are never served
	// with an executable MIME type and nosniff prevents script interpretation.
	if typeName == "" || strings.Contains(typeName, "mpegurl") || strings.HasPrefix(typeName, "application/") {
		return "application/octet-stream", true
	}
	return "", false
}

func boundedPlaybackRange(value string) (string, bool, error) {
	if value == "" {
		return "bytes=0-" + strconv.Itoa(moviePlaybackResponseLimit-1), true, nil
	}
	match := playbackRequestRange.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 3 || match[1] == "" && match[2] == "" {
		return "", false, errors.New("unsupported playback range")
	}
	if match[1] == "" {
		suffix, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil || suffix <= 0 {
			return "", false, errors.New("unsupported playback range")
		}
		if suffix > moviePlaybackResponseLimit {
			suffix = moviePlaybackResponseLimit
		}
		return "bytes=-" + strconv.FormatInt(suffix, 10), false, nil
	}
	start, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || start > (1<<63-1)-moviePlaybackResponseLimit {
		return "", false, errors.New("unsupported playback range")
	}
	end := start + moviePlaybackResponseLimit - 1
	if match[2] != "" {
		requestedEnd, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil || requestedEnd < start {
			return "", false, errors.New("unsupported playback range")
		}
		if requestedEnd < end {
			end = requestedEnd
		}
	}
	return "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10), start == 0, nil
}

func (h *moviePlaybackHarness) relay(w http.ResponseWriter, r *http.Request, target string) {
	if h.outcome() != "" {
		http.Error(w, "Protected playback sample stopped", http.StatusBadGateway)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-r.Context().Done():
		return
	}
	if h.outcome() != "" {
		http.Error(w, "Protected playback sample stopped", http.StatusBadGateway)
		return
	}
	h.budget.mu.Lock()
	remaining := h.budget.remaining
	h.budget.mu.Unlock()
	if remaining <= 0 {
		http.Error(w, "Playback sample limit reached", http.StatusBadGateway)
		return
	}
	initial, safe := safeAvailabilityURL(target)
	if !safe {
		http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
		return
	}
	if h.inspectTarget(initial) != nil {
		http.Error(w, "Playback resource blocked by protection", http.StatusBadGateway)
		return
	}
	requestCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, initial.String(), nil)
	if err != nil {
		http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
		return
	}
	request.Header.Set("User-Agent", "ITEMBA-NET/0.1 (bounded playback sample)")
	boundedRange, startsAtZero, rangeErr := boundedPlaybackRange(r.Header.Get("Range"))
	if rangeErr != nil {
		http.Error(w, "Playback range unavailable", http.StatusBadRequest)
		return
	}
	request.Header.Set("Range", boundedRange)
	if value := r.Header.Get("If-Range"); value != "" {
		request.Header.Set("If-Range", value)
	}
	// Track accepted redirects per fetch as well as response.Request; custom
	// fixture transports need not populate the latter, and concurrent resources
	// must never share the final playlist base URL.
	relayClient := *h.client
	finalURL := initial
	redirectPolicy := relayClient.CheckRedirect
	relayClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if _, safe := safeAvailabilityURL(req.URL.String()); !safe {
			return errors.New("playback redirect destination rejected")
		}
		if err := h.inspectTarget(req.URL); err != nil {
			return err
		}
		if redirectPolicy != nil {
			if err := redirectPolicy(req, via); err != nil {
				return err
			}
		}
		finalURL = req.URL
		return nil
	}
	if h.outcome() != "" {
		http.Error(w, "Protected playback sample stopped", http.StatusBadGateway)
		return
	}
	response, err := relayClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
		return
	}
	base := finalURL
	if response.Request != nil && response.Request.URL != nil {
		var valid bool
		base, valid = safeAvailabilityURL(response.Request.URL.String())
		if !valid {
			http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
			return
		}
	}
	if h.inspectTarget(base) != nil {
		http.Error(w, "Playback resource blocked by protection", http.StatusBadGateway)
		return
	}
	boundedBody := moviePlaybackReader{body: response.Body, budget: &h.budget}
	prefix := make([]byte, 512)
	prefixSize, _ := io.ReadFull(boundedBody, prefix)
	prefix = prefix[:prefixSize]
	reader := io.MultiReader(bytes.NewReader(prefix), boundedBody)
	contentType := response.Header.Get("Content-Type")
	playlist := strings.Contains(strings.ToLower(contentType), "mpegurl") || strings.HasSuffix(strings.ToLower(base.Path), ".m3u8") || strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(string(prefix), "\ufeff")), "#EXTM3U")
	var body []byte
	truncated := false
	if playlist {
		body, err = io.ReadAll(io.LimitReader(reader, moviePlaybackPlaylistLimit+1))
		if err == nil {
			body, err = h.rewritePlaylist(body, base)
		}
		if err != nil {
			http.Error(w, "Playback playlist unavailable", http.StatusBadGateway)
			return
		}
		contentType = "application/vnd.apple.mpegurl"
	} else {
		var allowed bool
		contentType, allowed = playbackResponseType(contentType, prefix)
		if !allowed {
			http.Error(w, "Playback resource unavailable", http.StatusBadGateway)
			return
		}
		body, err = io.ReadAll(io.LimitReader(reader, moviePlaybackResponseLimit+1))
		if err != nil && !errors.Is(err, errPlaybackBudget) || len(body) == 0 || r.Context().Err() != nil {
			http.Error(w, "Playback sample unavailable", http.StatusBadGateway)
			return
		}
		truncated = errors.Is(err, errPlaybackBudget) || len(body) > moviePlaybackResponseLimit
		if len(body) > moviePlaybackResponseLimit {
			body = body[:moviePlaybackResponseLimit]
		}
	}
	status := response.StatusCode
	if playlist {
		status = http.StatusOK
	} else if status == http.StatusOK && truncated {
		// An ignored Range must not turn a truncated movie into an apparently
		// complete file. Preserve total identity only for a known-size prefix.
		if !startsAtZero || response.ContentLength <= int64(len(body)) {
			http.Error(w, "Playback source did not support bounded ranges", http.StatusBadGateway)
			return
		}
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", "bytes 0-"+strconv.Itoa(len(body)-1)+"/"+strconv.FormatInt(response.ContentLength, 10))
	} else if status == http.StatusPartialContent {
		parts := playbackContentRange.FindStringSubmatch(response.Header.Get("Content-Range"))
		if len(parts) != 4 {
			http.Error(w, "Playback range unavailable", http.StatusBadGateway)
			return
		}
		start, parseErr := strconv.ParseInt(parts[1], 10, 64)
		end, endErr := strconv.ParseInt(parts[2], 10, 64)
		if parseErr != nil || endErr != nil || start > (1<<63-1)-int64(len(body)) || end < start || int64(len(body))-1 > end-start {
			http.Error(w, "Playback range unavailable", http.StatusBadGateway)
			return
		}
		if parts[3] != "*" {
			total, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil || total <= start+int64(len(body))-1 {
				http.Error(w, "Playback range unavailable", http.StatusBadGateway)
				return
			}
		}
		w.Header().Set("Content-Range", "bytes "+parts[1]+"-"+strconv.FormatInt(start+int64(len(body))-1, 10)+"/"+parts[3])
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if value := response.Header.Get("Accept-Ranges"); !playlist && value != "" {
		w.Header().Set("Accept-Ranges", value)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func startMoviePlaybackHarness(ctx context.Context, source VerifiedSource, client *http.Client, protections ...*shield.Shield) (origin string, close func(), err error) {
	var protection *shield.Shield
	if len(protections) > 0 {
		protection = protections[0]
	}
	origin, close, _, err = startProtectedMoviePlaybackHarness(ctx, source, client, protection)
	return origin, close, err
}

func startProtectedMoviePlaybackHarness(ctx context.Context, source VerifiedSource, client *http.Client, protection *shield.Shield) (origin string, close func(), outcome func() string, err error) {
	if protection == nil {
		protection = shield.New()
	}
	h := &moviePlaybackHarness{shield: protection, budget: moviePlaybackBudget{remaining: moviePlaybackByteLimit}, slots: make(chan struct{}, 4), targets: make(map[string]string), ids: make(map[string]string)}
	outcome = h.outcome
	media := strings.TrimSpace(source.DirectStreamURL)
	if media == "" {
		for _, known := range source.KnownManifests {
			if strings.TrimSpace(known) != "" {
				media = strings.TrimSpace(known)
				break
			}
		}
	}
	initial, safe := safeAvailabilityURL(media)
	if !safe || ctx.Err() != nil {
		return "", nil, outcome, errors.New("playback sample requires a public media source")
	}
	if err := h.inspectTarget(initial); err != nil {
		return "", nil, outcome, err
	}
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guarded := *client
	if guarded.Transport == nil {
		guarded.Transport = newMovieAvailabilityClient().Transport
	}
	guarded.Jar = nil
	// Page availability uses a shorter deadline; cold CDN sample bodies need
	// the bounded playback attempt's lifetime while retaining the same route.
	guarded.Timeout = moviePlaybackLifetime
	previousRedirect := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("playback redirect limit reached")
		}
		if _, allowed := safeAvailabilityURL(req.URL.String()); !allowed {
			return errors.New("playback redirect destination rejected")
		}
		if previousRedirect != nil {
			if err := previousRedirect(req, via); err != nil {
				return err
			}
		}
		for name := range req.Header {
			if !strings.EqualFold(name, "Range") && !strings.EqualFold(name, "If-Range") && !strings.EqualFold(name, "User-Agent") {
				req.Header.Del(name)
			}
		}
		return nil
	}
	var nonceBytes [16]byte
	if _, err = rand.Read(nonceBytes[:]); err != nil {
		return "", nil, outcome, errors.New("playback sample could not be initialized")
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, outcome, errors.New("local playback sample could not be started")
	}
	attemptCtx, cancel := context.WithTimeout(ctx, moviePlaybackLifetime)
	h.client = &guarded
	isHLS := strings.HasSuffix(strings.ToLower(initial.Path), ".m3u8") || strings.Contains(strings.ToLower(source.StreamFormat), "hls")
	page := moviePlaybackHTML(nonce, isHLS, len(moviePlaybackHLSScript) > 0)
	address := listener.Addr().String()
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, WriteTimeout: moviePlaybackLifetime, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return attemptCtx }}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; connect-src 'self'; media-src 'self' blob:; img-src 'none'; style-src 'none'; object-src 'none'; worker-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != address || r.URL.RawQuery != "" {
			http.Error(w, "Unknown playback request", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
		case r.URL.Path == "/hls.js" && len(moviePlaybackHLSScript) > 0:
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			_, _ = w.Write(moviePlaybackHLSScript)
		case r.URL.Path == "/media":
			h.relay(w, r, initial.String())
		case strings.HasPrefix(r.URL.Path, "/resource/"):
			id := strings.TrimPrefix(r.URL.Path, "/resource/")
			h.mu.Lock()
			target, exists := h.targets[id]
			h.mu.Unlock()
			if !exists {
				http.NotFound(w, r)
				return
			}
			h.relay(w, r, target)
		default:
			http.NotFound(w, r)
		}
	})
	var once sync.Once
	close = func() { once.Do(func() { cancel(); _ = server.Close(); _ = listener.Close() }) }
	go func() { _ = server.Serve(listener) }()
	go func() { <-attemptCtx.Done(); close() }()
	return "http://" + address, close, outcome, nil
}

func moviePlaybackHTML(nonce string, hls, library bool) []byte {
	script, source := "", ` src="/media"`
	if library {
		script = `<script src="/hls.js"></script>`
	}
	if hls {
		source = ""
	}
	page := `<!doctype html><html><head><meta charset="utf-8"><title>ITEMBA playback sample</title></head><body><video id="sample" autoplay muted playsinline preload="auto"{{source}}></video>{{script}}<script nonce="{{nonce}}">
(() => {
const video=document.getElementById('sample');
window.__itembaPlaybackProtocol='native';window.__itembaPlaybackState='loading';window.__itembaPlaybackError='';
video.muted=true;video.addEventListener('playing',()=>{window.__itembaPlaybackState='playing'});
video.addEventListener('error',()=>{window.__itembaPlaybackError='media_error';window.__itembaPlaybackState='error'});
const play=()=>{video.play().catch(()=>{window.__itembaPlaybackError='play_rejected'})};
if({{hls}} && typeof Hls!=='undefined' && Hls.isSupported()) {
 window.__itembaPlaybackProtocol='hls';
 const player=new Hls({enableWorker:false,startLevel:0,maxBufferLength:3,maxMaxBufferLength:3,maxBufferSize:1048576,backBufferLength:0,lowLatencyMode:false,autoStartLoad:true});
 player.on(Hls.Events.ERROR,(_event,data)=>{if(data.fatal){window.__itembaPlaybackError='hls_error';window.__itembaPlaybackState='error';player.stopLoad()}});
 player.on(Hls.Events.MANIFEST_PARSED,play);player.loadSource('/media');player.attachMedia(video);
 window.addEventListener('pagehide',()=>player.destroy(),{once:true});
} else if({{hls}} && !video.canPlayType('application/vnd.apple.mpegurl')) {window.__itembaPlaybackState='unsupported_hls';}
else {video.src='/media';play();}
})();</script></body></html>`
	return []byte(strings.NewReplacer("{{source}}", source, "{{script}}", script, "{{nonce}}", nonce, "{{hls}}", strconv.FormatBool(hls)).Replace(page))
}
