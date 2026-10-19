package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

// The Pluto TV and Filmzie access checks are drivers now. These are the
// legacy inspectors' tests run through the pipeline's bridge, with the real
// page and metadata judgments the bridge supplies.

const (
	plutoFeatureMoviePage = "https://pluto.tv/us/on-demand/movies/61c2323f68b5ad001a6211e1/details"
	filmzieFixtureMainID  = "63a58b76d7cd4dd230c2f862"
)

func plutoFeatureCandidate() CandidateSource {
	return CandidateSource{ID: "web-pluto-feature", Title: "Rugrats Go Wild", ReleaseYear: 2003, URL: plutoFeatureMoviePage, Provider: "Pluto TV", SourceType: "Web Listing", AccessType: "free", AccessBasis: "provider_model", ListingOnly: true}
}

func filmzieFixtureSource() CandidateSource {
	return CandidateSource{ID: "filmzie-imprint", Title: "Imprint", ReleaseYear: 2007, URL: "https://filmzie.com/content/imprint-2007", AccessType: "free", ListingOnly: true}
}

// checkScout is a Scout with the shipped drivers and a fixture transport
// behind the guarded provider check.
func checkScout(t *testing.T, fn func(*http.Request) (*http.Response, error)) *Scout {
	t.Helper()
	s := &Scout{shield: shield.New(), availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(fn)}}
	s.UseDrivers(testDrivers(t))
	return s
}

// preflight runs the provider check as the pipeline does, with the actual
// check country and requested region.
func preflight(t *testing.T, s *Scout, source CandidateSource, region, country string) VerifiedSource {
	t.Helper()
	result := VerifiedSource{CandidateSource: source, VerificationStatus: "listing"}
	s.inspectProviderMoviePlayback(context.Background(), &result, region, country)
	return result
}

// inspectWithDriver runs one driver's check directly, as the legacy
// inspectors were tested, without the pipeline's country and region.
func inspectWithDriver(t *testing.T, s *Scout, name string, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	t.Helper()
	d := s.driver(name)
	if d == nil {
		t.Fatalf("driver %s is not loaded", name)
	}
	return s.inspectDriverPlayback(context.Background(), s.availabilityHTTPClient.Transport, d, source)
}

func TestPlutoCheckTargetsOnlyFreeIndividualMoviePages(t *testing.T) {
	s := checkScout(t, func(*http.Request) (*http.Response, error) {
		t.Error("unsupported source triggered a public provider request")
		return nil, fmt.Errorf("unexpected request")
	})
	if !s.supportedProviderPlayback(plutoFeatureCandidate()) || s.providerPlaybackKind(plutoFeatureCandidate()) != "pluto-tv" {
		t.Fatal("individual free Pluto movie must be a supported check target")
	}
	for _, raw := range []string{
		"https://pluto.tv/us/movies/", "https://pluto.tv/us/on-demand/movies", "https://pluto.tv/us/on-demand/series/123/season/1", "https://pluto.tv/us/watch/live-tv/123", "https://pluto.tv/us/on-demand/movies/123/trailer", "https://pluto.tv/us/on-demand/movies/123/reviews", "https://support.pluto.tv/us/on-demand/movies/123", "https://pluto.tv.evil.net/us/on-demand/movies/123", "http://127.0.0.1/us/on-demand/movies/123", "https://user:pass@pluto.tv/us/on-demand/movies/123",
	} {
		source := plutoFeatureCandidate()
		source.URL = raw
		if s.supportedProviderPlayback(source) {
			t.Errorf("unsupported Pluto destination accepted: %s", raw)
		}
	}
	paid := plutoFeatureCandidate()
	paid.AccessType = "subscription"
	priced := plutoFeatureCandidate()
	priced.Price = "USD 3.99"
	if s.supportedProviderPlayback(paid) || s.supportedProviderPlayback(priced) {
		t.Fatal("paid candidates must never be included in a free provider sample")
	}
	channel := plutoFeatureCandidate()
	channel.URL = "https://pluto.tv/us/watch/live-tv/123"
	results := []VerifiedSource{{CandidateSource: paid}, {CandidateSource: channel}}
	s.checkFreeMoviePlayback(context.Background(), results, "ALL", "TZ")
	if results[0].PlaybackCheck != nil || results[1].PlaybackCheck == nil || results[1].PlaybackCheck.Method != "not_checked" {
		t.Fatalf("a paid offer or a channel reached a provider check: %+v", results)
	}
}

func TestPlutoRegionBlockIsPreservedWithoutPlayback(t *testing.T) {
	var requests atomic.Int32
	s := checkScout(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			t.Fatal("provider preflight must not include account state")
		}
		if request.URL.String() == plutoFeatureMoviePage {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://pluto.tv/intl/"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		if request.URL.String() == "https://pluto.tv/intl/" {
			return additionalFeatureResponse(request, `<main><h3>Pluto TV is not available in your location.</h3></main>`), nil
		}
		return nil, fmt.Errorf("unexpected URL")
	})
	result := preflight(t, s, plutoFeatureCandidate(), "ALL", "TZ")
	check := result.PlaybackCheck
	if check == nil || check.Status != "not_started" || check.Reason != "region_unavailable" || check.PlaybackConfirmed || check.Country != "TZ" || check.RequestedRegion != "ALL" || check.EvidenceURL != "https://pluto.tv/intl/" || check.Method != "provider_playback_preflight" || requests.Load() != 2 {
		t.Fatalf("provider location block was lost or overstated as playback: %#v, requests=%d", check, requests.Load())
	}
	if result.AvailabilityCheck == nil || result.AvailabilityCheck.Status != "region_blocked" || result.AvailabilityCheck.Country != "TZ" || result.AvailabilityCheck.EvidenceURL != "https://pluto.tv/intl/" {
		t.Fatalf("the restriction must reach the availability check: %+v", result.AvailabilityCheck)
	}
	if result.URL != plutoFeatureMoviePage || result.DirectStreamURL != "" || result.Title != "Rugrats Go Wild" || result.providerContentBound {
		t.Fatal("the listing itself must not change")
	}
}

func TestPlutoLocationBlockRequiresPrimaryVisibleProviderHeading(t *testing.T) {
	serve := func(final, html string) *Scout {
		return checkScout(t, func(request *http.Request) (*http.Response, error) {
			response := additionalFeatureResponse(request, html)
			clone := request.Clone(request.Context())
			clone.URL, _ = url.Parse(final)
			response.Request = clone
			return response, nil
		})
	}
	if check := preflight(t, serve("https://pluto.tv/intl/", `<main><h3>Pluto TV is not available in your location.</h3><p>Thanks for your patience.</p></main>`), plutoFeatureCandidate(), "ALL", "").PlaybackCheck; check == nil || check.Reason != "region_unavailable" {
		t.Fatalf("first-party generic country-block page must remain a region observation: %+v", check)
	}
	for _, html := range []string{
		`<h1>Rugrats Go Wild</h1><footer><h3>Pluto TV is not available in your location.</h3></footer>`,
		`<h1>Rugrats Go Wild</h1><div hidden><h3>Pluto TV is not available in your location.</h3></div>`,
		`<h1>Rugrats Go Wild</h1><section class="faq"><h3>Pluto TV is not available in your location.</h3></section>`,
		`<h1>Rugrats Go Wild</h1><script>"Pluto TV is not available in your location."</script>`,
		`<h3>Why is Pluto TV is not available in your location?</h3>`,
		`<h1>Access denied</h1><p>HTTP 403 does not establish a country block.</p>`,
	} {
		if check := preflight(t, serve("https://pluto.tv/intl/", html), plutoFeatureCandidate(), "ALL", "").PlaybackCheck; check == nil || check.Reason != "content_unconfirmed" || check.Status != "unconfirmed" {
			t.Errorf("non-primary or ambiguous region evidence accepted: %s -> %+v", html, check)
		}
	}
	if check := preflight(t, serve("https://pluto.tv.evil.net/intl/", `<h3>Pluto TV is not available in your location.</h3>`), plutoFeatureCandidate(), "ALL", "").PlaybackCheck; check == nil || check.Reason != "content_unconfirmed" || !strings.Contains(check.Detail, "redirected away") {
		t.Fatalf("a different host cannot establish Pluto's service availability: %+v", check)
	}
}

func TestPlutoReachableMoviePageCannotConfirmPlaybackOrAdsRemoved(t *testing.T) {
	s := checkScout(t, func(request *http.Request) (*http.Response, error) {
		return additionalFeatureResponse(request, `<title>Rugrats Go Wild (2003) | Pluto TV</title><main><h1>Rugrats Go Wild</h1><button>Watch Now</button><p>The requested movie is offered by the provider. A visible movie action or an HTTP success is not native movie playback.</p></main>`), nil
	})
	result := preflight(t, s, plutoFeatureCandidate(), "ALL", "")
	check := result.PlaybackCheck
	if check == nil || check.Status != "unconfirmed" || check.Reason != "content_unconfirmed" || check.PlaybackConfirmed || !strings.Contains(check.Detail, "in-stream advertisements remain unconfirmed") || result.AvailabilityCheck != nil {
		t.Fatalf("a page response must not become movie/ad-removal proof: %#v", check)
	}
}

func TestPlutoPageProbeRejectsPrivateRedirectWithoutFetchingIt(t *testing.T) {
	var requests atomic.Int32
	s := checkScout(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	check := preflight(t, s, plutoFeatureCandidate(), "ALL", "TZ").PlaybackCheck
	if check == nil || check.Status != "unconfirmed" || check.PlaybackConfirmed || requests.Load() != 1 || check.EvidenceURL != plutoFeatureMoviePage {
		t.Fatalf("private redirects must remain unfetched/unconfirmed: %#v, requests=%d", check, requests.Load())
	}
}

func TestPlutoPreflightRecordsReasonsWithoutDecodedClaims(t *testing.T) {
	for _, tc := range []struct{ name, html, finalPath, status, reason string }{
		{"region restriction", `<h3>Pluto TV is not available in your location.</h3>`, "/intl/", "not_started", "region_unavailable"},
		{"sign in required", `<main><h1>Sign in</h1><form><label>Sign in to watch</label><input type="password"></form></main>`, "/login/", "not_started", "sign_in_required"},
		{"matching page without movie asset proof", `<title>Rugrats Go Wild (2003) | Pluto TV</title><main><h1>Rugrats Go Wild</h1><button>Watch Now</button></main>`, "/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", "unconfirmed", "content_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := checkScout(t, func(request *http.Request) (*http.Response, error) {
				response := additionalFeatureResponse(request, tc.html)
				clone := request.Clone(request.Context())
				clone.URL, _ = url.Parse("https://pluto.tv" + tc.finalPath)
				response.Request = clone
				return response, nil
			})
			source := plutoFeatureCandidate()
			returned, check := inspectWithDriver(t, s, "pluto-tv", source)
			if returned.URL != source.URL || returned.DirectStreamURL != "" || check == nil || check.Status != tc.status || check.Reason != tc.reason || check.Method != "provider_playback_preflight" || check.CheckedAt == "" || check.PlaybackConfirmed || check.DecodedFrames != 0 || check.SecondsAdvanced != 0 || check.DurationSeconds != 0 || check.Width != 0 || check.Height != 0 {
				t.Fatalf("preflight status became native playback evidence: %#v %#v", returned, check)
			}
			if check.Country != "" || check.RequestedRegion != "" {
				t.Fatal("integration supplies the actual check country and requested preference")
			}
		})
	}
}

func filmzieFixtureState() map[string]any {
	main := map[string]any{"id": filmzieFixtureMainID, "type": "PAID_CONTENT", "duration": 5079.0, "formats": map[string]any{"HLS_SSAI": map[string]any{"approved": true, "jobStatus": "SUCCESS"}}}
	trailer := map[string]any{"id": "63a58b76d7cd4dd230c2f869", "type": "TRAILER", "duration": 113.0}
	content := map[string]any{"id": "63a58b76d7cd4dd230c2f871", "type": "MOVIE", "title": "Imprint", "duration": 5079.0, "released": "2007-01-01T00:00:00.000Z", "shareLink": filmzieFixtureSource().URL, "sefName": "imprint-2007", "videos": []any{main, trailer}}
	return map[string]any{"contentPage": map[string]any{"content": content, "isAvailableInUserCountry": true}}
}

func filmzieFixtureContent(state map[string]any) map[string]any {
	return state["contentPage"].(map[string]any)["content"].(map[string]any)
}

func filmzieFixtureMain(state map[string]any) map[string]any {
	return filmzieFixtureContent(state)["videos"].([]any)[0].(map[string]any)
}

func filmzieFixturePage(state map[string]any) string {
	encoded, _ := json.Marshal(state)
	return `<html><script>window['APP_STATE'] = ` + string(encoded) + `;</script></html>`
}

func filmzieFixtureScout(t *testing.T, state map[string]any, apiStatus int, apiBody string, calls *atomic.Int32) *Scout {
	t.Helper()
	return checkScout(t, func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("provider preflight forwarded cookies or authorization")
		}
		var response *http.Response
		switch req.URL.String() {
		case filmzieFixtureSource().URL:
			response = searchFixtureResponse(200, "text/html", filmzieFixturePage(state))
		case "https://filmzie.com/api/v1/video/stream/" + filmzieFixtureMainID:
			response = searchFixtureResponse(apiStatus, "application/json", apiBody)
		default:
			t.Errorf("preflight fetched a trailer, ad, guessed origin or movie media: %s", req.URL)
			response = searchFixtureResponse(404, "text/plain", "")
		}
		response.Request = req
		return response, nil
	})
}

func TestFilmziePlaybackHonorsProviderCountryRestrictionWithoutFetchingMedia(t *testing.T) {
	var calls atomic.Int32
	s := filmzieFixtureScout(t, filmzieFixtureState(), 200, `{"error":"not_available_in_your_country"}`, &calls)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://filmzie.com/")
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "fixture-account"}})
	s.availabilityHTTPClient.Jar = jar
	result := preflight(t, s, filmzieFixtureSource(), "ALL", "TZ")
	check := result.PlaybackCheck
	if check == nil || check.Status != "not_started" || check.Reason != "region_unavailable" || check.PlaybackConfirmed || check.Method != "provider_playback_preflight" || result.providerStreamURL != "" || result.DirectStreamURL != "" || calls.Load() != 2 {
		t.Fatalf("country block was ignored or media guessed: check=%+v calls=%d", check, calls.Load())
	}
	if !strings.HasSuffix(check.EvidenceURL, filmzieFixtureMainID) || check.CheckedAt == "" || result.AvailabilityCheck == nil || result.AvailabilityCheck.Status != "region_blocked" {
		t.Fatal("preflight evidence, timestamp or availability restriction missing")
	}
	if result.Duration != "5079" || result.ReleaseYear != 2007 || result.Title != "Imprint" {
		t.Fatalf("matching page metadata should survive the restriction: %+v", result.CandidateSource)
	}
}

func TestFilmziePlaybackNeverConfirmsTrailerDRMOrUnattributedSSAI(t *testing.T) {
	for _, test := range []struct{ name, body, reason string }{
		{"trailer", `{"data":{"type":"TRAILER","source":{"hlsV2":"https://media.filmzie-cdn.net/trailer.m3u8"}}}`, "trailer_only"},
		{"drm", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":{"src":"https://media.filmzie-cdn.net/movie.m3u8","keySystems":{"com.widevine.alpha":"https://license.filmzie.com/required"}}}}}`, "drm_required"},
		{"server_inserted_ads", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/movie.m3u8?token=fixture"}}}`, "content_unconfirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			s := filmzieFixtureScout(t, filmzieFixtureState(), 200, test.body, &calls)
			source, check := inspectWithDriver(t, s, "filmzie", filmzieFixtureSource())
			if check == nil || check.Reason != test.reason || check.PlaybackConfirmed || source.DirectStreamURL != "" || calls.Load() != 2 {
				t.Fatalf("unverified provider asset was accepted: %+v", check)
			}
			if test.name == "server_inserted_ads" && (!source.providerContentBound || !source.providerSSAI || source.providerStreamURL == "" || source.providerCheck != "filmzie") {
				t.Fatal("main asset association or SSAI uncertainty was lost")
			}
			encoded, _ := json.Marshal(source)
			if strings.Contains(string(encoded), "token=fixture") {
				t.Fatal("unverified signed media source leaked into public results")
			}
		})
	}
}

func TestFilmziePlaybackOnlyReadiesExplicitAuthorizedFullMovieFile(t *testing.T) {
	state := filmzieFixtureState()
	filmzieFixtureMain(state)["formats"].(map[string]any)["MP4_WURL"] = map[string]any{"approved": true, "jobStatus": "SUCCESS", "legacyUrl": "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture"}
	var calls atomic.Int32
	s := filmzieFixtureScout(t, state, 200, `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/ssai.m3u8"}}}`, &calls)
	source, check := inspectWithDriver(t, s, "filmzie", filmzieFixtureSource())
	if check != nil || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture" || source.DirectStreamURL != "" || source.Duration != "5079" || source.AccessBasis != "filmzie_movie_metadata" || source.Provider != "Filmzie" || source.SourceType != "Provider free movie" || source.AccessEvidenceURL != "https://filmzie.com/api/v1/video/stream/"+filmzieFixtureMainID || calls.Load() != 2 {
		t.Fatalf("explicit main asset did not stay private pending native verification: check=%+v source=%+v", check, source)
	}
	page, _ := url.Parse(source.URL)
	media, _ := url.Parse(source.providerStreamURL)
	evidence, _ := url.Parse(source.AccessEvidenceURL)
	if !providerPlaybackBinding(source) || !driverBoundPlaybackPair(source, page, media, evidence) {
		t.Fatal("exact authorized source association rejected")
	}
	media.Path = "/trailer.mp4"
	if driverBoundPlaybackPair(source, page, media, evidence) {
		t.Fatal("another media asset inherited main movie authorization")
	}
	ssai := source
	ssai.providerSSAI = true
	if providerPlaybackBinding(ssai) {
		t.Fatal("a stream with server-inserted ads must not be bound for a sample")
	}
}

func TestFilmziePlaybackRejectsMismatchedMovieRuntimeAndAmbiguousAssets(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"different title": func(s map[string]any) { filmzieFixtureContent(s)["title"] = "Different Movie" },
		"different year":  func(s map[string]any) { filmzieFixtureContent(s)["released"] = "2020-01-01T00:00:00Z" },
		"short asset":     func(s map[string]any) { filmzieFixtureMain(s)["duration"] = 113.0 },
		"two main assets": func(s map[string]any) {
			c := filmzieFixtureContent(s)
			c["videos"] = append(c["videos"].([]any), c["videos"].([]any)[0])
		},
		"other share link": func(s map[string]any) {
			filmzieFixtureContent(s)["shareLink"] = "https://filmzie.com/content/different-film"
		},
	} {
		state := filmzieFixtureState()
		mutate(state)
		var calls atomic.Int32
		s := filmzieFixtureScout(t, state, 200, `{"data":{"type":"PAID_CONTENT"}}`, &calls)
		source, check := inspectWithDriver(t, s, "filmzie", filmzieFixtureSource())
		if check == nil || check.Reason != "content_unconfirmed" || calls.Load() != 1 || source.Duration != "" || source.providerContentBound {
			t.Fatalf("%s: wrong or partial movie reached the stream API: %+v calls=%d", name, check, calls.Load())
		}
	}
}

func TestFilmziePlaybackDistinguishesGuestRestrictionsFromShieldBlocksAndTimeouts(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		var calls atomic.Int32
		s := filmzieFixtureScout(t, filmzieFixtureState(), status, `{"error":"automated_request_restricted"}`, &calls)
		_, check := inspectWithDriver(t, s, "filmzie", filmzieFixtureSource())
		if check == nil || check.Status != "unconfirmed" || check.Reason != "content_unconfirmed" {
			t.Fatalf("provider restriction was incorrectly attributed to Shield: %+v", check)
		}
	}
	s := checkScout(t, func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, check := s.inspectDriverPlayback(ctx, s.availabilityHTTPClient.Transport, s.driver("filmzie"), filmzieFixtureSource())
	if check == nil || check.Reason != "timeout" || check.PlaybackConfirmed {
		t.Fatalf("actual request deadline was not reported: %+v", check)
	}
	// A Shield rule on the host is reported as protection, not as the
	// provider's answer, and nothing is fetched.
	sh := shield.New()
	sh.Engine().AddCustomRule("filmzie.com")
	var requests atomic.Int32
	blocked := &Scout{shield: sh, availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return searchFixtureResponse(200, "text/html", "unexpected"), nil
	})}}
	blocked.UseDrivers(testDrivers(t))
	if result := preflight(t, blocked, filmzieFixtureSource(), "ALL", "TZ"); requests.Load() != 0 || result.PlaybackCheck == nil || result.PlaybackCheck.Reason != "blocked_with_protection" {
		t.Fatalf("a protected host was fetched or misattributed: %+v", result.PlaybackCheck)
	}
}

func TestPastedProviderLinksKeepTheirBrandAccessLabels(t *testing.T) {
	s := checkScout(t, func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("no request expected") })
	filmzie, ok := s.providerMovieURLInput("https://filmzie.com/content/imprint-2007", "ALL")
	if !ok || !strings.EqualFold(filmzie.Title, "Imprint") || filmzie.ReleaseYear != 2007 || filmzie.Provider != "Filmzie" || filmzie.AccessType != "free" || filmzie.AccessBasis != "provider_model" || filmzie.AccessEvidenceURL != filmzieAccessEvidence {
		t.Fatalf("pasted Filmzie link: %+v", filmzie)
	}
	pluto, ok := s.providerMovieURLInput(plutoFeatureMoviePage, "ALL")
	if !ok || pluto.Title != "Pluto TV movie" || pluto.Provider != "Pluto TV" || pluto.AccessType != "free" || pluto.AccessEvidenceURL != plutoAccessEvidence {
		t.Fatalf("pasted Pluto link: %+v", pluto)
	}
	if _, ok := s.providerMovieURLInput("https://pluto.tv/us/watch/live-tv/123", "ALL"); ok {
		t.Fatal("a channel is not a provider movie link")
	}
}

// These opt-in checks record the current genuine provider responses from
// this connection. They never override the country or attempt an
// undocumented API.
func TestLivePlutoFeatureMoviePagePreflight(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_PLUTO_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_PLUTO_TEST=1 to inspect the public movie page from this connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s := &Scout{shield: shield.New(), availabilityHTTPClient: newMovieAvailabilityClient()}
	s.UseDrivers(testDrivers(t))
	country, _ := s.availabilityCheckCountry(ctx)
	result := VerifiedSource{CandidateSource: plutoFeatureCandidate(), VerificationStatus: "listing"}
	s.inspectProviderMoviePlayback(ctx, &result, "ALL", country)
	encoded, _ := json.Marshal(result.PlaybackCheck)
	t.Logf("first-party Pluto movie observation: %s", encoded)
	if result.PlaybackCheck == nil || result.PlaybackCheck.PlaybackConfirmed {
		t.Fatal("public page preflight must never assert native playback")
	}
}

func TestLiveFilmzieProtectedPreflight(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FILMZIE_PLAYBACK") != "1" {
		t.Skip("set ITEMBA_LIVE_FILMZIE_PLAYBACK=1 for the bounded Filmzie public guest preflight")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Scout{shield: shield.New(), availabilityHTTPClient: newMovieAvailabilityClient()}
	s.UseDrivers(testDrivers(t))
	country, _ := s.availabilityCheckCountry(ctx)
	result := VerifiedSource{CandidateSource: filmzieFixtureSource(), VerificationStatus: "listing"}
	s.inspectProviderMoviePlayback(ctx, &result, "ALL", country)
	proof, _ := json.Marshal(result.PlaybackCheck)
	t.Logf("Filmzie protected public preflight: %s", proof)
	if result.PlaybackCheck != nil && result.PlaybackCheck.PlaybackConfirmed {
		t.Fatal("provider preflight alone claimed decoded playback")
	}
	if result.PlaybackCheck == nil || result.PlaybackCheck.Reason != "region_unavailable" {
		t.Log("The provider's current result differs from its observed country block; playback remains unconfirmed.")
	}
}
