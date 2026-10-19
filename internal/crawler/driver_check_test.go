package crawler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The checks are held to the legacy scout inspectors' own tests. The
// pipeline's judgments, what a page shows and whether declared metadata
// names the film, come from a stub here; the scout tests use the real ones.

type stubEnvironment struct {
	facts   map[string]string
	matches bool
}

func (e stubEnvironment) PageFacts([]byte, *url.URL) map[string]string { return e.facts }
func (e stubEnvironment) MetadataMatches(string, int, float64) bool    { return e.matches }

func factsFor(identity string, extra ...string) stubEnvironment {
	facts := map[string]string{"challenge": "false", "sign_in": "false", "soft_not_found": "false", "identity": identity}
	for i := 0; i+1 < len(extra); i += 2 {
		facts[extra[i]] = extra[i+1]
	}
	return stubEnvironment{facts: facts, matches: true}
}

const plutoFeaturePage = "https://pluto.tv/us/on-demand/movies/61c2323f68b5ad001a6211e1/details"

// servedAt answers a request with an HTML body as if it arrived from final.
func servedAt(req *http.Request, final, body string) *http.Response {
	r := htmlResponse(req, body)
	clone := req.Clone(req.Context())
	clone.URL, _ = url.Parse(final)
	r.Request = clone
	return r
}

func TestPlutoCheckRecognisesOnlyIndividualMoviePages(t *testing.T) {
	d := shippedDrivers(t)["pluto-tv"]
	if d == nil || d.Spec != nil || d.Check == nil || !d.Checks(plutoFeaturePage) || !d.Checks("https://www.pluto.tv/on-demand/movies/abc") || !d.Checks("https://pluto.tv/en-gb/watch/on-demand/movies/abc/details/") {
		t.Fatal("individual free Pluto movie pages must be check targets")
	}
	for _, raw := range []string{
		"https://pluto.tv/us/movies/", "https://pluto.tv/us/on-demand/movies", "https://pluto.tv/us/on-demand/series/123/season/1", "https://pluto.tv/us/watch/live-tv/123", "https://pluto.tv/us/on-demand/movies/123/trailer", "https://pluto.tv/us/on-demand/movies/123/reviews", "https://support.pluto.tv/us/on-demand/movies/123", "https://pluto.tv.evil.net/us/on-demand/movies/123", "http://127.0.0.1/us/on-demand/movies/123", "https://user:pass@pluto.tv/us/on-demand/movies/123",
	} {
		if d.Checks(raw) {
			t.Errorf("unsupported Pluto destination accepted: %s", raw)
		}
	}
	if d.URLTitle(plutoFeaturePage) != "" || d.Host() != "pluto.tv" || d.StatusName("") != "Pluto TV" || d.SearchURL("x", "") != "" {
		t.Fatal("a check-only driver has no slug title, search status or search URL")
	}
}

func TestPlutoCheckReadsTheServicesOwnRefusalAndNothingElse(t *testing.T) {
	d := shippedDrivers(t)["pluto-tv"]
	blocked := `<main><h3>Pluto TV is not available in your location.</h3><p>Thanks for your patience.</p></main>`
	var calls atomic.Int32
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("provider preflight must not include account state")
		}
		if req.URL.String() == plutoFeaturePage {
			r := jsonResponse(302, "")
			r.Header.Set("Location", "https://pluto.tv/intl/")
			r.Request = req
			return r, nil
		}
		return htmlResponse(req, blocked), nil
	}))
	result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, factsFor("mismatch"))
	if result.Status != "not_started" || result.Reason != "region_unavailable" || result.EvidenceURL != "https://pluto.tv/intl/" || calls.Load() != 2 || result.Bind != nil || result.Metadata != nil {
		t.Fatalf("provider location block was lost or overstated: %+v calls=%d", result, calls.Load())
	}
	for name, html := range map[string]string{
		"footer":   `<h1>Rugrats Go Wild</h1><footer><h3>Pluto TV is not available in your location.</h3></footer>`,
		"hidden":   `<h1>Rugrats Go Wild</h1><div hidden><h3>Pluto TV is not available in your location.</h3></div>`,
		"faq":      `<h1>Rugrats Go Wild</h1><section class="faq"><h3>Pluto TV is not available in your location.</h3></section>`,
		"script":   `<h1>Rugrats Go Wild</h1><script>"Pluto TV is not available in your location."</script>`,
		"question": `<h3>Why is Pluto TV is not available in your location?</h3>`,
		"denied":   `<h1>Access denied</h1><p>HTTP 403 does not establish a country block.</p>`,
	} {
		fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return servedAt(req, "https://pluto.tv/intl/", html), nil
		}))
		if result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, factsFor("mismatch")); result.Reason != "content_unconfirmed" || result.Status != "unconfirmed" || !strings.Contains(result.Detail, "did not establish") {
			t.Errorf("%s: non-primary or ambiguous region evidence accepted: %+v", name, result)
		}
	}
	// A challenge page is not a verdict about the service either.
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return servedAt(req, "https://pluto.tv/intl/", blocked), nil
	}))
	if result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, factsFor("mismatch", "challenge", "true")); result.Reason != "content_unconfirmed" || !strings.Contains(result.Detail, "challenge") {
		t.Fatalf("challenge: %+v", result)
	}
	// Another host cannot establish Pluto's availability.
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return servedAt(req, "https://pluto.tv.evil.net/intl/", blocked), nil
	}))
	if result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, factsFor("mismatch")); result.Reason != "content_unconfirmed" || !strings.Contains(result.Detail, "redirected away") || result.EvidenceURL != plutoFeaturePage {
		t.Fatalf("foreign host: %+v", result)
	}
}

func TestPlutoCheckOutcomesStayPreflightsAndRefusePrivateRedirects(t *testing.T) {
	d := shippedDrivers(t)["pluto-tv"]
	for _, tc := range []struct {
		name, html, finalPath string
		env                   stubEnvironment
		status, reason, hint  string
	}{
		{"region restriction", `<h3>Pluto TV is not available in your location.</h3>`, "/intl/", factsFor("mismatch"), "not_started", "region_unavailable", "not available from this connection"},
		{"sign in required", `<main><h1>Sign in</h1></main>`, "/login/", factsFor("mismatch", "sign_in", "true"), "not_started", "sign_in_required", "requires sign-in"},
		{"matching page without asset proof", `<main><h1>Rugrats Go Wild</h1></main>`, "/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", factsFor("match"), "unconfirmed", "content_unconfirmed", "in-stream advertisements remain unconfirmed"},
		{"soft 404", `<main><h1>Page not found</h1></main>`, "/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", factsFor("mismatch", "soft_not_found", "true"), "unconfirmed", "content_unconfirmed", "did not establish"},
		{"another film", `<main><h1>Other Film</h1></main>`, "/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", factsFor("mismatch"), "unconfirmed", "content_unconfirmed", "did not establish"},
	} {
		fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return servedAt(req, "https://pluto.tv"+tc.finalPath, tc.html), nil
		}))
		result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, tc.env)
		if result.Status != tc.status || result.Reason != tc.reason || !strings.Contains(result.Detail, tc.hint) || result.Bind != nil || result.Metadata != nil {
			t.Errorf("%s: %+v", tc.name, result)
		}
	}
	var calls atomic.Int32
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		r := jsonResponse(302, "")
		r.Header.Set("Location", "http://127.0.0.1/private")
		r.Request = req
		return r, nil
	}))
	if result := d.RunCheck(context.Background(), fetcher, plutoFeaturePage, factsFor("mismatch")); result.Status != "unconfirmed" || !strings.Contains(result.Detail, "could not be checked") || calls.Load() != 1 || result.EvidenceURL != plutoFeaturePage {
		t.Fatalf("private redirects must remain unfetched: %+v calls=%d", result, calls.Load())
	}
}

const (
	filmzieMainID = "63a58b76d7cd4dd230c2f862"
	filmziePage   = "https://filmzie.com/content/imprint-2007"
)

func filmzieState() map[string]any {
	main := map[string]any{"id": filmzieMainID, "type": "PAID_CONTENT", "duration": 5079.0, "formats": map[string]any{"HLS_SSAI": map[string]any{"approved": true, "jobStatus": "SUCCESS"}}}
	trailer := map[string]any{"id": "63a58b76d7cd4dd230c2f869", "type": "TRAILER", "duration": 113.0}
	content := map[string]any{"id": "63a58b76d7cd4dd230c2f871", "type": "MOVIE", "title": "Imprint", "duration": 5079.0, "released": "2007-01-01T00:00:00.000Z", "shareLink": filmziePage, "sefName": "imprint-2007", "videos": []any{main, trailer}}
	return map[string]any{"contentPage": map[string]any{"content": content, "isAvailableInUserCountry": true}}
}

func filmzieContent(state map[string]any) map[string]any {
	return state["contentPage"].(map[string]any)["content"].(map[string]any)
}

func filmzieMain(state map[string]any) map[string]any {
	return filmzieContent(state)["videos"].([]any)[0].(map[string]any)
}

func filmzieFetcher(t *testing.T, state map[string]any, apiStatus int, apiBody string, calls *atomic.Int32) *Fetcher {
	t.Helper()
	encoded, _ := json.Marshal(state)
	page := `<html><script>window['APP_STATE'] = ` + string(encoded) + `;</script></html>`
	return NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("provider preflight forwarded cookies or authorization")
		}
		switch req.URL.String() {
		case filmziePage:
			return htmlResponse(req, page), nil
		case "https://filmzie.com/api/v1/video/stream/" + filmzieMainID:
			r := jsonResponse(apiStatus, apiBody)
			r.Request = req
			return r, nil
		}
		t.Errorf("preflight fetched a trailer, ad, guessed origin or movie media: %s", req.URL)
		r := jsonResponse(404, "")
		r.Request = req
		return r, nil
	}))
}

func TestFilmzieCheckHonoursCountryRestrictionsAndKeepsMetadata(t *testing.T) {
	d := shippedDrivers(t)["filmzie"]
	if d == nil || d.Spec != nil || !d.Checks(filmziePage) || !d.Checks("https://www.filmzie.com/content/imprint-2007/") || d.Checks("https://filmzie.com/content/") || d.Checks("https://filmzie.com/browse/imprint-2007") || d.URLTitle(filmziePage) != "imprint-2007" {
		t.Fatal("Filmzie content pages must be check targets with a slug title")
	}
	var calls atomic.Int32
	result := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), 200, `{"error":"not_available_in_your_country"}`, &calls), filmziePage, stubEnvironment{matches: true})
	if result.Status != "not_started" || result.Reason != "region_unavailable" || result.Bind != nil || calls.Load() != 2 || !strings.HasSuffix(result.EvidenceURL, filmzieMainID) {
		t.Fatalf("country block was ignored or media guessed: %+v calls=%d", result, calls.Load())
	}
	if result.Metadata == nil || result.Metadata.Title != "Imprint" || result.Metadata.Year != 2007 || result.Metadata.Duration != 5079 {
		t.Fatalf("matching page metadata was lost behind the restriction: %+v", result.Metadata)
	}
	state := filmzieState()
	state["contentPage"].(map[string]any)["isAvailableInUserCountry"] = false
	if r := d.RunCheck(context.Background(), filmzieFetcher(t, state, 200, `{"data":{"type":"PAID_CONTENT"}}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Reason != "region_unavailable" || r.Status != "not_started" {
		t.Fatalf("page country flag: %+v", r)
	}
	if r := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), 401, `{"error":"unauthorized"}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Reason != "sign_in_required" || r.Status != "not_started" {
		t.Fatalf("401: %+v", r)
	}
	for _, status := range []int{403, 429} {
		if r := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), status, `{"error":"automated_request_restricted"}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Status != "unconfirmed" || r.Reason != "content_unconfirmed" || !strings.Contains(r.Detail, "restricted the automated") {
			t.Fatalf("%d: %+v", status, r)
		}
		if r := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), status, `<html>blocked</html>`, &calls), filmziePage, stubEnvironment{matches: true}); r.Status != "unconfirmed" || !strings.Contains(r.Detail, "restricted the automated") {
			t.Fatalf("%d without JSON: %+v", status, r)
		}
	}
	if r := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), 200, `{"error":"unknown_error"}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Reason != "content_unconfirmed" || !strings.Contains(r.Detail, "did not authorize") {
		t.Fatalf("other provider error: %+v", r)
	}
	if r := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), 500, `{"data":{"type":"PAID_CONTENT"}}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Reason != "content_unconfirmed" || !strings.Contains(r.Detail, "did not authorize") {
		t.Fatalf("server error: %+v", r)
	}
}

func TestFilmzieCheckNeverBindsTrailersDRMOrUnattributedSSAIAsPlayable(t *testing.T) {
	d := shippedDrivers(t)["filmzie"]
	for _, tc := range []struct {
		name, body, reason string
		bound              bool
	}{
		{"trailer", `{"data":{"type":"TRAILER","source":{"hlsV2":"https://media.filmzie-cdn.net/trailer.m3u8"}}}`, "trailer_only", false},
		{"drm", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":{"src":"https://media.filmzie-cdn.net/movie.m3u8","keySystems":{"com.widevine.alpha":"https://license.filmzie.com/required"}}}}}`, "drm_required", false},
		{"server inserted ads", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/movie.m3u8?token=fixture"}}}`, "content_unconfirmed", true},
		{"other asset", `{"data":{"id":"000000000000000000000000","type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/movie.m3u8"}}}`, "content_unconfirmed", false},
		{"unparseable", `<html>oops</html>`, "content_unconfirmed", false},
		{"plain http stream", `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"http://media.filmzie-cdn.net/movie.m3u8"}}}`, "content_unconfirmed", false},
	} {
		var calls atomic.Int32
		result := d.RunCheck(context.Background(), filmzieFetcher(t, filmzieState(), 200, tc.body, &calls), filmziePage, stubEnvironment{matches: true})
		if result.Reason != tc.reason || (result.Bind != nil) != tc.bound || calls.Load() != 2 {
			t.Fatalf("%s: unverified provider asset was accepted: %+v calls=%d", tc.name, result, calls.Load())
		}
		if tc.bound && (!result.Bind.SSAI || result.Bind.Media == "" || result.Bind.AccessBasis != "filmzie_movie_metadata" || !strings.Contains(result.Detail, "server-inserted ads")) {
			t.Fatalf("%s: main asset association or SSAI uncertainty was lost: %+v", tc.name, result)
		}
	}
}

func TestFilmzieCheckBindsOnlyTheExplicitAuthorisedMovieFile(t *testing.T) {
	d := shippedDrivers(t)["filmzie"]
	state := filmzieState()
	filmzieMain(state)["formats"].(map[string]any)["MP4_WURL"] = map[string]any{"approved": true, "jobStatus": "SUCCESS", "legacyUrl": "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture"}
	var calls atomic.Int32
	result := d.RunCheck(context.Background(), filmzieFetcher(t, state, 200, `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/ssai.m3u8"}}}`, &calls), filmziePage, stubEnvironment{matches: true})
	if result.Bind == nil || result.Bind.SSAI || result.Bind.Media != "https://media.filmzie-cdn.net/full-movie.mp4?token=fixture" || result.Bind.Evidence != "https://filmzie.com/api/v1/video/stream/"+filmzieMainID || result.Bind.SourceType != "Provider free movie" || result.Metadata == nil || calls.Load() != 2 {
		t.Fatalf("explicit main asset was not bound: %+v", result)
	}
	// An unapproved, unfinished, storage-key or non-movie legacy entry is
	// not a file to sample; without the SSAI stream there is nothing left.
	for name, format := range map[string]map[string]any{
		"unapproved":       {"approved": false, "jobStatus": "SUCCESS", "legacyUrl": "https://media.filmzie-cdn.net/full-movie.mp4"},
		"unfinished":       {"approved": true, "jobStatus": "PENDING", "legacyUrl": "https://media.filmzie-cdn.net/full-movie.mp4"},
		"storage key":      {"approved": true, "jobStatus": "SUCCESS", "legacyUrl": "gs://bucket/full-movie.mp4"},
		"not a movie file": {"approved": true, "jobStatus": "SUCCESS", "legacyUrl": "https://media.filmzie-cdn.net/full-movie.ts"},
	} {
		state := filmzieState()
		formats := filmzieMain(state)["formats"].(map[string]any)
		formats["MP4_WURL"] = format
		delete(formats, "HLS_SSAI")
		var calls atomic.Int32
		if r := d.RunCheck(context.Background(), filmzieFetcher(t, state, 200, `{"data":{"type":"PAID_CONTENT","source":{"hlsV2":"https://media.filmzie-cdn.net/ssai.m3u8"}}}`, &calls), filmziePage, stubEnvironment{matches: true}); r.Bind != nil || r.Reason != "content_unconfirmed" || !strings.Contains(r.Detail, "attributable") {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

func TestFilmzieCheckStopsAtTheFirstMismatchBeforeTheStreamRequest(t *testing.T) {
	d := shippedDrivers(t)["filmzie"]
	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		env    stubEnvironment
		hint   string
	}{
		"other film":        {func(map[string]any) {}, stubEnvironment{matches: false}, "did not match"},
		"short asset":       {func(s map[string]any) { filmzieMain(s)["duration"] = 113.0 }, stubEnvironment{matches: true}, "did not match"},
		"runtime disagrees": {func(s map[string]any) { filmzieContent(s)["duration"] = 7200.0 }, stubEnvironment{matches: true}, "did not match"},
		"two main assets": {func(s map[string]any) {
			c := filmzieContent(s)
			c["videos"] = append(c["videos"].([]any), c["videos"].([]any)[0])
		}, stubEnvironment{matches: true}, "one full movie asset"},
		"no main asset":    {func(s map[string]any) { filmzieMain(s)["type"] = "EXTRA" }, stubEnvironment{matches: true}, "one full movie asset"},
		"other share link": {func(s map[string]any) { filmzieContent(s)["shareLink"] = "https://filmzie.com/content/different-film" }, stubEnvironment{matches: true}, "could not be identified"},
		"coming soon":      {func(s map[string]any) { filmzieContent(s)["comingSoon"] = true }, stubEnvironment{matches: true}, "could not be identified"},
		"a series":         {func(s map[string]any) { filmzieContent(s)["type"] = "SERIES" }, stubEnvironment{matches: true}, "could not be identified"},
		"odd asset id":     {func(s map[string]any) { filmzieMain(s)["id"] = "../private" }, stubEnvironment{matches: true}, "one full movie asset"},
	} {
		state := filmzieState()
		tc.mutate(state)
		var calls atomic.Int32
		result := d.RunCheck(context.Background(), filmzieFetcher(t, state, 200, `{"data":{"type":"PAID_CONTENT"}}`, &calls), filmziePage, tc.env)
		if result.Reason != "content_unconfirmed" || calls.Load() != 1 || result.Metadata != nil || result.Bind != nil || !strings.Contains(result.Detail, tc.hint) {
			t.Fatalf("%s: wrong or partial movie reached the stream API: %+v calls=%d", name, result, calls.Load())
		}
	}
	// No state, a page over its byte budget, a redirect off the page and a
	// deadline are each failures the check names, never guesses.
	var calls atomic.Int32
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return htmlResponse(req, "<html><p>no state</p></html>"), nil
	}))
	if r := d.RunCheck(context.Background(), fetcher, filmziePage, stubEnvironment{matches: true}); r.Reason != "content_unconfirmed" || calls.Load() != 1 || !strings.Contains(r.Detail, "could not be identified") {
		t.Fatalf("no state: %+v", r)
	}
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return htmlResponse(req, strings.Repeat("x", 512<<10+1)), nil
	}))
	if r := d.RunCheck(context.Background(), fetcher, filmziePage, stubEnvironment{matches: true}); !strings.Contains(r.Detail, "could not be checked") || r.EvidenceURL != filmziePage {
		t.Fatalf("oversized page: %+v", r)
	}
	calls.Store(0)
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		r := jsonResponse(302, "")
		r.Header.Set("Location", "https://filmzie.com/")
		r.Request = req
		return r, nil
	}))
	if r := d.RunCheck(context.Background(), fetcher, filmziePage, stubEnvironment{matches: true}); !strings.Contains(r.Detail, "could not be checked") || calls.Load() != 1 {
		t.Fatalf("redirect off the page: %+v calls=%d", r, calls.Load())
	}
	fetcher = NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if r := d.RunCheck(ctx, fetcher, filmziePage, stubEnvironment{matches: true}); r.Reason != "timeout" || r.Status != "unconfirmed" {
		t.Fatalf("deadline: %+v", r)
	}
}

func TestCheckSpecsAreValidatedWhenLoaded(t *testing.T) {
	base := "name: c-d\nprovider: P\nhosts: [h.example]\ncheck:\n  url: '^https://h\\.example/m/'\n  requests:\n    - url: \"{page}\"\n      response: html\n      rules:\n        - when: { page: { challenge: \"true\" } }\n          result: { detail: challenged }\n  otherwise: { detail: nothing }\n"
	if d, err := loadDriverText(t, base); err != nil || d.Check == nil || d.Check.TimeoutSeconds != 6 || d.Check.Requests[0].TimeoutSeconds != 4 || d.Check.Requests[0].MaxBytes != 512<<10 || d.Check.Requests[0].Redirects != "public" || d.Check.Otherwise.Status != "unconfirmed" || d.Check.Otherwise.Reason != "content_unconfirmed" {
		t.Fatalf("defaults: %+v %v", d, err)
	}
	for name, body := range map[string]string{
		"no sections":          "name: c-d\nprovider: P\nhosts: [h.example]\n",
		"bad url":              strings.Replace(base, `'^https://h\.example/m/'`, `'^https://h\.example/m/('`, 1),
		"unknown fact":         strings.Replace(base, "challenge", "mood", 1),
		"unknown reason":       strings.Replace(base, "result: { detail: challenged }", "result: { reason: bored, detail: challenged }", 1),
		"when and unless":      strings.Replace(base, "        - when: { page: { challenge: \"true\" } }\n", "        - when: { page: { challenge: \"true\" } }\n          unless: { status: '^2' }\n", 1),
		"empty condition":      strings.Replace(base, "when: { page: { challenge: \"true\" } }", "when: {}", 1),
		"bind without media":   strings.Replace(base, "result: { detail: challenged }", "result: { bind: { ssai: \"false\" } }", 1),
		"feeds without search": "name: c-d\nprovider: P\nhosts: [h.example]\nfeeds:\n  top: { params: { a: b } }\ncheck:\n  url: '^https://h\\.example/'\n  requests:\n    - url: \"{page}\"\n  otherwise: { detail: nothing }\n",
	} {
		if _, err := loadDriverText(t, body); err == nil {
			t.Errorf("%s: a broken check loaded", name)
		}
	}
}
