package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const filmzieFixtureMainID = "63a58b76d7cd4dd230c2f862"

func filmzieFixtureSource() CandidateSource {
	return CandidateSource{ID: "filmzie-imprint", Title: "Imprint", ReleaseYear: 2007, URL: "https://filmzie.com/content/imprint-2007", AccessType: "free", ListingOnly: true}
}

func filmzieFixtureState() filmziePageState {
	var state filmziePageState
	state.ContentPage.Content.ID = "63a58b76d7cd4dd230c2f871"
	state.ContentPage.Content.Type = "MOVIE"
	state.ContentPage.Content.Title = "Imprint"
	state.ContentPage.Content.Duration = 5079
	state.ContentPage.Content.Released = "2007-01-01T00:00:00.000Z"
	state.ContentPage.Content.ShareLink = filmzieFixtureSource().URL
	state.ContentPage.Content.SefName = "imprint-2007"
	state.ContentPage.Content.Videos = []filmzieMainVideo{
		{ID: filmzieFixtureMainID, Type: "PAID_CONTENT", Duration: 5079, Formats: map[string]filmzieVideoFormat{"HLS_SSAI": {Approved: true, JobStatus: "SUCCESS"}}},
		{ID: "63a58b76d7cd4dd230c2f869", Type: "TRAILER", Duration: 113},
	}
	available := true
	state.ContentPage.Available = &available
	return state
}

func filmzieReadyFixtureSource() CandidateSource {
	return boundFilmzieSource(filmzieFixtureSource(), "Imprint", 2007, 5079, "https://filmzie.com/api/v1/video/stream/"+filmzieFixtureMainID, "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture", false)
}

func filmzieFixturePage(state filmziePageState) string {
	encoded, _ := json.Marshal(state)
	return `<html><script>window['APP_STATE'] = ` + string(encoded) + `;</script></html>`
}

func filmzieFixtureClient(t *testing.T, state filmziePageState, apiStatus int, apiBody string, calls *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
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
	})}
}

func TestFilmziePlaybackHonorsProviderCountryRestrictionWithoutFetchingMedia(t *testing.T) {
	calls := 0
	client := filmzieFixtureClient(t, filmzieFixtureState(), 200, `{"error":"not_available_in_your_country"}`, &calls)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://filmzie.com/")
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "fixture-account"}})
	client.Jar = jar
	source, check := inspectFilmziePlayback(context.Background(), client, filmzieFixtureSource())
	if check == nil || check.Status != "not_started" || check.Reason != "region_unavailable" || check.PlaybackConfirmed || check.Method != "provider_playback_preflight" || source.providerStreamURL != "" || source.DirectStreamURL != "" || calls != 2 {
		t.Fatalf("country block was ignored or media guessed: check=%+v calls=%d", check, calls)
	}
	if !strings.HasSuffix(check.EvidenceURL, filmzieFixtureMainID) || check.CheckedAt == "" {
		t.Fatal("preflight evidence/timestamp missing")
	}
}

func TestFilmziePlaybackNeverConfirmsTrailerDRMOrUnattributedSSAI(t *testing.T) {
	for _, test := range []struct{ name, body, reason string }{
		{"trailer", `{"data":{"type":"TRAILER","source":{"hlsV2":"https://media.filmzie-cdn.net/trailer.m3u8"}}}`, "trailer_only"},
		{"drm", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":{"src":"https://media.filmzie-cdn.net/movie.m3u8","keySystems":{"com.widevine.alpha":"https://license.filmzie.com/required"}}}}}`, "drm_required"},
		{"server_inserted_ads", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/movie.m3u8?token=fixture"}}}`, "content_unconfirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := filmzieFixtureClient(t, filmzieFixtureState(), 200, test.body, &calls)
			source, check := inspectFilmziePlayback(context.Background(), client, filmzieFixtureSource())
			if check == nil || check.Reason != test.reason || check.PlaybackConfirmed || source.DirectStreamURL != "" || calls != 2 {
				t.Fatalf("unverified provider asset was accepted: %+v", check)
			}
			if test.name == "server_inserted_ads" && (!source.providerContentBound || !source.providerSSAI || source.providerStreamURL == "") {
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
	state.ContentPage.Content.Videos[0].Formats["MP4_WURL"] = filmzieVideoFormat{Approved: true, JobStatus: "SUCCESS", LegacyURL: "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture"}
	calls := 0
	client := filmzieFixtureClient(t, state, 200, `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/ssai.m3u8"}}}`, &calls)
	source, check := inspectFilmziePlayback(context.Background(), client, filmzieFixtureSource())
	if check != nil || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != state.ContentPage.Content.Videos[0].Formats["MP4_WURL"].LegacyURL || source.DirectStreamURL != "" || source.Duration != "5079" || source.AccessBasis != "filmzie_movie_metadata" || calls != 2 {
		t.Fatalf("explicit main asset did not stay private pending native verification: check=%+v", check)
	}
	page, _ := url.Parse(source.URL)
	media, _ := url.Parse(source.providerStreamURL)
	evidence, _ := url.Parse(source.AccessEvidenceURL)
	if !filmzieResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("exact authorized source association rejected")
	}
	media.Path = "/trailer.mp4"
	if filmzieResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("another media asset inherited main movie authorization")
	}
}

func TestFilmziePlaybackRejectsMismatchedMovieRuntimeAndAmbiguousAssets(t *testing.T) {
	for _, mutate := range []func(*filmziePageState){
		func(s *filmziePageState) { s.ContentPage.Content.Title = "Different Movie" },
		func(s *filmziePageState) { s.ContentPage.Content.Released = "2020-01-01T00:00:00Z" },
		func(s *filmziePageState) { s.ContentPage.Content.Videos[0].Duration = 113 },
		func(s *filmziePageState) {
			s.ContentPage.Content.Videos = append(s.ContentPage.Content.Videos, s.ContentPage.Content.Videos[0])
		},
		func(s *filmziePageState) {
			s.ContentPage.Content.ShareLink = "https://filmzie.com/content/different-film"
		},
	} {
		state := filmzieFixtureState()
		mutate(&state)
		calls := 0
		client := filmzieFixtureClient(t, state, 200, `{"data":{"type":"PAID_CONTENT"}}`, &calls)
		_, check := inspectFilmziePlayback(context.Background(), client, filmzieFixtureSource())
		if check == nil || check.Reason != "content_unconfirmed" || calls != 1 {
			t.Fatalf("wrong or partial movie reached the stream API: %+v calls=%d", check, calls)
		}
	}
}

func TestFilmziePlaybackHeaderGuardAndResponseBounds(t *testing.T) {
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		response := searchFixtureResponse(302, "text/plain", "")
		response.Header.Set("Location", "http://127.0.0.1/private")
		response.Request = req
		return response, nil
	})}
	if _, _, _, err := fetchFilmziePlaybackDocument(context.Background(), client, filmzieFixtureSource().URL, 1024); err == nil {
		t.Fatal("provider metadata redirected to a private or unprotected origin")
	}
	client.Transport = searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		response := searchFixtureResponse(200, "text/html", strings.Repeat("x", 1025))
		response.Request = req
		return response, nil
	})
	if _, _, _, err := fetchFilmziePlaybackDocument(context.Background(), client, filmzieFixtureSource().URL, 1024); err == nil {
		t.Fatal("provider metadata exceeded its byte budget")
	}
}

func TestLiveFilmzieProtectedPreflight(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FILMZIE_PLAYBACK") != "1" {
		t.Skip("set ITEMBA_LIVE_FILMZIE_PLAYBACK=1 for the bounded Filmzie public guest preflight")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	s := &Scout{availabilityHTTPClient: client}
	country, _ := s.availabilityCheckCountry(ctx)
	_, check := inspectFilmziePlayback(ctx, client, filmzieFixtureSource())
	if check == nil {
		t.Fatal("live source became authorized; run a separate native movie sample before claiming playback")
	}
	check.Country, check.RequestedRegion = country, "ALL"
	proof, _ := json.Marshal(check)
	t.Logf("Filmzie protected public preflight: %s", proof)
	if check.PlaybackConfirmed {
		t.Fatal("provider preflight alone claimed decoded playback")
	}
	if check.Reason != "region_unavailable" {
		t.Log("The provider's current result differs from its observed country block; playback remains unconfirmed.")
	}
}

func TestFilmziePlaybackDistinguishesGuestRestrictionsFromShieldBlocksAndTimeouts(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		calls := 0
		client := filmzieFixtureClient(t, filmzieFixtureState(), status, `{"error":"automated_request_restricted"}`, &calls)
		_, check := inspectFilmziePlayback(context.Background(), client, filmzieFixtureSource())
		if check == nil || check.Status != "unconfirmed" || check.Reason != "content_unconfirmed" {
			t.Fatalf("provider restriction was incorrectly attributed to Shield: %+v", check)
		}
	}
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, check := inspectFilmziePlayback(ctx, client, filmzieFixtureSource())
	if check == nil || check.Reason != "timeout" || check.PlaybackConfirmed {
		t.Fatalf("actual request deadline was not reported: %+v", check)
	}
}
