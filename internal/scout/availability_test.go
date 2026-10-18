package scout

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type availabilityFixtureTransport func(*http.Request) (*http.Response, error)

func (f availabilityFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func availabilityFixtureClient(status int, body string, headers http.Header) *http.Client {
	return &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
		if headers == nil {
			headers = http.Header{"Content-Type": {"text/html; charset=utf-8"}}
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
}

const availabilityMovieSynopsis = `<p>A team of explorers travels beyond this galaxy to discover whether humanity has a future among the stars. Watch the film or view the available offer.</p>`

func TestMovieAvailabilityChecksPageWithoutClaimingPlayback(t *testing.T) {
	body := `<html><head><title>Watch Interstellar (2014) | Netflix</title></head><body><main><h1>Interstellar</h1>` + availabilityMovieSynopsis + `</main></body></html>`
	result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(http.StatusOK, body, nil), CandidateSource{Title: "Interstellar", ReleaseYear: 2014, URL: "https://www.netflix.com/title/70305903"}, "TZ", "TZ")
	if result.Status != "page_reachable" || result.PlaybackConfirmed || result.Country != "TZ" || result.RequestedRegion != "TZ" || result.HTTPStatus != 200 || result.Method != "http_page" {
		t.Fatalf("unexpected page-only check: %+v", result)
	}
	if _, err := time.Parse(time.RFC3339, result.CheckedAt); err != nil {
		t.Fatalf("invalid check timestamp: %q", result.CheckedAt)
	}
	if !strings.Contains(result.Detail, "playback remain unconfirmed") {
		t.Fatalf("page check overstated playback: %s", result.Detail)
	}
}

func TestMovieAvailabilityHTTPFailuresStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "not_found"}, {http.StatusGone, "not_found"},
		{http.StatusForbidden, "access_restricted"}, {http.StatusTooManyRequests, "access_restricted"},
		{http.StatusUnavailableForLegalReasons, "region_blocked"}, {http.StatusUnauthorized, "sign_in_required"},
		{http.StatusBadGateway, "error"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(tc.status, "", nil), CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "US")
			if result.Status != tc.want || result.Country != "US" || result.PlaybackConfirmed {
				t.Fatalf("HTTP %d classified incorrectly: %+v", tc.status, result)
			}
		})
	}
}

func TestMovieAvailabilityUsesVisibleMatchingMovieEvidence(t *testing.T) {
	base := `<html><head><title>Interstellar (2014)</title></head><body><main><h1>Interstellar</h1>` + availabilityMovieSynopsis
	for _, tc := range []struct {
		name string
		html string
		want string
	}{
		{"explicit location restriction", `<p>This title is not available in your country.</p>`, "region_blocked"},
		{"inline location restriction", `<p>This title is not available in your <strong>country</strong>.</p>`, "region_blocked"},
		{"explicit Tanzania restriction", `<p>Interstellar is unavailable in Tanzania.</p>`, "region_blocked"},
		{"script data is not page evidence", `<script>window.metadata = "Interstellar is not available in your country";</script>`, "page_reachable"},
		{"JSON LD is not page evidence", `<script type="application/ld+json">{"description":"Not available in your country"}</script>`, "page_reachable"},
		{"style data is not page evidence", `<style>.gate::after{content:"Not available in your country"}</style>`, "page_reachable"},
		{"comments are not page evidence", `<!-- Not available in your country -->`, "page_reachable"},
		{"hidden text is not page evidence", `<div hidden>Not available in your country</div>`, "page_reachable"},
		{"aria hidden text is not page evidence", `<div aria-hidden="true">Not available in your country</div>`, "page_reachable"},
		{"CSS hidden text is not page evidence", `<div style="display: none">Not available in your country</div>`, "page_reachable"},
		{"hidden utility class is not page evidence", `<div class="is-hidden">Not available in your country</div>`, "page_reachable"},
		{"unopened dialog is not page evidence", `<dialog>Not available in your country</dialog>`, "page_reachable"},
		{"trailer restriction is not movie restriction", `<p>The trailer is not available in your country.</p>`, "page_reachable"},
		{"preview restriction is not movie restriction", `<p>The preview is not available in your country.</p>`, "page_reachable"},
		{"trailer wrapper is ignored", `<section class="movie-trailer"><h2>Trailer</h2><p>Not available in your country</p></section>`, "page_reachable"},
		{"FAQ question is not movie evidence", `<p>Why is this movie not available in your country?</p>`, "page_reachable"},
		{"FAQ wrapper is ignored", `<section id="faq"><p>Not available in your country</p></section>`, "page_reachable"},
		{"FAQ heading scopes following text", `<section><h2>Frequently Asked Questions</h2><p>This title is not available in your country</p></section>`, "page_reachable"},
		{"recommendation wrapper is ignored", `<section class="recommendations"><h2>Another movie</h2><p>Not available in your country</p></section>`, "page_reachable"},
		{"recommendation heading scopes following text", `<div><h2>More like this</h2><p>Not available in your country</p></div>`, "page_reachable"},
		{"navigation does not establish movie restriction", `<nav>Not available in your country</nav>`, "page_reachable"},
		{"footer does not establish movie restriction", `<footer>Not available in your country</footer>`, "page_reachable"},
		{"uncertain phrasing is not a block", `<p>This title may be not available in your country.</p>`, "page_reachable"},
		{"other country is not check country", `<p>This title is not available in the United States.</p>`, "page_reachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, base+tc.html+`</main></body></html>`, nil), CandidateSource{Title: "Interstellar", ReleaseYear: 2014, URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
			if result.Status != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, result)
			}
		})
	}
}

func TestMovieAvailabilityDoesNotTrustWrongFilmOrShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"wrong film", `<title>Inception</title><main><h1>Inception</h1>` + availabilityMovieSynopsis + `<p>Not available in your country.</p></main>`},
		{"wrong release year", `<title>Interstellar (2015)</title><main><h1>Interstellar</h1>` + availabilityMovieSynopsis + `<p>Not available in your country.</p></main>`},
		{"wrong primary heading", `<title>Interstellar (2014)</title><main><h1>Inception</h1>` + availabilityMovieSynopsis + `<p>Not available in your country.</p></main>`},
		{"derivative page", `<title>Interstellar trailer</title><main><h1>Interstellar</h1>` + availabilityMovieSynopsis + `<p>Not available in your country.</p></main>`},
		{"JavaScript shell", `<title>Interstellar (2014)</title><main><h1>Interstellar</h1><div id="app"></div><script>app.render()</script></main>`},
		{"challenge with movie title", `<title>Interstellar (2014)</title><main><h1>Interstellar</h1><p>Please verify you are human before continuing.</p>` + availabilityMovieSynopsis + `</main>`},
		{"title only in recommendations", `<title>Welcome</title><main><h1>Find a film</h1><div class="recommended"><h1>Interstellar</h1><p>Not available in your country</p></div><p>Browse our catalog of movies and television series to find your next film.</p></main>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, tc.body, nil), CandidateSource{Title: "Interstellar", ReleaseYear: 2014, URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
			if result.Status != "unconfirmed" || result.PlaybackConfirmed {
				t.Fatalf("false availability confirmation: %+v", result)
			}
		})
	}
}

func TestMovieAvailabilitySeparatesUnknownCountryAndRequestedCountry(t *testing.T) {
	body := `<title>Interstellar</title><main><h1>Interstellar</h1>` + availabilityMovieSynopsis + `</main>`
	result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, body, nil), CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "")
	if result.Status != "page_reachable" || result.Country != "" || result.RequestedRegion != "TZ" {
		t.Fatalf("requested country must not manufacture check country: %+v", result)
	}
}

func TestMovieAvailabilityIgnoresHeaderAccountFormWhenMainMovieIsReachable(t *testing.T) {
	body := `<title>Interstellar</title><body><header><form><p>Sign in to your account</p><input type="password"></form></header><main><h1>Interstellar</h1>` + availabilityMovieSynopsis + `</main></body>`
	result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, body, nil), CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "page_reachable" {
		t.Fatalf("unrelated account form was mistaken for the main page: %+v", result)
	}
}

func TestMovieAvailabilityRecognizesMainSignInGate(t *testing.T) {
	body := `<title>Sign in</title><main><h1>Sign in to watch Interstellar</h1><form><p>Sign in with your account</p><input type="password"></form></main>`
	result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, body, nil), CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "sign_in_required" || result.PlaybackConfirmed {
		t.Fatalf("sign-in gate classification failed: %+v", result)
	}
}

func TestMovieAvailabilityRecognizesSoft404WithoutRejectingFilmNamedPageNotFound(t *testing.T) {
	for _, tc := range []struct {
		name, title, heading, want string
	}{
		{"stale title with missing page", "Interstellar", "Page not found", "not_found"},
		{"missing movie", "Interstellar", "Movie not found", "not_found"},
		{"404 heading", "Interstellar", "404", "not_found"},
		{"film named Page Not Found", "Page Not Found", "Page Not Found", "page_reachable"},
		{"film named 404", "404", "404", "page_reachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<title>` + tc.title + `</title><main><h1>` + tc.heading + `</h1>` + availabilityMovieSynopsis + `</main>`
			result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, body, nil), CandidateSource{Title: tc.title, URL: "https://movies.example.com/movie/example"}, "TZ", "TZ")
			if result.Status != tc.want {
				t.Fatalf("want %s got %+v", tc.want, result)
			}
		})
	}
}

func TestMovieAvailabilityCapsRedirectsAndHonorsCancellation(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://movies.example.com/again"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	result := checkMoviePageAvailability(context.Background(), client, CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "error" || calls != 5 {
		t.Fatalf("redirect limit not enforced calls=%d result=%+v", calls, result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client = &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.Context().Err() == nil {
			t.Fatal("caller cancellation was lost")
		}
		return nil, request.Context().Err()
	})}
	result = checkMoviePageAvailability(ctx, client, CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "error" {
		t.Fatalf("canceled check should not confirm page: %+v", result)
	}
}

func TestMovieAvailabilityLoginRedirectAndPrivateRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, destination, want string
		requests                int
	}{
		{"login redirect", "https://movies.example.com/account/sign-in", "sign_in_required", 2},
		{"private redirect", "http://127.0.0.1/internal", "error", 1},
		{"private IPv6 redirect", "http://[::1]/internal", "error", 1},
		{"credentials redirect", "https://user:password@movies.example.com/login", "error", 1},
		{"alternate port redirect", "https://movies.example.com:8443/login", "error", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {tc.destination}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(`<title>Sign in</title><p>Sign in to your account</p>`)), Request: request}, nil
			})}
			result := checkMoviePageAvailability(context.Background(), client, CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
			if result.Status != tc.want || calls != tc.requests {
				t.Fatalf("unexpected redirect check calls=%d: %+v", calls, result)
			}
		})
	}
}

func TestMovieAvailabilityBoundsResponseAndFailureDetails(t *testing.T) {
	large := `<title>Interstellar</title><main><h1>Interstellar</h1>` + strings.Repeat("a", availabilityPageLimit) + `</main>`
	result := checkMoviePageAvailability(context.Background(), availabilityFixtureClient(200, large, nil), CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "unconfirmed" || !strings.Contains(result.Detail, "bounded") {
		t.Fatalf("oversized page should remain unconfirmed: %+v", result)
	}
	client := &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("private upstream detail and credentials must never escape")
	})}
	result = checkMoviePageAvailability(context.Background(), client, CandidateSource{Title: "Interstellar", URL: "https://movies.example.com/movie/interstellar"}, "TZ", "TZ")
	if result.Status != "error" || strings.Contains(result.Detail, "credentials") {
		t.Fatalf("unsafe or missing failure detail: %+v", result)
	}
}

func TestMovieAvailabilityRejectsPrivateDestinationsBeforeRequest(t *testing.T) {
	for _, raw := range []string{
		"http://localhost/movie", "http://localhost./movie", "http://127.0.0.1/movie", "http://127.1/movie", "http://10.0.0.1/movie",
		"http://192.168.1.1/movie", "http://169.254.169.254/latest/meta-data", "http://100.100.100.200/meta-data", "http://[::1]/movie", "http://[::ffff:127.0.0.1]/movie",
		"http://[fe80::1]/movie", "http://[fc00::1]/movie", "https://movies.local/movie", "https://movies.internal/movie", "https://movies.example.com:9000/movie", "file:///movie",
	} {
		t.Run(raw, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: availabilityFixtureTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected network request")
			})}
			result := checkMoviePageAvailability(context.Background(), client, CandidateSource{Title: "Interstellar", URL: raw}, "TZ", "TZ")
			if calls != 0 || result.Status != "unconfirmed" {
				t.Fatalf("unsafe URL requested: calls=%d result=%+v", calls, result)
			}
		})
	}
}

func TestMovieAvailabilityPublicIPRules(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true}, {"1.1.1.1", true}, {"2606:4700:4700::1111", true},
		{"0.0.0.0", false}, {"127.0.0.1", false}, {"10.0.0.1", false}, {"172.16.5.5", false}, {"192.168.1.1", false},
		{"169.254.169.254", false}, {"100.64.0.1", false}, {"198.18.0.1", false}, {"192.0.2.1", false}, {"203.0.113.1", false},
		{"::1", false}, {"::ffff:127.0.0.1", false}, {"fc00::1", false}, {"fe80::1", false}, {"2001:db8::1", false}, {"64:ff9b::7f00:1", false}, {"2002:7f00:1::", false},
	} {
		if got := availabilityPublicIP(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("public(%s)=%v, want %v", tc.ip, got, tc.want)
		}
	}
}
