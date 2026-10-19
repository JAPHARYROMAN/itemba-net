package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

func providerFixtureFilmziePage(t *testing.T, ssai bool) string {
	t.Helper()
	formats := map[string]any{"MP4_WURL": map[string]any{"jobStatus": "SUCCESS", "approved": true, "legacyUrl": "https://media.example.com/main.mp4?token=fixture-private"}}
	if ssai {
		formats = map[string]any{"HLS_SSAI": map[string]any{"jobStatus": "SUCCESS", "approved": true}}
	}
	main := map[string]any{"id": "63a58b76d7cd4dd230c2f862", "type": "PAID_CONTENT", "duration": 5079, "formats": formats}
	content := map[string]any{"id": "63a58b76d7cd4dd230c2f861", "type": "MOVIE", "title": "Imprint", "duration": 5079, "released": "2007-01-01T00:00:00.000Z", "shareLink": "https://filmzie.com/content/imprint-2007", "sefName": "imprint-2007", "videos": []any{main}}
	data, err := json.Marshal(map[string]any{"contentPage": map[string]any{"content": content, "isAvailableInUserCountry": true}})
	if err != nil {
		t.Fatal(err)
	}
	return "<html><script>window['APP_STATE'] = " + string(data) + ";</script></html>"
}

func providerFixtureListing(raw string) VerifiedSource {
	return VerifiedSource{CandidateSource: CandidateSource{ID: raw, URL: raw, Title: "Imprint", ReleaseYear: 2007, AccessType: "free", ListingOnly: true}, VerificationStatus: "listing"}
}

func TestProviderRestrictionsStopBeforeNativePlaybackAndRankLowerGlobally(t *testing.T) {
	var requests atomic.Int32
	s := &Scout{shield: shield.New(), availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		switch req.URL.Hostname() {
		case "pluto.tv":
			response := searchFixtureResponse(200, "text/html", "<h3>Pluto TV is not available in your location.</h3>")
			response.Request, _ = http.NewRequest(http.MethodGet, "https://pluto.tv/intl/", nil)
			return response, nil
		case "filmzie.com":
			if strings.HasPrefix(req.URL.Path, "/content/") {
				return searchFixtureResponse(200, "text/html", providerFixtureFilmziePage(t, false)), nil
			}
			return searchFixtureResponse(200, "application/json", `{"error":"not_available_in_your_country"}`), nil
		}
		t.Error("a restricted provider requested media")
		return searchFixtureResponse(404, "text/plain", ""), nil
	})}, playbackProbe: func(context.Context, VerifiedSource, string, string) *PlaybackCheck {
		t.Error("a restricted provider launched a browser")
		return nil
	}}
	s.UseDrivers(testDrivers(t))
	results := []VerifiedSource{providerFixtureListing("https://pluto.tv/us/on-demand/movies/imprint-2007/details"), providerFixtureListing("https://filmzie.com/content/imprint-2007")}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if requests.Load() != 3 {
		t.Fatalf("unexpected preflight requests: %d", requests.Load())
	}
	for _, result := range results {
		p := result.PlaybackCheck
		if p == nil || p.Reason != "region_unavailable" || p.PlaybackConfirmed || p.Method != "provider_playback_preflight" || p.Country != "TZ" || p.RequestedRegion != "ALL" || p.CheckedAt == "" || p.DecodedFrames != 0 || result.AvailabilityCheck.Status != "region_blocked" {
			t.Fatalf("provider restriction was not preserved honestly: %+v", result)
		}
		if checkedWatchPriority(result, "ALL") <= checkedWatchPriority(playbackFixture("working"), "ALL") {
			t.Fatal("restricted provider outranked reachable global media")
		}
	}
	coverage := movieAvailabilityCoverage("ALL", "TZ", "", results)
	if coverage.ProviderCheckedCount != 2 || coverage.PlaybackCheckedCount != 0 || coverage.PlaybackStartedCount != 0 {
		t.Fatalf("provider metadata was counted as decoded playback: %+v", coverage)
	}
}

func TestFilmzieMainMediaRequiresProtectedNativeProofAndKeepsSignedURLPrivate(t *testing.T) {
	for _, ssai := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit main file", true: "unattributed SSAI"}[ssai], func(t *testing.T) {
			var sampled atomic.Int32
			s := &Scout{shield: shield.New(), availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == "media.example.com" {
					if ssai {
						t.Error("SSAI stream fetched without movie/ad attribution")
					}
					return searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom"), nil
				}
				if strings.HasPrefix(req.URL.Path, "/content/") {
					return searchFixtureResponse(200, "text/html", providerFixtureFilmziePage(t, ssai)), nil
				}
				return searchFixtureResponse(200, "application/json", `{"data":{"id":"63a58b76d7cd4dd230c2f862","type":"PAID_CONTENT","source":{"hlsV2":"https://media.example.com/main.m3u8?token=fixture-private"}}}`), nil
			})}, playbackProbe: func(_ context.Context, result VerifiedSource, region, country string) *PlaybackCheck {
				sampled.Add(1)
				if !eligibleFreePlayback(result) || !result.providerContentBound || result.providerSSAI {
					t.Error("unbound or advertising media entered native sampling")
				}
				return startedFixture(region, country)
			}}
			s.UseDrivers(testDrivers(t))
			results := []VerifiedSource{providerFixtureListing("https://filmzie.com/content/imprint-2007")}
			s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
			result := results[0]
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "fixture-private") || result.DirectStreamURL != "" || len(result.DetectedManifests) != 0 {
				t.Fatal("private provider stream escaped into API output")
			}
			if ssai {
				if sampled.Load() != 0 || result.PlaybackCheck.Reason != "content_unconfirmed" || playbackStarted(result) {
					t.Fatal("SSAI ads were presented as a full movie start")
				}
			} else if sampled.Load() != 1 || !playbackStarted(result) || checkedWatchPriority(result, "ALL") != -10 {
				t.Fatalf("bound main movie did not enter native proof and global ranking: %+v", result)
			}
		})
	}
}

func TestProviderChecksKeepProtectionAndPaidExclusion(t *testing.T) {
	var requests atomic.Int32
	sh := shield.New()
	sh.Engine().AddCustomRule("filmzie.com")
	s := &Scout{shield: sh, availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return searchFixtureResponse(200, "text/html", "unexpected"), nil
	})}}
	free := providerFixtureListing("https://filmzie.com/content/imprint-2007")
	paid := free
	paid.AccessType = "subscription"
	s.UseDrivers(testDrivers(t))
	results := []VerifiedSource{free, paid}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if requests.Load() != 0 || results[0].PlaybackCheck.Reason != "blocked_with_protection" || results[1].PlaybackCheck != nil {
		t.Fatal("provider checks bypassed ad filtering or sampled a paid offer")
	}
	for _, reason := range []string{"blocked_with_protection", "content_unconfirmed", "drm_required"} {
		check := applyPlaybackProtectionOutcome(startedFixture("ALL", "TZ"), func() string { return reason })
		if check.PlaybackConfirmed || check.Status == "started" || check.Reason != reason {
			t.Fatal("native frame evidence overrode a protection/content failure")
		}
		started := playbackFixture("ad")
		started.PlaybackCheck = startedFixture("ALL", "TZ")
		started.PlaybackCheck.Reason = reason
		if playbackStarted(started) {
			t.Fatal("contradictory failure reason received a green playback badge")
		}
	}
}

func TestDiscoveredAndPastedProviderMovieLinksRunTheSameChecks(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "fixture-key")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.UseDrivers(testDrivers(t))
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "api.tavily.com" {
			return searchFixtureResponse(200, "application/json", `{"results":[{"title":"Watch Imprint (2007) | Filmzie","url":"https://filmzie.com/content/imprint-2007"},{"title":"Watch Imprint (2007) | Pluto TV","url":"https://pluto.tv/us/on-demand/movies/imprint-2007/details"},{"title":"Watch Imprint (2007) | Netflix","url":"https://www.netflix.com/title/imprint2007"}]}`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"results":[],"response":{"docs":[]},"data":[],"query":{"pages":{}}}`), nil
	})}
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Hostname() {
		case "www.cloudflare.com":
			return searchFixtureResponse(200, "text/plain", "loc=TZ\n"), nil
		case "filmzie.com":
			if strings.HasPrefix(req.URL.Path, "/content/") {
				return searchFixtureResponse(200, "text/html", providerFixtureFilmziePage(t, true)), nil
			}
			return searchFixtureResponse(200, "application/json", `{"error":"not_available_in_your_country"}`), nil
		case "pluto.tv":
			response := searchFixtureResponse(200, "text/html", "<h3>Pluto TV is not available in your location.</h3>")
			response.Request, _ = http.NewRequest(http.MethodGet, "https://pluto.tv/intl/", nil)
			return response, nil
		case "www.netflix.com":
			return searchFixtureResponse(200, "text/html", `<title>Imprint (2007)</title><main><h1>Imprint (2007)</h1><p>Watch this feature film with your subscription. Movie details and account membership offers.</p></main>`), nil
		}
		return searchFixtureResponse(200, "application/json", `{"data":[],"feed":{"results":[]}}`), nil
	})}
	s.playbackProbe = func(context.Context, VerifiedSource, string, string) *PlaybackCheck {
		t.Fatal("restricted provider or paid movie entered native sampling")
		return nil
	}
	for _, query := range []string{"Imprint 2007", "https://filmzie.com/content/imprint-2007", "https://pluto.tv/us/on-demand/movies/imprint-2007/details"} {
		report := s.SearchWithOptions(context.Background(), query, "ALL", SearchOptions{FullLengthOnly: true})
		want := 2
		if strings.HasPrefix(query, "https:") {
			want = 1
		}
		if report.Region != "ALL" || report.AvailabilityChecks == nil || report.AvailabilityChecks.ProviderCheckedCount != want || report.AvailabilityChecks.PlaybackStartedCount != 0 || len(report.Sources) == 0 {
			t.Fatalf("provider checks were lost between discovery and API report for %q: %+v", query, report)
		}
		for _, source := range report.Sources {
			if s.supportedProviderPlayback(source.CandidateSource) && (source.PlaybackCheck == nil || source.PlaybackCheck.Reason != "region_unavailable") {
				t.Fatalf("provider link bypassed the access check: %+v", source)
			}
		}
	}
}
