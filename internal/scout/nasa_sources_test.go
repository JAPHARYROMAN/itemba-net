package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

const nasaFixturePage = `<html><head><link rel="alternate" type="application/json" href="https://plus.nasa.gov/wp-json/wp/v2/video/6604"><script type="application/ld+json">{"headline":"Planetary Defenders","url":"https://plus.nasa.gov/video/planetary-defenders/","articleSection":"Documentaries","datePublished":"2025-04-16T20:27:46Z"}</script></head><body><video id="main-video" title="Planetary Defenders" data-is-youtube="false" data-video-type="application/x-mpegURL" data-video-url="https://nasaplus.akamaized.net/output/16899.m3u8"><source src="https://nasaplus.akamaized.net/output/16899.m3u8"></video></body></html>`
const nasaFixtureAPI = `{"id":6604,"type":"video","slug":"planetary-defenders","link":"https://plus.nasa.gov/video/planetary-defenders/","date":"2025-04-16T16:27:46","title":{"rendered":"Planetary Defenders"},"meta":{"video-url":"https://nasaplus.akamaized.net/output/16899.m3u8","runtime":"4508","youtube-url":"","preview-url":"https://nasaplus.akamaized.net/output/trailer.m3u8"}}`

func nasaFixtureSource() CandidateSource {
	return CandidateSource{Title: "Planetary Defenders", URL: "https://plus.nasa.gov/video/planetary-defenders/", AccessType: "unknown", ListingOnly: true}
}

func TestNASAProviderMatchesFullDocumentaryAndNotTrailer(t *testing.T) {
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/wp-json/") {
			return searchFixtureResponse(200, "application/json", nasaFixtureAPI), nil
		}
		return searchFixtureResponse(200, "text/html", nasaFixturePage), nil
	})}
	source, check := inspectNASAPlayback(context.Background(), client, nasaFixtureSource())
	if check != nil || !providerPlaybackBinding(source) || source.AccessType != "free" || source.Duration != "4508" || source.DirectStreamURL != "" || source.providerStreamURL == "" || strings.Contains(source.providerStreamURL, "trailer") {
		t.Fatalf("main public documentary was not bound correctly: %+v check=%+v", source, check)
	}
	for _, page := range []string{
		strings.ReplaceAll(nasaFixturePage, "Documentaries", "Live coverage"),
		strings.ReplaceAll(nasaFixturePage, `data-is-youtube="false"`, `data-is-youtube="true"`),
		strings.ReplaceAll(nasaFixturePage, `title="Planetary Defenders"`, `title="Unrelated movie"`),
		strings.ReplaceAll(nasaFixturePage, "/output/16899.m3u8", "/output/trailer.m3u8"),
	} {
		client.Transport = searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
			if strings.HasPrefix(req.URL.Path, "/wp-json/") {
				return searchFixtureResponse(200, "application/json", nasaFixtureAPI), nil
			}
			return searchFixtureResponse(200, "text/html", page), nil
		})
		if bad, check := inspectNASAPlayback(context.Background(), client, nasaFixtureSource()); check == nil || check.PlaybackConfirmed || bad.providerContentBound {
			t.Fatal("unmatched/unsupported NASA content qualified as a full movie")
		}
	}
}

func TestNASACatalogFindsFeatureAndKeepsGlobalSearch(t *testing.T) {
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("search") != "Planetary Defenders" || req.URL.Query().Get("country") != "" {
			t.Error("NASA title or global search was changed")
		}
		return searchFixtureResponse(200, "application/json", "["+nasaFixtureAPI+","+strings.ReplaceAll(nasaFixtureAPI, `"4508"`, `"139"`)+"]"), nil
	})}}
	items, err := s.queryNASAMovieSites(context.Background(), "Planetary Defenders 2025")
	if err != nil || len(items) != 1 || items[0].Duration != "4508" || items[0].DirectStreamURL != "" || items[0].AccessType != "unknown" {
		t.Fatalf("NASA feature discovery incorrectly included previews or playback claims: %+v %v", items, err)
	}
}

func TestNASAFeaturePassesThroughProtectedNativePipeline(t *testing.T) {
	s := &Scout{shield: shield.New(), availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "nasaplus.akamaized.net" {
			return searchFixtureResponse(200, "application/vnd.apple.mpegurl", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=272000\n16899-0.m3u8\n"), nil
		}
		if strings.HasPrefix(req.URL.Path, "/wp-json/") {
			return searchFixtureResponse(200, "application/json", nasaFixtureAPI), nil
		}
		return searchFixtureResponse(200, "text/html", nasaFixturePage), nil
	})}, playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		if !eligibleFreePlayback(source) || source.providerStreamURL == "" {
			t.Error("NASA's bound main film did not enter native sampling")
		}
		return startedFixture(region, country)
	}}
	results := []VerifiedSource{{CandidateSource: nasaFixtureSource(), VerificationStatus: "listing"}}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if !playbackStarted(results[0]) || checkedWatchPriority(results[0], "ALL") != -10 {
		t.Fatal("decoded NASA main movie was not promoted globally")
	}
	data, _ := json.Marshal(results[0])
	if strings.Contains(string(data), "16899.m3u8") {
		t.Fatal("provider media escaped private sample into API")
	}
}

func TestLiveNASAProtectedPlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_NASA_PLAYBACK") != "1" {
		t.Skip("opt-in bounded live movie sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Second)
	defer cancel()
	s := New(shield.New(), "http://127.0.0.1:8080")
	// Go test runs from this package directory; load the same built, local HLS
	// artifact used by the server running from the project root.
	previousHLS := moviePlaybackHLSScript
	moviePlaybackHLSScript = readMoviePlaybackHLSScript("../../web/dist/playback-hls.min.js")
	defer func() { moviePlaybackHLSScript = previousHLS }()
	if len(moviePlaybackHLSScript) == 0 {
		t.Fatal("built local HLS runtime is unavailable")
	}
	s.playbackProbe = func(ctx context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		internal := source
		internal.DirectStreamURL = source.providerStreamURL
		origin, close, outcome, err := startProtectedMoviePlaybackHarness(ctx, internal, s.availabilityHTTPClient, s.shield)
		if err != nil {
			t.Fatal("protected harness unavailable")
		}
		defer close()
		nativeCtx, cancel := context.WithTimeout(ctx, freePlaybackDeadline)
		defer cancel()
		browser, err := launchFreePlaybackBrowser(nativeCtx)
		if err != nil {
			t.Fatal("isolated browser unavailable")
		}
		defer browser.Close()
		browser.OnEvent(func(method string, data json.RawMessage) {
			if method == "Network.loadingFailed" {
				var failure struct {
					Error string `json:"errorText"`
				}
				if json.Unmarshal(data, &failure) == nil {
					t.Logf("owned-player network outcome: %s", failure.Error)
				}
			}
		})
		debug := &nasaPlaybackDebug{playbackBrowser: browser}
		check := runFreePlaybackSample(nativeCtx, debug, source, origin, &PlaybackCheck{Status: "unconfirmed", Method: "isolated_video_sample", RequestedRegion: region, Country: country, CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL})
		if check.Status != "started" {
			t.Logf("last owned native snapshot: %s", debug.last)
		}
		return applyPlaybackProtectionOutcome(check, outcome)
	}
	country, _ := s.availabilityCheckCountry(ctx)
	results := []VerifiedSource{{CandidateSource: nasaFixtureSource(), VerificationStatus: "listing"}}
	s.checkFreeMoviePlayback(ctx, results, "ALL", country)
	check := results[0].PlaybackCheck
	proof := struct {
		Title    string         `json:"title"`
		URL      string         `json:"url"`
		Duration string         `json:"listedDuration"`
		Check    *PlaybackCheck `json:"playbackCheck"`
	}{results[0].Title, results[0].URL, results[0].Duration, check}
	data, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile("../../.itemba-runtime/nasa-playback-proof.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if !playbackStarted(results[0]) {
		t.Fatalf("protected full documentary start not confirmed: %+v", check)
	}
	t.Logf("NASA+ main documentary started: country=%s frames=%d progress=%.2fs duration=%.1fs", country, check.DecodedFrames, check.SecondsAdvanced, check.DurationSeconds)
}

type nasaPlaybackDebug struct {
	playbackBrowser
	last []byte
}

func (b *nasaPlaybackDebug) Call(ctx context.Context, method string, params any, result any) error {
	err := b.playbackBrowser.Call(ctx, method, params, result)
	if method == "Runtime.evaluate" && err == nil {
		b.last, _ = json.Marshal(result)
	}
	return err
}
