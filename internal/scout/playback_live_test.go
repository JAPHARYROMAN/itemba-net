package scout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicit opt-in only: this test downloads a bounded public movie sample and
// launches a disposable local Chromium profile. No API keys or account cookies
// are loaded. Ordinary test runs never contact these public endpoints.
func TestLiveFreeMoviePlaybackSintel(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_PLAYBACK_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_PLAYBACK_TEST=1 to run the bounded public sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	var events []any
	record := func(value any) { mu.Lock(); events = append(events, value); mu.Unlock() }
	client := newMovieAvailabilityClient()
	client.Transport = livePlaybackTransport{base: client.Transport, record: record}
	scout := &Scout{availabilityHTTPClient: client}
	country, _ := scout.availabilityCheckCountry(ctx)
	t.Logf("check country: %q", country)
	source := VerifiedSource{
		CandidateSource: CandidateSource{ID: "ia-2-sintel_202409", Title: "Sintel", URL: "https://archive.org/embed/sintel_202409", DirectStreamURL: "https://archive.org/download/sintel_202409/Sintel%2FSintel.mp4", Provider: "Internet Archive", SourceType: "Archive video", AccessType: "free", Duration: "888.06"},
		Verified:        true, VerificationStatus: "media_reachable", StreamFormat: "Direct MP4",
	}
	if !eligibleFreePlayback(source) {
		t.Fatal("public catalog sample failed eligibility")
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, freePlaybackDeadline)
	defer checkCancel()
	origin, closeHarness, err := startMoviePlaybackHarness(checkCtx, source, client)
	if err != nil {
		t.Fatal("local harness could not be prepared")
	}
	defer closeHarness()
	browser, err := launchFreePlaybackBrowser(checkCtx)
	if err != nil {
		t.Fatal("isolated browser could not be launched")
	}
	defer func() {
		browser.Close()
		// cdp.Close retries profile removal asynchronously. Keep the test process
		// alive until that cleanup finishes, rather than abandoning its goroutine.
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, err := os.Stat(browser.UserDataDir)
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil {
				t.Error("could not verify disposable playback profile cleanup")
				return
			}
			if time.Now().After(deadline) {
				t.Error("disposable playback profile remained after browser shutdown")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	browser.OnEvent(func(method string, data json.RawMessage) {
		switch method {
		case "Network.responseReceived":
			var p struct {
				Response struct {
					URL     string         `json:"url"`
					Status  int            `json:"status"`
					Headers map[string]any `json:"headers"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &p) == nil && strings.HasPrefix(p.Response.URL, origin+"/") {
				record(map[string]any{"kind": "local_response", "path": strings.TrimPrefix(p.Response.URL, origin), "status": p.Response.Status, "headers": livePlaybackHeaders(p.Response.Headers)})
			}
		case "Network.loadingFailed":
			var p struct {
				Type   string `json:"type"`
				Error  string `json:"errorText"`
				Reason string `json:"blockedReason"`
			}
			if json.Unmarshal(data, &p) == nil {
				record(map[string]any{"kind": "load_failure", "type": p.Type, "error": p.Error, "reason": p.Reason})
			}
		case "Fetch.requestPaused":
			var p struct {
				Type    string `json:"resourceType"`
				Request struct {
					URL     string         `json:"url"`
					Headers map[string]any `json:"headers"`
				} `json:"request"`
			}
			if json.Unmarshal(data, &p) == nil && strings.HasPrefix(p.Request.URL, origin+"/") {
				record(map[string]any{"kind": "local_request", "type": p.Type, "path": strings.TrimPrefix(p.Request.URL, origin), "allowed": playbackHarnessRequestAllowed(origin, p.Request.URL, p.Type), "headers": livePlaybackHeaders(p.Request.Headers)})
			}
		case "Media.playerErrorsRaised":
			var p struct {
				Errors []struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			if json.Unmarshal(data, &p) == nil {
				record(map[string]any{"kind": "native_media_error", "errors": p.Errors})
			}
		}
	})
	_ = browser.Call(checkCtx, "Media.enable", map[string]any{}, nil)
	result := runFreePlaybackSample(checkCtx, browser, source, origin, &PlaybackCheck{Status: "unconfirmed", Method: "isolated_video_sample", RequestedRegion: "TZ", Country: country})
	encoded, _ := json.Marshal(result)
	t.Logf("sample: %s", encoded)
	diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), time.Second)
	defer diagnosticCancel()
	var diagnostic any
	if browser.Call(diagnosticCtx, "Runtime.evaluate", map[string]any{"returnByValue": true, "expression": `(() => { const v=document.querySelector('video'); if(!v)return null; return { errorCode:v.error?v.error.code:0,error:v.error?v.error.message:'',networkState:v.networkState,readyState:v.readyState,currentTime:v.currentTime,duration:Number.isFinite(v.duration)?v.duration:0,width:v.videoWidth,height:v.videoHeight,decodedFrames:v.webkitDecodedFrameCount,canPlayH264:v.canPlayType('video/mp4; codecs="avc1.42E01E, mp4a.40.2"'),buffered:Array.from({length:v.buffered.length},(_,i)=>[v.buffered.start(i),v.buffered.end(i)])}; })()`}, &diagnostic) == nil {
		encoded, _ := json.Marshal(diagnostic)
		t.Logf("native state: %s", encoded)
	}
	mu.Lock()
	encoded, _ = json.Marshal(events)
	mu.Unlock()
	t.Logf("bounded transport events: %s", encoded)
	if !result.PlaybackConfirmed {
		t.Errorf("public sample did not start: %s", result.Detail)
	}
}

type livePlaybackTransport struct {
	base   http.RoundTripper
	record func(any)
}

func (t livePlaybackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if movieHostIs(strings.ToLower(req.URL.Hostname()), "archive.org") {
		event := map[string]any{"kind": "upstream_response", "host": req.URL.Hostname(), "range": req.Header.Get("Range"), "failed": err != nil}
		if response != nil {
			event["status"], event["contentRange"], event["contentType"] = response.StatusCode, response.Header.Get("Content-Range"), response.Header.Get("Content-Type")
			response.Body = &livePlaybackBody{ReadCloser: response.Body, record: t.record, started: time.Now()}
		}
		t.record(event)
	}
	return response, err
}

type livePlaybackBody struct {
	io.ReadCloser
	record  func(any)
	started time.Time
	mu      sync.Mutex
	bytes   int64
	error   string
}

func (b *livePlaybackBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	b.bytes += int64(n)
	if err != nil {
		b.error = "read_error"
		if errors.Is(err, io.EOF) {
			b.error = "eof"
		} else if errors.Is(err, context.DeadlineExceeded) {
			b.error = "deadline"
		} else if errors.Is(err, context.Canceled) {
			b.error = "cancelled"
		}
	}
	b.mu.Unlock()
	return n, err
}

func (b *livePlaybackBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	value := map[string]any{"kind": "upstream_body", "bytesRead": b.bytes, "readEnd": b.error, "elapsedMs": time.Since(b.started).Milliseconds()}
	b.mu.Unlock()
	b.record(value)
	return err
}

func livePlaybackHeaders(headers map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range headers {
		for _, allowed := range []string{"range", "content-range", "content-type", "content-length"} {
			if strings.EqualFold(key, allowed) {
				result[allowed] = value
			}
		}
	}
	return result
}
