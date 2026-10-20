package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/lens"
)

const freePlaybackDeadline = 12 * time.Second

// playbackBrowser deliberately has no dependency on the user's vault or pooled
// discovery tabs. A playback sample always gets its own throwaway profile.
type playbackBrowser interface {
	Call(context.Context, string, any, any) error
	CallAsync(string, any) error
	OnEvent(cdp.EventHandler)
}

type playbackSnapshot struct {
	CurrentTime   float64 `json:"currentTime"`
	ReadyState    int     `json:"readyState"`
	Paused        bool    `json:"paused"`
	Ended         bool    `json:"ended"`
	Seeking       bool    `json:"seeking"`
	SeekCount     int     `json:"seekCount"`
	RateChanges   int     `json:"rateChanges"`
	PlaybackRate  float64 `json:"playbackRate"`
	ErrorCode     int     `json:"errorCode"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	DecodedFrames int     `json:"decodedFrames"`
	Duration      float64 `json:"duration"`
	Source        string  `json:"src"`
	Protocol      string  `json:"protocol"`
	HarnessState  string  `json:"harnessState"`
	// The audio page's fingerprint work: pending, ready with the
	// fingerprint, or failed with a cause. The video page reports none.
	Fingerprint      *lens.Fingerprint `json:"fingerprint"`
	FingerprintState string            `json:"fingerprintState"`
	FingerprintError string            `json:"fingerprintError"`
	// The video page's sampled frames: the grey cells of each frame taken
	// a VideoFrameInterval apart, in order, for the frame fingerprint, and
	// the capture's error when one stopped it. The audio page reports none.
	Frames     []lens.VideoFrame `json:"frames"`
	FrameError string            `json:"frameError"`
}

// videoFrameGrace is how long a confirmed video sample keeps playing to
// gather frames for the fingerprint when it has fewer than
// videoFingerprintFrames, inside the sample's own deadline; and
// videoFingerprintFrames is how many frames the sample is content with.
// Variables so tests can shorten them.
var (
	videoFrameGrace        = 6 * time.Second
	videoFingerprintFrames = 12
)

// attachVideoFingerprint builds the frame fingerprint from the frames the
// page captured and carries it onto the check: computed when the sequence
// is long enough to compare, too_short when it is not, unsupported when
// the page could not read the frames at all.
func attachVideoFingerprint(result *PlaybackCheck, state playbackSnapshot) {
	if len(state.Frames) == 0 && state.FrameError != "" {
		result.Fingerprint, result.FingerprintStatus = nil, "unsupported"
		return
	}
	fp := lens.VideoFingerprint(state.Frames)
	if !fp.Valid() || lens.VideoFrameCount(fp) < lens.MinVideoFrames {
		result.Fingerprint, result.FingerprintStatus = nil, "too_short"
		return
	}
	result.Fingerprint, result.FingerprintStatus = &fp, "computed"
}

// gatherVideoFrames keeps reading the page after a confirmed video sample
// until it has videoFingerprintFrames frames, for at most videoFrameGrace
// and within ctx, then attaches the fingerprint of what it has.
func gatherVideoFrames(ctx context.Context, browser playbackBrowser, snapshotJS string, result *PlaybackCheck, state playbackSnapshot) {
	deadline := time.Now().Add(videoFrameGrace)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for len(state.Frames) < videoFingerprintFrames && state.FrameError == "" {
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			attachVideoFingerprint(result, state)
			return
		case <-ticker.C:
			var response struct {
				Result struct {
					Value *playbackSnapshot `json:"value"`
				} `json:"result"`
			}
			if browser.Call(ctx, "Runtime.evaluate", map[string]any{"expression": snapshotJS, "returnByValue": true}, &response) != nil || response.Result.Value == nil {
				attachVideoFingerprint(result, state)
				return
			}
			state = *response.Result.Value
		}
	}
	attachVideoFingerprint(result, state)
}

// fingerprintGrace is how long a confirmed audio sample waits for the
// page's fingerprint when it is still pending, inside the sample's own
// deadline. A variable so tests can shorten it.
var fingerprintGrace = 4 * time.Second

// fingerprintFailures are the causes the page may report; anything else is
// recorded as failed.
var fingerprintFailures = map[string]bool{"fetch_failed": true, "decode_failed": true, "too_short": true, "unsupported": true}

// attachFingerprint carries the page's fingerprint work onto the check: a
// ready, valid fingerprint with the status computed; a ready but malformed
// one as invalid; a failure by its cause; pending as pending.
func attachFingerprint(result *PlaybackCheck, state playbackSnapshot) {
	switch state.FingerprintState {
	case "ready":
		if state.Fingerprint.Valid() {
			result.Fingerprint, result.FingerprintStatus = state.Fingerprint, "computed"
		} else {
			result.Fingerprint, result.FingerprintStatus = nil, "invalid"
		}
	case "failed":
		result.Fingerprint, result.FingerprintStatus = nil, "failed"
		if fingerprintFailures[state.FingerprintError] {
			result.FingerprintStatus = state.FingerprintError
		}
	case "pending":
		if result.FingerprintStatus == "" {
			result.FingerprintStatus = "pending"
		}
	}
}

// waitForFingerprint keeps reading the page after a confirmed sample while
// its fingerprint is pending, for at most fingerprintGrace and within ctx.
// A pending fingerprint at the end is reported as timed out; the confirmed
// sample stands either way.
func waitForFingerprint(ctx context.Context, browser playbackBrowser, snapshotJS string, result *PlaybackCheck) {
	deadline := time.Now().Add(fingerprintGrace)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for result.FingerprintStatus == "pending" {
		select {
		case <-ctx.Done():
			result.FingerprintStatus = "timeout"
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				result.FingerprintStatus = "timeout"
				return
			}
			var response struct {
				Result struct {
					Value *playbackSnapshot `json:"value"`
				} `json:"result"`
			}
			if browser.Call(ctx, "Runtime.evaluate", map[string]any{"expression": snapshotJS, "returnByValue": true}, &response) != nil {
				result.FingerprintStatus = "timeout"
				return
			}
			if response.Result.Value != nil {
				attachFingerprint(result, *response.Result.Value)
			}
		}
	}
}

// verifyFreeMoviePlayback proves only that a short beginning sample decoded and
// advanced. It does not infer completion, licensing, or future uninterrupted
// playback. The harness relays through the same guarded connection as the
// country check; Chromium never contacts an upstream movie site itself.
func verifyFreeMoviePlayback(ctx context.Context, source VerifiedSource, harnessOrigin, region, country string) *PlaybackCheck {
	result := &PlaybackCheck{
		Status: "unconfirmed", Country: movieCountry(country), RequestedRegion: strings.ToUpper(strings.TrimSpace(region)),
		CheckedAt: time.Now().UTC().Format(time.RFC3339), Method: "isolated_video_sample",
		EvidenceURL: source.URL, Detail: "A short playback sample could not be confirmed.",
	}
	if !eligibleFreePlayback(source) {
		result.Status = "skipped"
		result.Detail = "Automatic playback samples require a reachable free video from a supported movie catalog."
		return result
	}
	origin, ok := playbackHarnessOrigin(harnessOrigin)
	if !ok {
		result.Detail = "The isolated playback harness was unavailable."
		return result
	}
	checkCtx, cancel := context.WithTimeout(ctx, freePlaybackDeadline)
	defer cancel()
	if checkCtx.Err() != nil {
		result.Detail = "The playback sample was cancelled before it started."
		return result
	}
	browser, err := launchFreePlaybackBrowser(checkCtx)
	if err != nil {
		result.Detail = "An isolated browser could not be started for the playback sample."
		return result
	}
	defer browser.Close()
	return runFreePlaybackSample(checkCtx, browser, source, origin, result)
}

// verifyAudioPlayback is the recording's counterpart: a short muted sample in
// the same isolated browser and harness, judged by the audio element's time
// advancing at normal speed rather than by decoded frames. It proves a
// beginning decoded, nothing about the rest of the recording.
func verifyAudioPlayback(ctx context.Context, source VerifiedSource, harnessOrigin, region, country string) *PlaybackCheck {
	result := &PlaybackCheck{
		Status: "unconfirmed", Country: movieCountry(country), RequestedRegion: strings.ToUpper(strings.TrimSpace(region)),
		CheckedAt: time.Now().UTC().Format(time.RFC3339), Method: "isolated_audio_sample",
		EvidenceURL: source.URL, Detail: "A short playback sample could not be confirmed.",
	}
	if !eligibleAudioPlayback(source) {
		result.Status = "skipped"
		result.Detail = "Automatic playback samples require a reachable free recording from a supported audio catalog."
		return result
	}
	origin, ok := playbackHarnessOrigin(harnessOrigin)
	if !ok {
		result.Detail = "The isolated playback harness was unavailable."
		return result
	}
	checkCtx, cancel := context.WithTimeout(ctx, freePlaybackDeadline)
	defer cancel()
	if checkCtx.Err() != nil {
		result.Detail = "The playback sample was cancelled before it started."
		return result
	}
	browser, err := launchFreePlaybackBrowser(checkCtx)
	if err != nil {
		result.Detail = "An isolated browser could not be started for the playback sample."
		return result
	}
	defer browser.Close()
	return runFreePlaybackSample(checkCtx, browser, source, origin, result)
}

// eligibleAudioPlayback admits a recording to the sample: a free, reachable,
// verified Archive audio listing whose file sits under the item its metadata
// evidence names, in a container the console plays, and whose page is that
// item's. Every other audio source waits for its own pairing rule.
func eligibleAudioPlayback(source VerifiedSource) bool {
	if source.Kind != lens.TypeAudio || source.AccessType != "free" || source.ListingOnly || source.VerificationStatus != "media_reachable" || !source.Verified {
		return false
	}
	if source.Provider != "Internet Archive" || source.SourceType != "Archive audio" || !strings.HasPrefix(source.ID, "ia-audio-") || source.AccessBasis != "archive_metadata" {
		return false
	}
	media, mediaOK := safeAvailabilityURL(source.DirectStreamURL)
	page, pageOK := safeAvailabilityURL(source.URL)
	evidence, evidenceOK := safeAvailabilityURL(source.AccessEvidenceURL)
	if !mediaOK || !pageOK || !evidenceOK || media.Hostname() != "archive.org" || page.Hostname() != "archive.org" || evidence.Hostname() != "archive.org" || evidence.RawQuery != "" {
		return false
	}
	id := strings.TrimPrefix(evidence.Path, "/metadata/")
	if !freeArchiveIdentifier.MatchString(id) || evidence.Path != "/metadata/"+id || !strings.HasPrefix(media.Path, "/download/"+id+"/") {
		return false
	}
	if _, playable := audioExtensions[strings.ToLower(path.Ext(media.Path))]; !playable {
		return false
	}
	return page.Path == "/details/"+id || page.Path == "/embed/"+id
}

// expectedAudioDuration is the listed length of the file being sampled: the
// track that carries the direct URL. A recording's Duration is the whole
// item's, so it cannot judge one track. Zero means no length to judge by.
func expectedAudioDuration(source VerifiedSource) float64 {
	for _, track := range source.Tracks {
		if track.URL == source.DirectStreamURL {
			return track.DurationSeconds
		}
	}
	if len(source.Tracks) == 0 {
		return mediaDurationSeconds(source.Duration)
	}
	return 0
}

func launchFreePlaybackBrowser(ctx context.Context) (*cdp.BrowserInstance, error) {
	return cdp.Launch(ctx, cdp.LaunchOptions{
		ID: fmt.Sprintf("playback-%d", time.Now().UnixNano()), Width: 640, Height: 360, MuteAudio: true,
		HostResolver: "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
		ExtraArgs:    []string{"--no-proxy-server", "--disable-quic", "--disable-component-extensions-with-background-pages", "--disable-features=MediaRouter,OptimizationHints,Translate", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp"},
	})
}

func eligibleFreePlayback(source VerifiedSource) bool {
	if source.AccessType != "free" || source.ListingOnly || source.VerificationStatus != "media_reachable" || !source.Verified {
		return false
	}
	mediaURL := source.DirectStreamURL
	if source.providerContentBound {
		mediaURL = source.providerStreamURL
	}
	media, mediaOK := safeAvailabilityURL(mediaURL)
	page, pageOK := safeAvailabilityURL(source.URL)
	if !mediaOK || !pageOK {
		return false
	}
	if source.providerContentBound {
		return providerPlaybackBinding(source.CandidateSource)
	}
	mediaHost := strings.ToLower(media.Hostname())
	pageHost := strings.ToLower(page.Hostname())
	if source.AccessBasis == "archive_metadata" || source.AccessBasis == "peertube_api" || strings.Contains(source.ID, "-resolved-") {
		return resolvedFreePlaybackPair(source.CandidateSource, page, media)
	}
	switch {
	case (source.Provider == "Internet Archive" && source.SourceType == "Archive video" && strings.HasPrefix(source.ID, "ia-")) || (source.Provider == "Internet Archive Cinema" && source.SourceType == "Public Domain" && strings.HasPrefix(source.ID, "ref-")):
		parts := strings.Split(strings.Trim(page.Path, "/"), "/")
		return pageHost == "archive.org" && mediaHost == "archive.org" && len(parts) == 2 && (parts[0] == "embed" || parts[0] == "details") && freeArchiveIdentifier.MatchString(parts[1]) && strings.HasPrefix(media.Path, "/download/"+parts[1]+"/")
	case source.Provider == "Wikimedia Commons" && source.SourceType == "Open media" && strings.HasPrefix(source.ID, "wm-"):
		return pageHost == "commons.wikimedia.org" && mediaHost == "upload.wikimedia.org"
	case source.SourceType == "Federated PeerTube" && strings.HasPrefix(source.ID, "pt-"):
		return pageHost == mediaHost && source.Provider == "PeerTube ("+pageHost+")"
	case source.SourceType == "HLS Reference":
		known := map[string]string{
			"ref-tears-of-steel": "https://demo.unified-streaming.com/k8s/features/stable/video/tears-of-steel/tears-of-steel.ism/.m3u8",
			"ref-big-buck-bunny": "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
			"ref-sintel":         "https://bitdash-a.akamaihd.net/content/sintel/hls/playlist.m3u8",
		}
		return known[source.ID] != "" && source.DirectStreamURL == known[source.ID]
	}
	return false
}

func playbackHarnessOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	if _, err := net.LookupPort("tcp", u.Port()); err != nil {
		return "", false
	}
	return "http://" + u.Host, true
}

func playbackHarnessRequestAllowed(origin, raw, resourceType string) bool {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Scheme+"://"+u.Host != origin {
		return false
	}
	switch u.Path {
	case "/":
		return resourceType == "Document"
	case "/hls.js":
		return resourceType == "Script"
	case "/media":
		return resourceType == "Media" || resourceType == "XHR" || resourceType == "Fetch" || resourceType == "Other"
	}
	if strings.HasPrefix(u.Path, "/resource/") && len(u.Path) == len("/resource/")+32 {
		for _, r := range u.Path[len("/resource/"):] {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return resourceType == "Media" || resourceType == "XHR" || resourceType == "Fetch" || resourceType == "Other"
	}
	return false
}

// Read the native video element, not a provider's custom "playing" flag. All
// code here executes only in the locally served, ITEMBA-owned harness.
const freePlaybackSnapshotJS = `(() => {
	const videos = document.querySelectorAll('video');
	if (videos.length !== 1) return null;
	const v = videos[0];
	const q = typeof v.getVideoPlaybackQuality === 'function' ? v.getVideoPlaybackQuality() : null;
	const decoded = Number.isFinite(v.webkitDecodedFrameCount) ? v.webkitDecodedFrameCount :
		(q && Number.isFinite(q.totalVideoFrames) && Number.isFinite(q.droppedVideoFrames) ? Math.max(0, q.totalVideoFrames-q.droppedVideoFrames) : -1);
	if (typeof window.__itembaCaptureFrame === 'function') window.__itembaCaptureFrame();
	return { currentTime:v.currentTime, readyState:v.readyState, paused:v.paused, ended:v.ended, seeking:v.seeking,
		playbackRate:v.playbackRate, errorCode:v.error ? v.error.code : 0, width:v.videoWidth, height:v.videoHeight,
		decodedFrames:decoded, duration:Number.isFinite(v.duration) ? v.duration : 0, src:v.currentSrc || v.src,
		protocol:window.__itembaPlaybackProtocol || 'direct', harnessState:window.__itembaPlaybackState || '',
		seekCount:window.__itembaPlaybackIntegrity.seeks, rateChanges:window.__itembaPlaybackIntegrity.rateChanges,
		frames:window.__itembaFrames || [], frameError:window.__itembaFrameError || '' };
})()`

// The audio element reports no frames or picture; its time advancing at
// normal speed is the evidence. The same integrity counters apply.
const audioPlaybackSnapshotJS = `(() => {
	const audios = document.querySelectorAll('audio');
	if (audios.length !== 1) return null;
	const a = audios[0];
	return { currentTime:a.currentTime, readyState:a.readyState, paused:a.paused, ended:a.ended, seeking:a.seeking,
		playbackRate:a.playbackRate, errorCode:a.error ? a.error.code : 0, width:0, height:0, decodedFrames:0,
		duration:Number.isFinite(a.duration) ? a.duration : 0, src:a.currentSrc || a.src,
		protocol:window.__itembaPlaybackProtocol || 'direct', harnessState:window.__itembaPlaybackState || '',
		seekCount:window.__itembaPlaybackIntegrity.seeks, rateChanges:window.__itembaPlaybackIntegrity.rateChanges,
		fingerprint:window.__itembaFingerprint || null, fingerprintState:window.__itembaFingerprintState || '', fingerprintError:window.__itembaFingerprintError || '' };
})()`

const freePlaybackIntegrityJS = `(() => {
	Object.defineProperty(window, 'open', { value: () => null, writable: false, configurable: false });
	const integrity = { seeks:0, rateChanges:0 };
	Object.defineProperty(window, '__itembaPlaybackIntegrity', { value:integrity, writable:false, configurable:false });
	document.addEventListener('seeking', e => { if (e.target instanceof HTMLMediaElement) integrity.seeks++; }, true);
	document.addEventListener('ratechange', e => { if (e.target instanceof HTMLMediaElement && e.target.playbackRate !== 1) integrity.rateChanges++; }, true);
})()`

func runFreePlaybackSample(ctx context.Context, browser playbackBrowser, source VerifiedSource, origin string, result *PlaybackCheck) *PlaybackCheck {
	// A recording is sampled in an audio element and judged without frames.
	audio := source.Kind == lens.TypeAudio
	snapshotJS, noun := freePlaybackSnapshotJS, "video"
	if audio {
		snapshotJS, noun = audioPlaybackSnapshotJS, "recording"
	}
	var mediaResponse, segmentResponse, isolationFailed atomic.Bool
	browser.OnEvent(func(method string, data json.RawMessage) {
		switch method {
		case "Fetch.requestPaused":
			var p struct {
				RequestID string `json:"requestId"`
				Type      string `json:"resourceType"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if json.Unmarshal(data, &p) != nil || p.RequestID == "" {
				isolationFailed.Store(true)
				return
			}
			method := "Fetch.failRequest"
			params := map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"}
			if playbackHarnessRequestAllowed(origin, p.Request.URL, p.Type) {
				method = "Fetch.continueRequest"
				params = map[string]any{"requestId": p.RequestID}
			}
			if browser.CallAsync(method, params) != nil {
				isolationFailed.Store(true)
			}
		case "Network.responseReceived":
			var p struct {
				Response struct {
					URL    string `json:"url"`
					Status int    `json:"status"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &p) == nil && (p.Response.Status == 200 || p.Response.Status == 206) {
				if p.Response.URL == origin+"/media" {
					mediaResponse.Store(true)
				}
				if strings.HasPrefix(p.Response.URL, origin+"/resource/") && playbackHarnessRequestAllowed(origin, p.Response.URL, "XHR") {
					segmentResponse.Store(true)
				}
			}
		case "Page.windowOpen":
			// The owned harness never opens windows. Stop if that invariant changes.
			isolationFailed.Store(true)
		case "Target.attachedToTarget", "Target.targetCreated":
			var p struct {
				Target struct {
					ID       string `json:"targetId"`
					OpenerID string `json:"openerId"`
				} `json:"targetInfo"`
			}
			if json.Unmarshal(data, &p) == nil && p.Target.ID != "" && (method == "Target.attachedToTarget" || p.Target.OpenerID != "") {
				// Child targets are paused before executing and immediately closed;
				// none can get a separate, unintercepted network context.
				isolationFailed.Store(true)
				_ = browser.CallAsync("Target.closeTarget", map[string]any{"targetId": p.Target.ID})
			}
		}
	})
	commands := []struct {
		method string
		params any
	}{
		{"Page.enable", map[string]any{}},
		{"Runtime.enable", map[string]any{}},
		{"Network.enable", map[string]any{}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}},
		{"Target.setDiscoverTargets", map[string]any{"discover": true}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": freePlaybackIntegrityJS}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}},
	}
	for _, command := range commands {
		if browser.Call(ctx, command.method, command.params, nil) != nil {
			result.Detail = "The browser could not enforce the isolated playback sample."
			return result
		}
	}
	if browser.Call(ctx, "Page.navigate", map[string]any{"url": origin + "/"}, nil) != nil {
		result.Detail = "The isolated playback harness did not open."
		return result
	}
	evidence := playbackProgress{audio: audio}
	expectedDuration := mediaDurationSeconds(source.Duration)
	if audio {
		expectedDuration = expectedAudioDuration(source)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result.Status = "not_started"
			result.Detail = "Video did not decode and advance within the bounded playback sample; playback remains unconfirmed."
			if audio {
				result.Detail = "The recording did not decode and advance within the bounded playback sample; playback remains unconfirmed."
				if result.FingerprintStatus == "pending" {
					result.FingerprintStatus = "timeout"
				}
			}
			return result
		case now := <-ticker.C:
			if isolationFailed.Load() {
				result.Detail = "The isolated playback sample could not be completed safely."
				return result
			}
			var response struct {
				Result struct {
					Value *playbackSnapshot `json:"value"`
				} `json:"result"`
				Exception json.RawMessage `json:"exceptionDetails"`
			}
			if browser.Call(ctx, "Runtime.evaluate", map[string]any{"expression": snapshotJS, "returnByValue": true}, &response) != nil {
				result.Detail = "The browser stopped responding during the playback sample."
				return result
			}
			if len(response.Exception) > 0 && string(response.Exception) != "null" {
				result.Detail = "The native " + noun + " state could not be read."
				return result
			}
			if response.Result.Value == nil {
				continue
			}
			state := *response.Result.Value
			if audio {
				// The fingerprint is evidence of the file whatever the sample
				// concludes, so it rides on every outcome below.
				attachFingerprint(result, state)
			}
			ownedSource := state.Source == origin+"/media" || state.Protocol == "hls" && strings.HasPrefix(state.Source, "blob:"+origin+"/")
			if ownedSource && state.Duration > 0 && !math.IsNaN(state.Duration) && !math.IsInf(state.Duration, 0) {
				// Native track metadata is useful length evidence even if decode
				// fails. It does not confirm that playback started.
				result.DurationSeconds = math.Round(state.Duration*100) / 100
			}
			if state.HarnessState == "unsupported_hls" {
				result.Status = "unsupported"
				result.Detail = "This browser cannot decode the source's streaming format; playback remains unconfirmed."
				return result
			}
			if state.ErrorCode != 0 {
				result.Status = "not_started"
				result.Detail = "The browser could not load or decode this free " + noun + "; playback remains unconfirmed."
				return result
			}
			if !audio && state.DecodedFrames < 0 {
				result.Status = "unsupported"
				result.Detail = "This browser cannot report decoded video frames; playback remains unconfirmed."
				return result
			}
			if !mediaResponse.Load() || state.Protocol == "hls" && !segmentResponse.Load() {
				continue
			}
			confirmed, detail := evidence.observe(state, now, origin, expectedDuration)
			if detail != "" {
				result.Status = "not_started"
				result.Detail = detail
				return result
			}
			if confirmed {
				result.Status = "started"
				result.PlaybackConfirmed = true
				result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
				result.SecondsAdvanced = math.Round((state.CurrentTime-evidence.start.CurrentTime)*100) / 100
				result.DecodedFrames = state.DecodedFrames - evidence.start.DecodedFrames
				result.Width, result.Height = state.Width, state.Height
				result.DurationSeconds = math.Round(state.Duration*100) / 100
				result.Detail = "A muted beginning sample decoded video frames and advanced at normal speed. Full-film playback remains unconfirmed."
				if audio {
					// A recording has no frames or picture to report.
					result.DecodedFrames, result.Width, result.Height = 0, 0, 0
					result.Detail = "A muted beginning sample decoded audio and advanced at normal speed. The rest of the recording remains unconfirmed."
					if result.FingerprintStatus == "pending" {
						waitForFingerprint(ctx, browser, snapshotJS, result)
					}
				} else {
					// The frames the page sampled so far are the start of the
					// fingerprint; the sample plays on a little to make it long
					// enough to compare.
					gatherVideoFrames(ctx, browser, snapshotJS, result, state)
				}
				return result
			}
		}
	}
}

type playbackProgress struct {
	start, last playbackSnapshot
	startedAt   time.Time
	lastAt      time.Time
	samples     int
	// audio judges a recording: time must advance as for video, but there
	// are no frames or picture to demand.
	audio bool
}

func (p *playbackProgress) observe(state playbackSnapshot, now time.Time, origin string, expectedDuration float64) (bool, string) {
	if state.Source == "" && state.ReadyState == 0 {
		return false, ""
	}
	validSource := state.Source == origin+"/media"
	if state.Protocol == "hls" {
		validSource = strings.HasPrefix(state.Source, "blob:"+origin+"/") && len(state.Source) > len("blob:"+origin+"/")
	}
	if !validSource {
		return false, "The video source changed outside the isolated playback harness; playback remains unconfirmed."
	}
	if math.IsNaN(state.CurrentTime) || math.IsInf(state.CurrentTime, 0) || math.IsNaN(state.Duration) || math.IsInf(state.Duration, 0) || state.CurrentTime < 0 {
		return false, "The video timeline could not be verified."
	}
	if state.Seeking && p.samples == 0 {
		return false, ""
	}
	if state.Seeking || state.PlaybackRate != 1 || state.RateChanges > 0 || p.samples > 0 && state.SeekCount != p.start.SeekCount {
		return false, "The sample did not advance naturally at normal speed; playback remains unconfirmed."
	}
	if state.Duration > 0 && expectedDuration > 0 && math.Abs(state.Duration-expectedDuration) > math.Max(60, expectedDuration*.15) {
		return false, "The video's duration does not match the movie catalog; it may be a clip or a different edition."
	}
	if state.ReadyState < 2 || state.Paused || state.Ended || state.Duration <= 0 || !p.audio && (state.Width <= 0 || state.Height <= 0 || state.DecodedFrames < 1) {
		return false, ""
	}
	if p.samples == 0 {
		if state.CurrentTime > 2.5 {
			return false, "The sample did not begin near the start of the movie; playback remains unconfirmed."
		}
		p.start, p.last, p.startedAt, p.lastAt, p.samples = state, state, now, now, 1
		return false, ""
	}
	elapsed := now.Sub(p.lastAt).Seconds()
	advance := state.CurrentTime - p.last.CurrentTime
	if elapsed <= 0 || advance < -.05 || !p.audio && state.DecodedFrames < p.last.DecodedFrames || advance > elapsed*1.35+.25 || math.Abs(state.Duration-p.start.Duration) > 1 {
		if p.audio {
			return false, "The recording's timeline changed unexpectedly; playback remains unconfirmed."
		}
		return false, "The video timeline or frame progression changed unexpectedly; playback remains unconfirmed."
	}
	p.last, p.lastAt = state, now
	p.samples++
	return p.samples >= 3 && now.Sub(p.startedAt) >= 800*time.Millisecond && state.CurrentTime-p.start.CurrentTime >= 1 && (p.audio || state.DecodedFrames-p.start.DecodedFrames >= 2), ""
}
