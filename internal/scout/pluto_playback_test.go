package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const plutoFeatureMoviePage = "https://pluto.tv/us/on-demand/movies/61c2323f68b5ad001a6211e1/details"

func plutoFeatureCandidate() CandidateSource {
	return CandidateSource{ID: "web-pluto-feature", Title: "Rugrats Go Wild", ReleaseYear: 2003, URL: plutoFeatureMoviePage, Provider: "Pluto TV", SourceType: "Web Listing", AccessType: "free", AccessBasis: "provider_model", ListingOnly: true}
}

func TestPlutoPlaybackTargetOnlyAcceptsFreeIndividualMoviePages(t *testing.T) {
	if !plutoMoviePlaybackTarget(plutoFeatureCandidate()) {
		t.Fatal("individual free Pluto movie must be a supported native-check target")
	}
	for _, raw := range []string{
		"https://pluto.tv/us/movies/", "https://pluto.tv/us/on-demand/movies", "https://pluto.tv/us/on-demand/series/123/season/1", "https://pluto.tv/us/watch/live-tv/123", "https://pluto.tv/us/on-demand/movies/123/trailer", "https://pluto.tv/us/on-demand/movies/123/reviews", "https://support.pluto.tv/us/on-demand/movies/123", "https://pluto.tv.evil.net/us/on-demand/movies/123", "http://127.0.0.1/us/on-demand/movies/123", "https://user:pass@pluto.tv/us/on-demand/movies/123",
	} {
		source := plutoFeatureCandidate()
		source.URL = raw
		if plutoMoviePlaybackTarget(source) {
			t.Errorf("unsupported Pluto destination accepted: %s", raw)
		}
	}
	source := plutoFeatureCandidate()
	source.AccessType = "subscription"
	if plutoMoviePlaybackTarget(source) {
		t.Fatal("paid candidates must never be included in a free provider sample")
	}
}

func TestPlutoLocationBlockRequiresPrimaryVisibleProviderHeading(t *testing.T) {
	final, _ := url.Parse("https://pluto.tv/intl/")
	if !plutoLocationBlockedPage([]byte(`<main><h3>Pluto TV is not available in your location.</h3><p>Thanks for your patience.</p></main>`), final) {
		t.Fatal("first-party generic country-block page must remain a region observation")
	}
	for _, html := range []string{
		`<h1>Rugrats Go Wild</h1><footer><h3>Pluto TV is not available in your location.</h3></footer>`,
		`<h1>Rugrats Go Wild</h1><div hidden><h3>Pluto TV is not available in your location.</h3></div>`,
		`<h1>Rugrats Go Wild</h1><section class="faq"><h3>Pluto TV is not available in your location.</h3></section>`,
		`<h1>Rugrats Go Wild</h1><script>"Pluto TV is not available in your location."</script>`,
		`<h3>Why is Pluto TV is not available in your location?</h3>`,
		`<h1>Access denied</h1><p>HTTP 403 does not establish a country block.</p>`,
	} {
		if plutoLocationBlockedPage([]byte(html), final) {
			t.Errorf("non-primary or ambiguous region evidence accepted: %s", html)
		}
	}
	other, _ := url.Parse("https://pluto.tv.evil.net/intl/")
	if plutoLocationBlockedPage([]byte(`<h3>Pluto TV is not available in your location.</h3>`), other) {
		t.Fatal("a different host cannot establish Pluto's service availability")
	}
}

func TestPlutoGuardedPageProbePreservesActualRegionBlockWithoutPlayback(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		requests++
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
	})}
	check := checkPlutoPlaybackPage(context.Background(), client, plutoFeatureCandidate(), "ALL", "TZ")
	if check == nil || check.Status != "region_blocked" || check.PlaybackConfirmed || check.Country != "TZ" || check.RequestedRegion != "ALL" || check.EvidenceURL != "https://pluto.tv/intl/" || requests != 2 {
		t.Fatalf("provider location block was lost or overstated as playback: %#v, requests=%d", check, requests)
	}
}

func TestPlutoReachableMoviePageCannotConfirmPlaybackOrAdsRemoved(t *testing.T) {
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		return additionalFeatureResponse(request, `<title>Rugrats Go Wild (2003) | Pluto TV</title><main><h1>Rugrats Go Wild</h1><button>Watch Now</button><p>The requested movie is offered by the provider. A visible movie action or an HTTP success is not native movie playback.</p></main>`), nil
	})}
	check := checkPlutoPlaybackPage(context.Background(), client, plutoFeatureCandidate(), "ALL", "")
	if check == nil || check.Status != "page_reachable" || check.PlaybackConfirmed || !strings.Contains(check.Detail, "in-stream advertisements remain unconfirmed") {
		t.Fatalf("a page response must not become movie/ad-removal proof: %#v", check)
	}
}

func TestPlutoPageProbeRejectsPrivateRedirectWithoutFetchingIt(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	check := checkPlutoPlaybackPage(context.Background(), client, plutoFeatureCandidate(), "ALL", "TZ")
	if check == nil || check.Status != "unconfirmed" || check.PlaybackConfirmed || requests != 1 {
		t.Fatalf("private redirects must remain unfetched/unconfirmed: %#v, requests=%d", check, requests)
	}
}

func TestInspectPlutoPlaybackRecordsReasonsWithoutDecodedClaims(t *testing.T) {
	for _, tc := range []struct{ name, html, finalPath, status, reason string }{
		{"region restriction", `<h3>Pluto TV is not available in your location.</h3>`, "/intl/", "not_started", "region_unavailable"},
		{"sign in required", `<main><h1>Sign in</h1><form><label>Sign in to watch</label><input type="password"></form></main>`, "/login/", "not_started", "sign_in_required"},
		{"matching page without movie asset proof", `<title>Rugrats Go Wild (2003) | Pluto TV</title><main><h1>Rugrats Go Wild</h1><button>Watch Now</button></main>`, "/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", "unconfirmed", "content_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				response := additionalFeatureResponse(request, tc.html)
				clone := request.Clone(request.Context())
				clone.URL, _ = url.Parse("https://pluto.tv" + tc.finalPath)
				response.Request = clone
				return response, nil
			})}
			source := plutoFeatureCandidate()
			returned, check := inspectPlutoPlayback(context.Background(), client, source)
			if returned.URL != source.URL || returned.DirectStreamURL != "" || check == nil || check.Status != tc.status || check.Reason != tc.reason || check.Method != "provider_playback_preflight" || check.CheckedAt == "" || check.PlaybackConfirmed || check.DecodedFrames != 0 || check.SecondsAdvanced != 0 || check.DurationSeconds != 0 || check.Width != 0 || check.Height != 0 {
				t.Fatalf("preflight status became native playback evidence: %#v %#v", returned, check)
			}
			if check.Country != "" || check.RequestedRegion != "" {
				t.Fatal("integration supplies the actual check country and requested preference")
			}
		})
	}
}

func TestInspectPlutoPlaybackDoesNotFetchUnsupportedOrPaidSources(t *testing.T) {
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		t.Fatal("unsupported source triggered a public provider request")
		return nil, fmt.Errorf("unexpected request")
	})}
	source := plutoFeatureCandidate()
	source.AccessType = "subscription"
	if _, check := inspectPlutoPlayback(context.Background(), client, source); check != nil {
		t.Fatal("paid source must remain outside free-provider playback verification")
	}
	source.AccessType, source.URL = "free", "https://pluto.tv/us/watch/live-tv/123"
	if _, check := inspectPlutoPlayback(context.Background(), client, source); check != nil {
		t.Fatal("a channel is not an individual on-demand movie")
	}
}

// This opt-in check records the current genuine feature-movie link's provider
// response. It never overrides the country or attempts an undocumented API.
func TestLivePlutoFeatureMoviePagePreflight(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_PLUTO_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_PLUTO_TEST=1 to inspect the public movie page from this connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	scout := &Scout{availabilityHTTPClient: client}
	country, _ := scout.availabilityCheckCountry(ctx)
	check := checkPlutoPlaybackPage(ctx, client, plutoFeatureCandidate(), "ALL", country)
	encoded, _ := json.Marshal(check)
	t.Logf("first-party Pluto movie observation: %s", encoded)
	if check == nil || check.PlaybackConfirmed {
		t.Fatal("public page preflight must never assert native playback")
	}
}
