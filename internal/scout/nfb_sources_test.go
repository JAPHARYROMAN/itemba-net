package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

const nfbFixtureURL = "https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/"
const nfbFixtureTitle = "nîpawistamâsowin: We Will Stand Up"
const nfbFixtureMedia = "https://d3acx5b8mnvbua.cloudfront.net/hls/1016461_1016460_DM-13119/1016461_1016460_DM-13119.m3u8?Policy=fixture"

// Shape follows the public film page and its named player, observed 2026-10-18.
// The fixture policy is deliberately synthetic; no live signed URL is recorded.
const nfbFixturePage = `<html><script type="application/ld+json">{"@type":["Movie","VideoObject"],"@id":"https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/","url":"https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/","name":"nîpawistamâsowin: We Will Stand Up","duration":"PT1H38M","embedUrl":"https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/embed/player/"}</script><script>dataLayer.push({'nfb_version_year':'2019','nfb_version_duration':'5916'});</script><div id="download-product-button" data-id_registre="1016460"></div></html>`
const nfbFixturePlayer = `<html><script>window.IFRAME_OPTIONS = {"source":"https://d3acx5b8mnvbua.cloudfront.net/hls/1016461_1016460_DM-13119/1016461_1016460_DM-13119.m3u8?Policy=fixture","gtm":{"title":"nîpawistamâsowin: We Will Stand Up","registryId":1016460,"versionType":"film"},"overlay":{"url":"https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/"}};</script></html>`

func nfbFixtureSource() CandidateSource {
	return CandidateSource{Title: nfbFixtureTitle, URL: nfbFixtureURL, ReleaseYear: 2019, AccessType: "unknown", ListingOnly: true}
}

func nfbFixtureClient(t *testing.T, page, player string, calls *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("NFB preflight sent account credentials")
		}
		switch req.URL.String() {
		case nfbFixtureURL:
			return searchFixtureResponse(200, "text/html", page), nil
		case strings.TrimSuffix(nfbFixtureURL, "/") + "/embed/player/":
			return searchFixtureResponse(200, "text/html", player), nil
		default:
			t.Error("NFB preflight fetched an unassociated page or requested media")
			return searchFixtureResponse(404, "text/plain", ""), nil
		}
	})}
}

func TestNFBMovieTargetOnlyGuestFilmPages(t *testing.T) {
	for _, access := range []string{"", "unknown", "free"} {
		source := nfbFixtureSource()
		source.AccessType = access
		if !nfbMovieTarget(source) {
			t.Errorf("guest target rejected: access=%s", access)
		}
	}
	for _, source := range []CandidateSource{
		{URL: nfbFixtureURL, AccessType: "paid"},
		{URL: nfbFixtureURL, AccessType: "subscription"},
		{URL: nfbFixtureURL, AccessType: "free", Price: "$2.99"},
		{URL: "http://www.nfb.ca/film/movie/"},
		{URL: "https://www.nfb.ca/film/movie/embed/player/"},
		{URL: "https://nfb.ca.evil.example/film/movie/"},
		{URL: "https://www.nfb.ca:8081/film/movie/"},
	} {
		if nfbMovieTarget(source) {
			t.Error("paid, non-primary or unsafe target accepted")
		}
	}
}

func TestNFBFullMovieExactBindingKeepsMediaPrivate(t *testing.T) {
	calls := 0
	source, check := inspectNFBPlayback(context.Background(), nfbFixtureClient(t, nfbFixturePage, nfbFixturePlayer, &calls), nfbFixtureSource())
	if check != nil || calls != 2 || source.Duration != "5916" || source.ReleaseYear != 2019 || source.AccessType != "free" || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != nfbFixtureMedia || source.DirectStreamURL != "" || source.AccessBasis != "nfb_movie_metadata" {
		t.Fatalf("explicit full film was not privately bound: check=%+v calls=%d", check, calls)
	}
	page, _ := url.Parse(source.URL)
	media, _ := url.Parse(source.providerStreamURL)
	evidence, _ := url.Parse(source.AccessEvidenceURL)
	if !nfbResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("exact public player source binding rejected")
	}
	media.RawQuery = "Policy=another"
	if nfbResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("different signed asset inherited main film authorization")
	}
	encoded, _ := json.Marshal(source)
	if strings.Contains(string(encoded), "Policy=") || strings.Contains(string(encoded), "cloudfront.net") {
		t.Fatal("private provider source leaked into public results")
	}
}

func TestNFBRefusesTrailersMismatchesAndRestrictions(t *testing.T) {
	for _, test := range []struct {
		name, page, player string
		calls              int
		reason             string
	}{
		{"trailer", nfbFixturePage, strings.Replace(nfbFixturePlayer, `"versionType":"film"`, `"versionType":"trailer"`, 1), 2, "content_unconfirmed"},
		{"wrong_title", nfbFixturePage, strings.Replace(nfbFixturePlayer, nfbFixtureTitle, "Different Movie", 1), 2, "content_unconfirmed"},
		{"wrong_registry", nfbFixturePage, strings.Replace(nfbFixturePlayer, `"registryId":1016460`, `"registryId":999`, 1), 2, "content_unconfirmed"},
		{"non_http_source", nfbFixturePage, strings.Replace(nfbFixturePlayer, nfbFixtureMedia, "gs://bucket/movie.m3u8", 1), 2, "content_unconfirmed"},
		{"offsite_embed", strings.Replace(nfbFixturePage, `https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/embed/player/`, `https://unrelated.example/player/`, 1), nfbFixturePlayer, 1, "content_unconfirmed"},
		{"short_movie", strings.Replace(strings.Replace(nfbFixturePage, "PT1H38M", "PT3M", 1), "5916", "180", 1), nfbFixturePlayer, 1, "content_unconfirmed"},
		{"wrong_year", strings.Replace(nfbFixturePage, "'2019'", "'2020'", 1), nfbFixturePlayer, 1, "content_unconfirmed"},
		{"primary_geo", nfbFixturePage + `<nfb-player-error-message error="geoblocked"></nfb-player-error-message>`, nfbFixturePlayer, 1, "region_unavailable"},
		{"player_geo", nfbFixturePage, `<nfb-player-error-message error="geoblocked"></nfb-player-error-message>`, 2, "region_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			source, check := inspectNFBPlayback(context.Background(), nfbFixtureClient(t, test.page, test.player, &calls), nfbFixtureSource())
			if check == nil || check.Reason != test.reason || check.PlaybackConfirmed || source.providerContentBound || source.providerStreamURL != "" || calls != test.calls {
				t.Fatalf("unverified or restricted asset accepted: reason=%+v calls=%d", check, calls)
			}
		})
	}
}

func TestNFBFullFilmEntersProtectedNativeChecksWithoutPublicMedia(t *testing.T) {
	calls := 0
	client := nfbFixtureClient(t, nfbFixturePage, nfbFixturePlayer, &calls)
	base := client.Transport
	client.Transport = searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "d3acx5b8mnvbua.cloudfront.net" {
			return searchFixtureResponse(200, "application/vnd.apple.mpegurl", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=549237\nlow.m3u8?Policy=fixture\n"), nil
		}
		return base.RoundTrip(req)
	})
	s := &Scout{shield: shield.New(), availabilityHTTPClient: client, playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		if !eligibleFreePlayback(source) || !providerPlaybackBinding(source.CandidateSource) || source.providerStreamURL == "" {
			t.Error("publicly bound NFB main movie did not enter native verification")
		}
		return startedFixture(region, country)
	}}
	results := []VerifiedSource{{CandidateSource: nfbFixtureSource(), VerificationStatus: "listing"}}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if !playbackStarted(results[0]) || checkedWatchPriority(results[0], "ALL") != -10 {
		t.Fatal("decoded full movie was not promoted globally")
	}
	encoded, _ := json.Marshal(results[0])
	if strings.Contains(string(encoded), "Policy=") || strings.Contains(string(encoded), "cloudfront.net") {
		t.Fatal("private NFB playback source leaked into public API")
	}
}

func TestLiveNFBProtectedPlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_NFB_PLAYBACK") != "1" {
		t.Skip("opt-in bounded live movie sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 26*time.Second)
	defer cancel()
	s := New(shield.New(), "http://127.0.0.1:8080")
	previousHLS := moviePlaybackHLSScript
	moviePlaybackHLSScript = readMoviePlaybackHLSScript("../../web/dist/playback-hls.min.js")
	defer func() { moviePlaybackHLSScript = previousHLS }()
	if len(moviePlaybackHLSScript) == 0 {
		t.Fatal("built local HLS runtime unavailable")
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
		debug := &nfbPlaybackDebug{playbackBrowser: browser}
		check := runFreePlaybackSample(nativeCtx, debug, source, origin, &PlaybackCheck{Status: "unconfirmed", Method: "isolated_video_sample", RequestedRegion: region, Country: country, CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL})
		if check.Status != "started" {
			t.Logf("owned player final snapshot: %s", debug.last)
		}
		return applyPlaybackProtectionOutcome(check, outcome)
	}
	country, _ := s.availabilityCheckCountry(ctx)
	results := []VerifiedSource{{CandidateSource: nfbFixtureSource(), VerificationStatus: "listing"}}
	s.checkFreeMoviePlayback(ctx, results, "ALL", country)
	check := results[0].PlaybackCheck
	proof := struct {
		Title    string         `json:"title"`
		URL      string         `json:"url"`
		Duration string         `json:"listedDuration"`
		Check    *PlaybackCheck `json:"playbackCheck"`
	}{results[0].Title, results[0].URL, results[0].Duration, check}
	data, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile("../../.itemba-runtime/nfb-playback-proof.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if !playbackStarted(results[0]) {
		t.Fatalf("protected NFB full film start not confirmed: %+v", check)
	}
	t.Logf("NFB full film started: country=%s frames=%d progress=%.2fs duration=%.1fs", country, check.DecodedFrames, check.SecondsAdvanced, check.DurationSeconds)
}

type nfbPlaybackDebug struct {
	playbackBrowser
	last []byte
}

func (b *nfbPlaybackDebug) Call(ctx context.Context, method string, params any, result any) error {
	err := b.playbackBrowser.Call(ctx, method, params, result)
	if method == "Runtime.evaluate" && err == nil {
		b.last, _ = json.Marshal(result)
	}
	return err
}
