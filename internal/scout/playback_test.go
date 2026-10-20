package scout

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/cdp"
)

func freePlaybackFixture() VerifiedSource {
	return VerifiedSource{
		CandidateSource: CandidateSource{ID: "ref-notld-1968", URL: "https://archive.org/embed/night_of_the_living_dead", DirectStreamURL: "https://archive.org/download/night_of_the_living_dead/night_of_the_living_dead_512kb.mp4", SourceType: "Public Domain", Provider: "Internet Archive Cinema", AccessType: "free", Duration: "1:35:52"},
		Verified:        true, VerificationStatus: "media_reachable",
	}
}

func playbackStateFixture(origin string) playbackSnapshot {
	return playbackSnapshot{CurrentTime: .2, ReadyState: 4, PlaybackRate: 1, Width: 640, Height: 480, DecodedFrames: 5, Duration: 5752, Source: origin + "/media", Protocol: "native"}
}

func TestEligibleFreePlaybackRequiresTrustedReachableFreeCatalog(t *testing.T) {
	base := freePlaybackFixture()
	if !eligibleFreePlayback(base) {
		t.Fatal("trusted reachable free movie rejected")
	}
	mutations := map[string]func(*VerifiedSource){
		"paid":              func(s *VerifiedSource) { s.AccessType = "subscription" },
		"unknown":           func(s *VerifiedSource) { s.AccessType = "unknown" },
		"listing":           func(s *VerifiedSource) { s.ListingOnly = true },
		"page only":         func(s *VerifiedSource) { s.VerificationStatus = "page_reachable" },
		"unverified":        func(s *VerifiedSource) { s.Verified = false },
		"no media":          func(s *VerifiedSource) { s.DirectStreamURL = "" },
		"unknown media":     func(s *VerifiedSource) { s.DirectStreamURL = "https://untrusted.example.net/movie.mp4" },
		"archive lookalike": func(s *VerifiedSource) { s.DirectStreamURL = "https://archive.org.evil.net/download/movie.mp4" },
		"arbitrary type":    func(s *VerifiedSource) { s.SourceType = "Open Web" },
		"arbitrary id":      func(s *VerifiedSource) { s.ID = "url-test" },
		"private media":     func(s *VerifiedSource) { s.DirectStreamURL = "http://127.0.0.1/movie.mp4" },
		"preview route":     func(s *VerifiedSource) { s.DirectStreamURL = "https://archive.org/services/img/movie" },
		"user info":         func(s *VerifiedSource) { s.DirectStreamURL = "https://user:password@archive.org/download/movie.mp4" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			source := base
			mutate(&source)
			if eligibleFreePlayback(source) {
				t.Fatal("ineligible source accepted")
			}
		})
	}
}

func TestFreePlaybackHarnessRequiresExplicitLoopbackOrigin(t *testing.T) {
	if origin, ok := playbackHarnessOrigin("http://127.0.0.1:12345/"); !ok || origin != "http://127.0.0.1:12345" {
		t.Fatalf("valid loopback origin rejected: %q, %t", origin, ok)
	}
	for _, raw := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://127.0.0.2:1234", "http://127.0.0.1", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?target=https://external.net", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234#fragment"} {
		if _, ok := playbackHarnessOrigin(raw); ok {
			t.Errorf("unsafe harness origin accepted: %s", raw)
		}
	}
}

func TestPlaybackNetworkAllowlistOnlyOwnedHarnessResources(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	for _, check := range []struct {
		url, kind string
		allowed   bool
	}{
		{origin + "/", "Document", true},
		{origin + "/media", "Media", true},
		{origin + "/media", "XHR", true},
		{origin + "/hls.js", "Script", true},
		{origin + "/resource/0123456789abcdef0123456789abcdef", "XHR", true},
		{origin + "/resource/0123456789abcdef0123456789abcdef", "Script", false},
		{origin + "/resource/../../private", "XHR", false},
		{origin + "/resource/https%3A%2F%2Fexample.net", "XHR", false},
		{origin + "/media?target=https://example.net", "Media", false},
		{origin + "/media", "Document", false},
		{origin + "/hls.js", "XHR", false},
		{origin + "/favicon.ico", "Other", false},
		{"http://127.0.0.1:4321/media", "Media", false},
		{"https://archive.org/download/movie.mp4", "Media", false},
		{"https://tracker.net/collect", "XHR", false},
		{"file:///C:/private.txt", "Document", false},
		{"data:video/mp4;base64,AAAA", "Media", false},
	} {
		if got := playbackHarnessRequestAllowed(origin, check.url, check.kind); got != check.allowed {
			t.Errorf("request %s (%s): got %t want %t", check.url, check.kind, got, check.allowed)
		}
	}
}

func TestPlaybackProgressRequiresRealTimeAndDecodedFrames(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	start := time.Now()
	var progress playbackProgress
	state := playbackStateFixture(origin)
	for i := 0; i <= 5; i++ {
		state.CurrentTime = .2 + float64(i)*.2
		state.DecodedFrames = 5 + i*5
		confirmed, detail := progress.observe(state, start.Add(time.Duration(i)*200*time.Millisecond), origin, 5752)
		if detail != "" || confirmed != (i == 5) {
			t.Fatalf("sample %d: confirmed=%t detail=%q", i, confirmed, detail)
		}
	}
}

func TestPlaybackProgressNeverAcceptsEventsFrozenFramesOrTimeJumps(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	for _, test := range []struct {
		name   string
		mutate func(*playbackSnapshot, int)
	}{
		{"no frames", func(s *playbackSnapshot, i int) { s.DecodedFrames = 0 }},
		{"frozen frames", func(s *playbackSnapshot, i int) { s.DecodedFrames = 5 }},
		{"frozen time", func(s *playbackSnapshot, i int) { s.CurrentTime = .2 }},
		{"audio only", func(s *playbackSnapshot, i int) { s.Width, s.Height = 0, 0 }},
		{"playing event only", func(s *playbackSnapshot, i int) { s.ReadyState = 1 }},
		{"paused", func(s *playbackSnapshot, i int) { s.Paused = true }},
		{"ended", func(s *playbackSnapshot, i int) { s.Ended = true }},
		{"seek", func(s *playbackSnapshot, i int) { s.Seeking = i > 0 }},
		{"seek between samples", func(s *playbackSnapshot, i int) { s.SeekCount = i }},
		{"rate trick", func(s *playbackSnapshot, i int) { s.PlaybackRate = 2 }},
		{"rate trick between samples", func(s *playbackSnapshot, i int) { s.RateChanges = i }},
		{"time jump", func(s *playbackSnapshot, i int) { s.CurrentTime = .2 + float64(i)*3 }},
		{"wrong source", func(s *playbackSnapshot, i int) { s.Source = "https://tracker.net/movie.mp4" }},
		{"clip duration", func(s *playbackSnapshot, i int) { s.Duration = 90 }},
		{"infinite duration", func(s *playbackSnapshot, i int) { s.Duration = math.Inf(1) }},
		{"unknown duration", func(s *playbackSnapshot, i int) { s.Duration = 0 }},
		{"not beginning", func(s *playbackSnapshot, i int) { s.CurrentTime += 30 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var progress playbackProgress
			start := time.Now()
			for i := 0; i < 8; i++ {
				state := playbackStateFixture(origin)
				state.CurrentTime += float64(i) * .2
				state.DecodedFrames += i * 5
				test.mutate(&state, i)
				confirmed, _ := progress.observe(state, start.Add(time.Duration(i)*200*time.Millisecond), origin, 5752)
				if confirmed {
					t.Fatal("unsupported evidence confirmed playback")
				}
			}
		})
	}
}

func TestPlaybackProgressAllowsOnlyOwnedHLSBlob(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	state := playbackStateFixture(origin)
	state.Protocol = "hls"
	state.Source = "blob:" + origin + "/owned-media-source"
	var progress playbackProgress
	if _, detail := progress.observe(state, time.Now(), origin, 5752); detail != "" {
		t.Fatal(detail)
	}
	for _, raw := range []string{"blob:https://archive.org/external", "blob:http://127.0.0.1:12345/external", origin + "/media"} {
		var p playbackProgress
		state.Source = raw
		if _, detail := p.observe(state, time.Now(), origin, 5752); detail == "" {
			t.Errorf("non-owned HLS source accepted: %s", raw)
		}
	}
}

type fakePlaybackBrowser struct {
	handler       cdp.EventHandler
	calls         []string
	asyncCalls    []string
	failCommand   string
	mediaResponse bool
	evaluations   int
	origin        string
	state         playbackSnapshot
	// evolve, when set, lets a test change the page's state by evaluation,
	// as the audio page's fingerprint work finishes while playback runs.
	evolve func(state *playbackSnapshot, evaluation int)
}

func (b *fakePlaybackBrowser) OnEvent(h cdp.EventHandler) { b.handler = h }
func (b *fakePlaybackBrowser) CallAsync(method string, params any) error {
	b.asyncCalls = append(b.asyncCalls, method)
	return nil
}
func (b *fakePlaybackBrowser) Call(ctx context.Context, method string, params any, result any) error {
	b.calls = append(b.calls, method)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if method == b.failCommand {
		return errors.New("fixture command failed")
	}
	if method == "Page.navigate" && b.mediaResponse {
		data, _ := json.Marshal(map[string]any{"response": map[string]any{"url": b.origin + "/media", "status": 206}})
		b.handler("Network.responseReceived", data)
	}
	if method == "Runtime.evaluate" {
		b.evaluations++
		state := b.state
		state.CurrentTime += float64(b.evaluations-1) * .2
		state.DecodedFrames += (b.evaluations - 1) * 5
		if b.evolve != nil {
			b.evolve(&state, b.evaluations)
		}
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"value": state}})
		return json.Unmarshal(data, result)
	}
	return nil
}

func TestPlaybackSamplerFailsClosedOnProtectionFailure(t *testing.T) {
	browser := &fakePlaybackBrowser{failCommand: "Fetch.enable"}
	result := runFreePlaybackSample(context.Background(), browser, freePlaybackFixture(), "http://127.0.0.1:1234", &PlaybackCheck{})
	if result.PlaybackConfirmed || !strings.Contains(result.Detail, "enforce") {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, call := range browser.calls {
		if call == "Page.navigate" {
			t.Fatal("harness navigation began without request interception")
		}
	}
}

func TestPlaybackSamplerRequiresSuccessfulMediaResponse(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	browser := &fakePlaybackBrowser{origin: origin, state: playbackStateFixture(origin)}
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	result := runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if result.PlaybackConfirmed || result.Status != "not_started" || browser.evaluations == 0 {
		t.Fatalf("missing media response was accepted: %+v", result)
	}
	data, _ := json.Marshal(map[string]any{"requestId": "tracker-request", "resourceType": "XHR", "request": map[string]any{"url": "https://tracker.net/pixel"}})
	browser.handler("Fetch.requestPaused", data)
	if len(browser.asyncCalls) != 1 || browser.asyncCalls[0] != "Fetch.failRequest" {
		t.Fatal("external request was not blocked")
	}
}

func TestPlaybackSamplerConfirmsDecodedBeginningSample(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	browser := &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: playbackStateFixture(origin)}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	result := runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if !result.PlaybackConfirmed || result.Status != "started" || result.SecondsAdvanced < 1 || result.DecodedFrames < 2 || result.Width != 640 || result.DurationSeconds != 5752 {
		t.Fatalf("valid playback evidence was not confirmed: %+v", result)
	}
}

func TestPlaybackErrorFourDoesNotClaimUnsupportedCodec(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	state := playbackStateFixture(origin)
	state.ErrorCode = 4 // Chromium also reports this for a failed HTTP response.
	browser := &fakePlaybackBrowser{origin: origin, state: state}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if result.Status != "not_started" || result.PlaybackConfirmed || strings.Contains(strings.ToLower(result.Detail), "unsupported") {
		t.Fatalf("generic load failure mislabeled as codec failure: %+v", result)
	}
}
