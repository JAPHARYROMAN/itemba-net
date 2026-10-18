package scout

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

type playbackHarnessTransport func(*http.Request) (*http.Response, error)

func (f playbackHarnessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func playbackFixtureResponse(code int, mediaType string, body []byte) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {mediaType}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}
}

func playbackFixtureHarness(t *testing.T, source string, transport playbackHarnessTransport) (string, func()) {
	t.Helper()
	origin, close, err := startMoviePlaybackHarness(context.Background(), VerifiedSource{CandidateSource: CandidateSource{DirectStreamURL: source}}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(close)
	return origin, close
}

func playbackHarnessGET(t *testing.T, origin, path string, headers http.Header) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, origin+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header.Clone(), body
}

func TestPlaybackHarnessOwnsOneVideoAndRejectsCallerTargets(t *testing.T) {
	var upstream atomic.Int32
	origin, _ := playbackFixtureHarness(t, "https://media.example.com/movie.mp4", func(*http.Request) (*http.Response, error) {
		upstream.Add(1)
		return playbackFixtureResponse(200, "video/mp4", []byte("fixture")), nil
	})
	status, headers, body := playbackHarnessGET(t, origin, "/", nil)
	if status != 200 || strings.Count(string(body), "<video ") != 1 || !bytes.Contains(body, []byte(`src="/media"`)) || bytes.Contains(body, []byte("media.example.com")) {
		t.Fatal("sample page does not isolate one fixed local-source video")
	}
	for _, policy := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "connect-src 'self'", "media-src 'self' blob:", "worker-src 'none'"} {
		if !strings.Contains(headers.Get("Content-Security-Policy"), policy) {
			t.Errorf("sample CSP missing %s", policy)
		}
	}
	for _, path := range []string{"/media?url=https://evil.example.com/movie", "/resource/unregistered", "/unknown"} {
		code, _, _ := playbackHarnessGET(t, origin, path, nil)
		if code < 400 {
			t.Errorf("unknown/caller-selected target accepted: %s", path)
		}
	}
	if upstream.Load() != 0 {
		t.Fatal("page or caller-selected paths triggered an upstream fetch")
	}
}

func TestPlaybackHarnessPreservesBoundedRangeIdentityWithoutCredentials(t *testing.T) {
	const payload = "0123456789"
	origin, _ := playbackFixtureHarness(t, "https://media.example.com/movie.mp4", func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Range") != "bytes=100-109" || req.Header.Get("If-Range") != "fixture-etag" {
			t.Error("relay lost requested Range/If-Range")
		}
		for _, name := range []string{"Cookie", "Authorization", "Referer", "X-Private-Header"} {
			if req.Header.Get(name) != "" {
				t.Error("relay forwarded a client credential or tracking header")
			}
		}
		response := playbackFixtureResponse(206, "video/mp4", []byte(payload))
		response.Header.Set("Content-Range", "bytes 100-109/9000")
		response.Header.Set("Accept-Ranges", "bytes")
		response.Header.Set("Set-Cookie", "not-for-browser=1")
		return response, nil
	})
	status, headers, body := playbackHarnessGET(t, origin, "/media", http.Header{"Range": {"bytes=100-109"}, "If-Range": {"fixture-etag"}, "Cookie": {"private=1"}, "Authorization": {"Bearer fixture"}, "Referer": {"https://private.example.com/"}, "X-Private-Header": {"private"}})
	if status != 206 || string(body) != payload || headers.Get("Content-Range") != "bytes 100-109/9000" || headers.Get("Content-Length") != "10" || headers.Get("Accept-Ranges") != "bytes" || headers.Get("Set-Cookie") != "" {
		t.Fatal("relay changed the fixed media resource or its byte-range identity")
	}
}

func TestPlaybackHarnessHLSSelectsLowestVariantAndResolvesFinalBase(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	origin, _ := playbackFixtureHarness(t, "https://media.example.com/original/master.m3u8", func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		paths = append(paths, req.URL.Host+req.URL.Path)
		mu.Unlock()
		switch req.URL.Host + req.URL.Path {
		case "media.example.com/original/master.m3u8":
			response := playbackFixtureResponse(302, "text/plain", nil)
			response.Header.Set("Location", "https://cdn.example.com/final/master.m3u8")
			return response, nil
		case "cdn.example.com/final/master.m3u8":
			return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5000000\nhigh.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=64000\nlow.m3u8\n")), nil
		case "cdn.example.com/final/low.m3u8":
			return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:2,\nsegment.ts\n")), nil
		case "cdn.example.com/final/init.mp4", "cdn.example.com/final/key.bin", "cdn.example.com/final/segment.ts":
			return playbackFixtureResponse(200, "application/octet-stream", bytes.Repeat([]byte{0x47}, 32)), nil
		default:
			t.Error("harness requested an unselected variant or resolved against the wrong base")
			return playbackFixtureResponse(404, "text/plain", nil), nil
		}
	})
	status, _, master := playbackHarnessGET(t, origin, "/media", nil)
	tokens := regexp.MustCompile(`/resource/[0-9a-f]{32}`).FindAllString(string(master), -1)
	if status != 200 || len(tokens) != 1 || bytes.Contains(master, []byte("https://")) || bytes.Contains(master, []byte("high.m3u8")) || bytes.Count(master, []byte("#EXT-X-STREAM-INF:")) != 1 {
		t.Fatal("master playlist was not reduced to one opaque lowest-bandwidth variant")
	}
	status, _, playlist := playbackHarnessGET(t, origin, tokens[0], nil)
	resources := regexp.MustCompile(`/resource/[0-9a-f]{32}`).FindAllString(string(playlist), -1)
	if status != 200 || len(resources) != 3 || bytes.Contains(playlist, []byte("https://")) || !bytes.Contains(playlist, []byte(`URI="/resource/`)) {
		t.Fatal("HLS map/key/segment resources were not rewritten to registered opaque paths")
	}
	for _, resource := range resources {
		if code, _, _ := playbackHarnessGET(t, origin, resource, nil); code != 200 {
			t.Error("registered HLS resource was not relayed")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 6 {
		t.Errorf("unexpected HLS fetch count: %d", len(paths))
	}
}

func TestPlaybackHarnessRejectsPrivateInitialRedirectAndPlaylistTargets(t *testing.T) {
	for _, source := range []string{"http://127.0.0.1/movie.mp4", "http://10.0.0.1/movie.mp4", "file:///movie", "https://user:pass@media.example.com/movie.mp4"} {
		if _, close, err := startMoviePlaybackHarness(context.Background(), VerifiedSource{CandidateSource: CandidateSource{DirectStreamURL: source}}, nil); err == nil || close != nil {
			t.Error("unsafe initial playback source accepted")
		}
	}
	for _, redirect := range []bool{false, true} {
		var calls atomic.Int32
		origin, _ := playbackFixtureHarness(t, "https://media.example.com/master.m3u8", func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			if redirect {
				response := playbackFixtureResponse(302, "text/plain", nil)
				response.Header.Set("Location", "http://127.0.0.1/private-key")
				return response, nil
			}
			return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte("#EXTM3U\n#EXTINF:2,\nhttp://127.0.0.1/private-key\n")), nil
		})
		status, _, body := playbackHarnessGET(t, origin, "/media", nil)
		if status != 502 || calls.Load() != 1 || bytes.Contains(body, []byte("127.0.0.1/private-key")) {
			t.Fatal("private redirect/playlist target escaped relay guarding or error redaction")
		}
	}
}

type playbackCountingReader struct{ bytes *atomic.Int64 }

func (r playbackCountingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0x47
	}
	r.bytes.Add(int64(len(p)))
	return len(p), nil
}

func TestPlaybackHarnessBoundsEachResponseAndSharedAttemptBudget(t *testing.T) {
	var read atomic.Int64
	var requests atomic.Int32
	origin, _ := playbackFixtureHarness(t, "https://media.example.com/movie.mp4", func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Header.Get("Range") != "bytes=0-"+strconv.Itoa(moviePlaybackResponseLimit-1) {
			t.Error("relay did not constrain an open media request")
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-9999999/10000000"}}, ContentLength: 10000000, Body: io.NopCloser(playbackCountingReader{bytes: &read})}, nil
	})
	for i := 0; i < 7; i++ {
		status, headers, body := playbackHarnessGET(t, origin, "/media", nil)
		if len(body) > moviePlaybackResponseLimit || read.Load() > moviePlaybackByteLimit {
			t.Fatal("relay exceeded per-response or per-attempt byte budget")
		}
		if status == 206 && headers.Get("Content-Range") != "bytes 0-"+strconv.Itoa(len(body)-1)+"/10000000" {
			t.Fatal("bounded partial response lost the original resource size/offset")
		}
	}
	if read.Load() != moviePlaybackByteLimit || requests.Load() != 6 {
		t.Fatalf("budget did not stop subsequent upstream reads: bytes=%d requests=%d", read.Load(), requests.Load())
	}
}

func TestPlaybackHarnessIgnoredRangeDoesNotClaimTruncatedCompleteMovie(t *testing.T) {
	for _, length := range []int64{10000000, -1} {
		var read atomic.Int64
		origin, _ := playbackFixtureHarness(t, "https://media.example.com/movie.mp4", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"video/mp4"}}, ContentLength: length, Body: io.NopCloser(playbackCountingReader{bytes: &read})}, nil
		})
		status, headers, body := playbackHarnessGET(t, origin, "/media", nil)
		if length > 0 {
			if status != 206 || headers.Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", moviePlaybackResponseLimit-1, length) || len(body) != moviePlaybackResponseLimit {
				t.Fatal("ignored Range did not retain the full resource's identity")
			}
		} else if status != 502 {
			t.Fatal("unknown-length truncated movie was presented as a complete file")
		}
	}
}

func TestPlaybackHarnessRegistryAndPlaylistLimits(t *testing.T) {
	h := &moviePlaybackHarness{targets: make(map[string]string), ids: make(map[string]string)}
	base, _ := url.Parse("https://media.example.com/playlist.m3u8")
	for i := 0; i < moviePlaybackTargetLimit; i++ {
		if _, err := h.register(base, fmt.Sprintf("segment-%d.ts", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.register(base, "segment-over-limit.ts"); err == nil {
		t.Fatal("resource registry exceeded its fixed target limit")
	}
	if _, err := h.register(base, "segment-0.ts"); err != nil || len(h.targets) != moviePlaybackTargetLimit {
		t.Fatal("repeated playlist target was not reused")
	}
	var read atomic.Int64
	origin, _ := playbackFixtureHarness(t, "https://media.example.com/master.m3u8", func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/vnd.apple.mpegurl"}}, Body: io.NopCloser(playbackCountingReader{bytes: &read})}, nil
	})
	if status, _, _ := playbackHarnessGET(t, origin, "/media", nil); status != 502 || read.Load() > moviePlaybackPlaylistLimit+1 {
		t.Fatal("playlist response exceeded its bounded read size")
	}
}

func TestPlaybackHarnessPreservesLongPlaylistDurationWithoutRegisteringWholeMovie(t *testing.T) {
	h := &moviePlaybackHarness{targets: make(map[string]string), ids: make(map[string]string)}
	base, _ := url.Parse("https://media.example.com/playlist.m3u8")
	var playlist strings.Builder
	playlist.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:14\n")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&playlist, "#EXTINF:6,\nsegment-%d.ts\n", i)
	}
	playlist.WriteString("#EXT-X-ENDLIST\n")
	rewritten, err := h.rewritePlaylist([]byte(playlist.String()), base)
	if err != nil || len(h.targets) != 4 || bytes.Count(rewritten, []byte("#EXTINF:")) != 1000 || bytes.Count(rewritten, []byte("#EXT-X-ENDLIST")) != 1 || !bytes.Contains(rewritten, []byte("#EXT-X-MEDIA-SEQUENCE:14")) || bytes.Count(rewritten, []byte(moviePlaybackUnused)) != 996 {
		t.Fatal("long playlist lost its declared duration or registered unsampled movie resources")
	}
}

func TestPlaybackHarnessCloseCancelsInflightUpstream(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	origin, stop := playbackFixtureHarness(t, "https://media.example.com/movie.mp4", func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, _ := http.Get(origin + "/media")
		if response != nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upstream fixture was not reached")
	}
	stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("closing harness left an upstream request running")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing harness left a downstream request running")
	}
}

func TestPlaybackHarnessLocalHLSArtifactIsBoundedAndOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playback-hls.min.js")
	if err := os.WriteFile(path, []byte("/* fixture */ window.Hls={};"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(readMoviePlaybackHLSScript(path)) == 0 {
		t.Fatal("installed local player artifact was not loaded")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), moviePlaybackScriptLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	if readMoviePlaybackHLSScript(path) != nil || readMoviePlaybackHLSScript(path+"-missing") != nil {
		t.Fatal("oversized or missing player artifact was accepted")
	}
	page := string(moviePlaybackHTML("fixture-nonce", true, false))
	if strings.Contains(page, `<script src="/hls.js">`) || !strings.Contains(page, "unsupported_hls") || !strings.Contains(page, "enableWorker:false") || !strings.Contains(page, "__itembaPlaybackProtocol='hls'") {
		t.Fatal("missing HLS runtime did not preserve an explicit unsupported state")
	}
}

func TestPlaybackHarnessPrefersOwnedMSEBeforeNativeHLSFallback(t *testing.T) {
	page := string(moviePlaybackHTML("fixture-nonce", true, true))
	mse := strings.Index(page, "typeof Hls!=='undefined' && Hls.isSupported()")
	native := strings.Index(page, "video.canPlayType('application/vnd.apple.mpegurl')")
	if mse < 0 || native < mse || !strings.Contains(page, "enableWorker:false") {
		t.Fatal("a browser's incomplete native HLS path displaced the owned MSE decoder")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Fatal("HLS decoder fallback introduced an external player dependency")
	}
}

func TestPlaybackHarnessSelectsVideoBeforeCheaperAudioOnlyRendition(t *testing.T) {
	master := []string{"#EXTM3U", `#EXT-X-STREAM-INF:BANDWIDTH=74000,CODECS="mp4a.40.2"`, "audio.m3u8", `#EXT-X-STREAM-INF:BANDWIDTH=272000,RESOLUTION=416x234,CODECS="avc1.42c00d,mp4a.40.5"`, "video.m3u8", `#EXT-X-STREAM-INF:BANDWIDTH=1200000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2"`, "large.m3u8"}
	selected := strings.Join(lowestPlaybackVariant(master), "\n")
	if !strings.Contains(selected, "video.m3u8") || strings.Contains(selected, "audio.m3u8") || strings.Contains(selected, "large.m3u8") {
		t.Fatal("an audio-only rendition displaced the cheapest declared video track")
	}
	if lowestPlaybackVariant([]string{"#EXTM3U", `#EXT-X-STREAM-INF:BANDWIDTH=74000,CODECS="mp4a.40.2"`, "audio.m3u8"}) != nil {
		t.Fatal("an explicitly audio-only master qualified for a movie sample")
	}
	for _, tag := range []string{`#EXT-X-STREAM-INF:BANDWIDTH=200000,CODECS="avc1.42c00d,mp4a.40.2"`, `#EXT-X-STREAM-INF:BANDWIDTH=200000, RESOLUTION=416x234`} {
		if playbackVariantVideoClass(tag) != 2 {
			t.Fatal("declared video track was not recognized")
		}
	}
}

func TestPlaybackHarnessHandlesLargeSignedFeaturePlaylistWithinTotalBudget(t *testing.T) {
	var playlist strings.Builder
	playlist.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for i := 0; i < 1580; i++ {
		fmt.Fprintf(&playlist, "#EXTINF:4,\nhttps://media.example.com/segment-%d.ts?signature=%s\n", i, strings.Repeat("x", 520))
	}
	playlist.WriteString("#EXT-X-ENDLIST\n")
	if playlist.Len() <= 256<<10 || playlist.Len() >= moviePlaybackPlaylistLimit {
		t.Fatal("fixture is not a bounded, large signed movie playlist")
	}
	base, _ := url.Parse("https://media.example.com/feature.m3u8")
	h := &moviePlaybackHarness{targets: map[string]string{}, ids: map[string]string{}}
	result, err := h.rewritePlaylist([]byte(playlist.String()), base)
	if err != nil || len(h.targets) != 4 || strings.Count(string(result), "#EXTINF:") != 1580 || bytes.Contains(result, []byte("signature=")) || moviePlaybackByteLimit != 6<<20 {
		t.Fatal("signed feature playlist changed duration, exposed sources or broadened media budget")
	}
}

func protectedPlaybackFixtureHarness(t *testing.T, raw string, protection *shield.Shield, transport playbackHarnessTransport) (string, func() string) {
	t.Helper()
	origin, stop, outcome, err := startProtectedMoviePlaybackHarness(context.Background(), VerifiedSource{CandidateSource: CandidateSource{DirectStreamURL: raw}}, &http.Client{Transport: transport}, protection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return origin, outcome
}

func TestProtectedPlaybackHarnessBlocksInitialMediaBeforeNetworkAndRedactsTelemetry(t *testing.T) {
	for _, useDefault := range []bool{false, true} {
		protection := shield.New()
		raw := "https://blocked-stream.example.com/movie.mp4?signed=private-fixture"
		protection.Engine().AddCustomRule("||blocked-stream.example.com^")
		if useDefault {
			protection = nil
			raw = "https://doubleclick.net/movie.mp4?signed=private-fixture"
		}
		var requests atomic.Int32
		origin, stop, outcome, err := startProtectedMoviePlaybackHarness(context.Background(), VerifiedSource{CandidateSource: CandidateSource{DirectStreamURL: raw}}, &http.Client{Transport: playbackHarnessTransport(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return playbackFixtureResponse(200, "video/mp4", []byte("not fetched")), nil
		})}, protection)
		if err == nil || origin != "" || stop != nil || outcome == nil || outcome() != "blocked_with_protection" || requests.Load() != 0 || strings.Contains(err.Error(), "private-fixture") {
			t.Fatal("initial protected endpoint was fetched, retried, or exposed sensitive request details")
		}
		if protection != nil {
			snapshot := protection.Snapshot()
			if snapshot.TotalBlocked != 1 || len(snapshot.RecentEvents) != 1 || strings.Contains(snapshot.RecentEvents[0].Target, "?") {
				t.Fatal("server-side protection must record a query-redacted blocked event")
			}
		}
	}
}

func TestProtectedPlaybackHarnessBlocksRedirectBeforeDestinationFetch(t *testing.T) {
	protection := shield.New()
	protection.Engine().AddCustomRule("||blocked-stream.example.com^")
	var requests atomic.Int32
	origin, outcome := protectedPlaybackFixtureHarness(t, "https://media.example.com/movie.mp4", protection, func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		response := playbackFixtureResponse(302, "text/plain", nil)
		response.Header.Set("Location", "https://blocked-stream.example.com/normal-movie.mp4")
		return response, nil
	})
	status, _, body := playbackHarnessGET(t, origin, "/media", nil)
	if status != 502 || requests.Load() != 1 || outcome() != "blocked_with_protection" || bytes.Contains(body, []byte("blocked-stream")) {
		t.Fatal("blocked redirected media was fetched, retried or exposed")
	}
}

func TestProtectedPlaybackHarnessInspectsNestedManifestKeysAndSegments(t *testing.T) {
	for name, playlist := range map[string]string{
		"nested manifest":         "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nhttps://blocked-stream.example.com/child.m3u8\n",
		"encryption key":          "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://blocked-stream.example.com/key.bin\"\n#EXTINF:6,\nsegment.ts\n",
		"media segment":           "#EXTM3U\n#EXTINF:6,\nhttps://blocked-stream.example.com/segment.ts\n",
		"unsampled later segment": "#EXTM3U\n#EXTINF:6,\n1.ts\n#EXTINF:6,\n2.ts\n#EXTINF:6,\n3.ts\n#EXTINF:6,\n4.ts\n#EXTINF:6,\nhttps://blocked-stream.example.com/later.ts\n#EXT-X-ENDLIST\n",
	} {
		t.Run(name, func(t *testing.T) {
			protection := shield.New()
			protection.Engine().AddCustomRule("||blocked-stream.example.com^")
			var requests atomic.Int32
			origin, outcome := protectedPlaybackFixtureHarness(t, "https://media.example.com/master.m3u8", protection, func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte(playlist)), nil
			})
			status, _, body := playbackHarnessGET(t, origin, "/media", nil)
			if status != 502 || requests.Load() != 1 || outcome() != "blocked_with_protection" || bytes.Contains(body, []byte("blocked-stream")) {
				t.Fatal("blocked playlist dependency escaped request guards")
			}
			if status, _, _ := playbackHarnessGET(t, origin, "/media", nil); status != 502 || requests.Load() != 1 {
				t.Fatal("a blocked sample retried without protection")
			}
		})
	}
}

func TestProtectedPlaybackHarnessInspectsChildPlaylistAndFetchTimeRuleChanges(t *testing.T) {
	for _, changeAtFetch := range []bool{false, true} {
		protection := shield.New()
		if !changeAtFetch {
			protection.Engine().AddCustomRule("||blocked-stream.example.com^")
		}
		var requests atomic.Int32
		origin, outcome := protectedPlaybackFixtureHarness(t, "https://media.example.com/master.m3u8", protection, func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			if req.URL.Path == "/master.m3u8" {
				return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nhttps://nested.example.com/child.m3u8\n")), nil
			}
			return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte("#EXTM3U\n#EXTINF:6,\nhttps://blocked-stream.example.com/segment.ts\n")), nil
		})
		_, _, master := playbackHarnessGET(t, origin, "/media", nil)
		tokens := regexp.MustCompile(`/resource/[0-9a-f]{32}`).FindAllString(string(master), -1)
		if len(tokens) != 1 {
			t.Fatal("expected one allowed child playlist token")
		}
		if changeAtFetch {
			protection.Engine().AddCustomRule("||nested.example.com^")
		}
		status, _, _ := playbackHarnessGET(t, origin, tokens[0], nil)
		wantRequests := int32(2)
		if changeAtFetch {
			wantRequests = 1
		}
		if status != 502 || requests.Load() != wantRequests || outcome() != "blocked_with_protection" {
			t.Fatal("registered child URL bypassed current protection rules or blocked nested segment")
		}
	}
}

func TestProtectedPlaybackHarnessRejectsExplicitAdTimelineAndDRMWithoutRewritingThemAway(t *testing.T) {
	for name, marker := range map[string]string{
		"SCTE35 splice":             "#EXT-OATCLS-SCTE35:provider-splice",
		"cue preroll":               "#EXT-X-CUE-OUT:30",
		"SCTE date range":           `#EXT-X-DATERANGE:ID="break-1",SCTE35-OUT="0xFC"`,
		"ad date range":             `#EXT-X-DATERANGE:ID="ad-1",CLASS="com.provider.adbreak",START-DATE="2026-01-01T00:00:00Z"`,
		"spaced ad date range":      `#EXT-X-DATERANGE: ID="break-1", CLASS="com.provider.adbreak", START-DATE="2026-01-01T00:00:00Z"`,
		"interstitial asset":        `#EXT-X-DATERANGE:ID="insert-1",CLASS="com.apple.hls.interstitial",X-ASSET-URI="asset.m3u8"`,
		"spaced interstitial asset": `#EXT-X-DATERANGE: ID="insert-1", X-ASSET-URI="asset.m3u8"`,
	} {
		t.Run(name, func(t *testing.T) {
			assertPlaybackPlaylistRefused(t, marker, "content_unconfirmed")
		})
	}
	for name, marker := range map[string]string{
		"Widevine":               `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="key.bin",KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"`,
		"FairPlay":               `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://license",KEYFORMAT="com.apple.streamingkeydelivery"`,
		"PlayReady session":      `#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,URI="key.bin",KEYFORMAT="com.microsoft.playready"`,
		"nonidentity AES key":    `#EXT-X-KEY:METHOD=AES-128,URI="key.bin",KEYFORMAT="provider-license"`,
		"spaced nonidentity key": `#EXT-X-KEY:METHOD=AES-128, KEYFORMAT="com.widevine",URI="https://license.example/key"`,
		"spaced method and key":  `#EXT-X-KEY: METHOD=AES-128, KEYFORMAT="com.widevine", URI="https://license.example/key"`,
		"conflicting methods":    `#EXT-X-KEY:METHOD=SAMPLE-AES,METHOD=NONE,URI="key.bin"`,
	} {
		t.Run(name, func(t *testing.T) {
			assertPlaybackPlaylistRefused(t, marker, "drm_required")
		})
	}
	if reason := playbackPlaylistProtectionReason("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",KEYFORMAT=\"identity\"\n#EXT-X-DATERANGE:ID=\"chapter-1\",CLASS=\"chapter\"\n#EXTINF:6,\nmovie.ts\n"); reason != "" {
		t.Fatal("clear identity AES-128 or ordinary chapter metadata was incorrectly labeled DRM/ad insertion")
	}
}

func assertPlaybackPlaylistRefused(t *testing.T, marker, reason string) {
	t.Helper()
	var requests atomic.Int32
	origin, outcome := protectedPlaybackFixtureHarness(t, "https://media.example.com/movie.m3u8", nil, func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		playlist := "#EXTM3U\n" + marker + "\n#EXTINF:6000,\nmovie.ts\n#EXT-X-ENDLIST\n"
		return playbackFixtureResponse(200, "application/vnd.apple.mpegurl", []byte(playlist)), nil
	})
	status, _, body := playbackHarnessGET(t, origin, "/media", nil)
	if status != 502 || requests.Load() != 1 || outcome() != reason || bytes.Contains(body, []byte(marker)) || bytes.Contains(body, []byte("movie.ts")) {
		t.Fatalf("ad/DRM timeline reached the browser or lost its reason: status=%d requests=%d reason=%s", status, requests.Load(), outcome())
	}
}
