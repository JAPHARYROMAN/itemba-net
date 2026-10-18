package scout

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

type searchFixtureTransport func(*http.Request) (*http.Response, error)

func (f searchFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := f(req)
	if response != nil && response.Request == nil {
		response.Request = req
	}
	return response, err
}

func searchFixtureResponse(code int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReferenceCatalogDoesNotSubstituteUnrelatedMovies(t *testing.T) {
	for _, query := range []string{"", "Interstellar", "Rare Documentary About Cats", "ordinary generic keywords"} {
		if got := matchReferenceCatalog(query); len(got) != 0 {
			t.Errorf("search %q received unrelated reference films: %+v", query, got)
		}
	}
	got := matchReferenceCatalog("Tears of Steel")
	if len(got) != 1 || got[0].ID != "ref-tears-of-steel" {
		t.Fatalf("matching reference movie was lost: %+v", got)
	}
}

func TestRelevanceUsesWholeTitleTokensAndReleaseYear(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		candidate CandidateSource
		want      int
	}{
		{"exact title and year", "Interstellar (2014)", CandidateSource{Title: "Interstellar", ReleaseYear: 2014}, 100},
		{"wrong release", "Interstellar (2014)", CandidateSource{Title: "Interstellar", ReleaseYear: 2024}, 0},
		{"unknown year is weaker", "Interstellar (2014)", CandidateSource{Title: "Interstellar"}, 85},
		{"whole tokens", "Star Wars", CandidateSource{Title: "Starship Wars"}, 0},
		{"punctuation normalized", "Spider-Man", CandidateSource{Title: "Spider Man"}, 100},
		{"single title does not include sequels", "Dune", CandidateSource{Title: "Dune Part Two"}, 0},
		{"numeric film title", "1917", CandidateSource{Title: "1917", ReleaseYear: 2019}, 100},
		{"numeric title with explicit year", "2001: A Space Odyssey (1968)", CandidateSource{Title: "2001: A Space Odyssey", ReleaseYear: 1968}, 100},
		{"release year in numeric title label", "2001: A Space Odyssey (1968)", CandidateSource{Title: "2001: A Space Odyssey (1968)"}, 100},
		{"known year overrides matching title label", "Interstellar (2014)", CandidateSource{Title: "Interstellar 2014", ReleaseYear: 2024}, 0},
		{"common search wording before year", "Watch Interstellar full movie online (2014)", CandidateSource{Title: "Interstellar", ReleaseYear: 2014}, 100},
		{"common search wording after year", "Watch Interstellar (2014) full movie online", CandidateSource{Title: "Interstellar", ReleaseYear: 2014}, 100},
		{"store creator credit preserves exact film", "Night of the Living Dead 1968", CandidateSource{Title: "George A. Romero's Night of the Living Dead (1968) (In Color & Restored)", ReleaseYear: 1968, SourceType: "Movie Store", Provider: "Apple TV / iTunes"}, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := relevanceScore(tc.query, tc.candidate); got != tc.want {
				t.Errorf("relevance %d, want %d for %q → %q", got, tc.want, tc.query, tc.candidate.Title)
			}
		})
	}
}

func TestInterstellarSearchRejectsLiveArchiveFalseMatches(t *testing.T) {
	for _, title := range []string{
		"Hans Zimmer Interstellar",
		"Hidden Meaning in Interstellar – EarthlingCinema",
		"Interstellar Ella TVOKids UpNext Screenbug",
		"Interstellar Intermission...",
		"Opening/Closing To InterstellarUK Blu Ray",
		"Four Things To Know About The Interstellar Mysteries Of Prey",
	} {
		t.Run(title, func(t *testing.T) {
			candidate := CandidateSource{Title: title, AccessType: "free", URL: "https://archive.org/details/unrelated"}
			if score := relevanceScore("Interstellar (2014)", candidate); score != 0 {
				t.Fatalf("unrelated Archive video received score %d", score)
			}
			if got := rankCandidates("Interstellar (2014)", []CandidateSource{candidate}, false); len(got) != 0 {
				t.Fatalf("unrelated Archive video survived source ranking: %+v", got)
			}
		})
	}
}

func TestCanonicalTitlesAllowSubtitlesAndConservativeKeywordVariants(t *testing.T) {
	for _, tc := range []struct {
		query, title string
		want         int
	}{
		{"Interstellar", "Interstellar (2014) [1080p]", 100},
		{"Nosferatu", "Nosferatu: A Symphony of Horror (1922)", 95},
		{"The Score", "The Score (2001)", 100},
		{"Night Living Dead", "Night of the Living Dead (1968)", 100},
		{"Night Living Dead", "Night of the Living Dead Restored (1968)", 80},
		{"Night Living Dead", "A Night of All the Living Dead Movies Explained", 0},
		{"Interstellar", "Interstellar: Behind the Scenes", 0},
		{"Interstellar", "Interstellar: Hidden Meaning", 0},
		{"Interstellar score", "Interstellar Score", 100},
		{"Interstellar review", "Interstellar Review", 100},
	} {
		t.Run(tc.query+"/"+tc.title, func(t *testing.T) {
			if score := relevanceScore(tc.query, CandidateSource{Title: tc.title}); score != tc.want {
				t.Errorf("relevance %d, want %d", score, tc.want)
			}
		})
	}
}

func TestMovieSearchFiltersTrailersAndReviewsUnlessRequested(t *testing.T) {
	for _, title := range []string{
		"Interstellar Official Trailer",
		"Interstellar (Official Trailer)",
		"Interstellar (2014) Review",
		"Interstellar - Reaction",
		"Interstellar Explained",
		"Interstellar Clip",
		"Interstellar Soundtrack",
		"Interstellar Score",
		"Interstellar News",
		"Interstellar Opening Closing",
		"Interstellar Intermission",
		"Interstellar Behind Scenes",
	} {
		if got := relevanceScore("Interstellar", CandidateSource{Title: title}); got != 0 {
			t.Errorf("movie search retained promotional/commentary title %q with score %d", title, got)
		}
	}
	if score := relevanceScore("Interstellar trailer", CandidateSource{Title: "Interstellar Trailer"}); score != 100 {
		t.Errorf("explicit trailer search was filtered: %d", score)
	}
}

func TestRankCandidatesDeduplicatesSourcesAndPreservesRegions(t *testing.T) {
	got := rankCandidates("Fixture Original Film", []CandidateSource{
		{Title: "Unrelated", URL: "https://source.example/movie"},
		{Title: "Fixture Original Film", URL: "https://source.example/movie"},
		{Title: "Fixture Original Film", URL: "https://source.example/movie/"},
		{Title: "Fixture Original Film", URL: "https://source.example/movie", Region: "US"},
		{Title: "Fixture Original Film", URL: "https://source.example/movie", Region: "KE"},
		{Title: "Fixture Original Film Restored", URL: "https://source.example/restored"},
		{Title: "Fixture Original Film", URL: "file:///tmp/movie"},
		{Title: "Fixture Original Film", URL: "/relative/movie"},
	}, false)
	if len(got) != 4 {
		t.Fatalf("incorrect source deduplication: %+v", got)
	}
	regions := map[string]bool{}
	for _, candidate := range got {
		if candidate.AccessType != "unknown" {
			t.Errorf("unpriced source inferred free/paid access: %+v", candidate)
		}
		if candidate.URL != "https://source.example/restored" {
			regions[candidate.Region] = true
		}
	}
	if !regions[""] || !regions["US"] || !regions["KE"] || got[len(got)-1].URL != "https://source.example/restored" {
		t.Errorf("regional offers or relevance ordering lost: %+v", got)
	}
}

func TestFeatureRuntimeRejectsExactNameClipsWithoutFilteringShortFilms(t *testing.T) {
	for _, trustedFeature := range []CandidateSource{
		{ID: "ref-feature", Title: "Night of the Living Dead (1968)", URL: "https://reference.example/full", Duration: "1:35:52"},
		{Title: "Night of the Living Dead", ReleaseYear: 1968, URL: "https://itunes.apple.com/us/movie/feature/id1", SourceType: "Movie Store", Provider: "Apple TV / iTunes", Duration: "1h 35m", AccessType: "buy", ListingOnly: true},
	} {
		got := rankCandidates("Night of the Living Dead (1968)", []CandidateSource{
			trustedFeature,
			{Title: "Night Of The Living Dead", URL: "https://archive.org/details/bronxnet-clip", Duration: "113.09", ReleaseYear: 1968},
			{Title: "Night Of The Living Dead", URL: "https://archive.org/details/full-feature", Duration: "01:35:00", ReleaseYear: 1968},
			{Title: "Night Of The Living Dead", URL: "https://archive.org/details/unknown-runtime", ReleaseYear: 1968},
		}, false)
		if len(got) != 3 {
			t.Fatalf("feature source filtering lost valid/unknown editions: %+v", got)
		}
		for _, source := range got {
			if strings.Contains(source.URL, "bronxnet-clip") {
				t.Fatalf("two-minute clip was retained as a 95-minute feature: %+v", source)
			}
		}
	}
	shorts := rankCandidates("Sintel", []CandidateSource{
		{ID: "ref-sintel", Title: "Sintel", URL: "https://reference.example/sintel", Duration: "14:48"},
		{Title: "Sintel", URL: "https://archive.org/details/sintel", Duration: "888"},
		{Title: "Sintel", URL: "https://archive.org/details/sintel-reasonable-cut", Duration: "13:01"},
		{Title: "Sintel", URL: "https://archive.org/details/sintel-unknown-runtime"},
		{Title: "Sintel", URL: "https://archive.org/details/sintel-dvd-menu", Duration: "107.24"},
	}, false)
	if len(shorts) != 4 {
		t.Fatalf("short-film runtime should remove the DVD menu while retaining full/unknown editions: %+v", shorts)
	}
	for _, source := range shorts {
		if strings.Contains(source.URL, "dvd-menu") {
			t.Fatalf("DVD menu was retained as a full short film: %+v", source)
		}
	}
	noTrustedRuntime := rankCandidates("Independent Short", []CandidateSource{
		{Title: "Independent Short", URL: "https://archive.org/details/short", Duration: "113.09"},
	}, false)
	if len(noTrustedRuntime) != 1 {
		t.Fatal("short source was discarded without a known feature runtime")
	}
	for name, reference := range map[string]CandidateSource{
		"store shorts do not establish expected runtime":           {Title: "Independent Short", SourceType: "Movie Store", Provider: "Apple TV / iTunes", Duration: "14:48", URL: "https://itunes.apple.com/us/movie/short/id1"},
		"very short reference does not establish expected runtime": {ID: "ref-tiny", Title: "Independent Short", Duration: "02:30", URL: "https://reference.example/tiny"},
	} {
		t.Run(name, func(t *testing.T) {
			got := rankCandidates("Independent Short", []CandidateSource{reference, {Title: "Independent Short", URL: "https://archive.org/details/independent-short", Duration: "45"}}, false)
			if len(got) != 2 {
				t.Fatalf("unestablished runtime discarded an independent short: %+v", got)
			}
		})
	}
}

func TestMediaProbeChecksResponseRatherThanFileExtensionOrMIME(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		contentType string
		body        string
		verified    bool
	}{
		{"missing.mp4", 404, "video/mp4", "\x00\x00\x00\x18ftypisom", false},
		{"html.mp4", 200, "video/mp4", "<!DOCTYPE html><html><body>Sign in</body></html>", false},
		{"comment-prefixed-html.mp4", 200, "video/mp4", "<!-- CDN error -->\n<html><body>Denied</body></html>", false},
		{"text-denial.mp4", 200, "video/mp4", "Access denied", false},
		{"invalid-playlist.m3u8", 200, "application/vnd.apple.mpegurl", "Access denied", false},
		{"empty.mp4", 200, "video/mp4", "", false},
		{"range-playlist.m3u8", 206, "application/octet-stream", "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\nsegment.ts\n", true},
		{"valid.mp4", 206, "application/octet-stream", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom", true},
		{"valid.webm", 200, "application/octet-stream", "\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01", true},
		{"valid.mpd", 206, "application/octet-stream", `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period/></MPD>`, true},
		{"html.mpd", 200, "application/dash+xml", `<?xml version="1.0"?><html><body>Payment required</body></html>`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=0-1023" {
					t.Errorf("media check did not request a bounded range: %q", r.Header.Get("Range"))
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			target := server.URL + "/" + tc.name
			result := verifyFastHTTPWithClient(context.Background(), shield.New(), CandidateSource{URL: target, DirectStreamURL: target}, server.Client())
			if result.Verified != tc.verified {
				t.Fatalf("media verification=%v, want %v: %+v", result.Verified, tc.verified, result)
			}
			if tc.verified {
				if result.VerificationStatus != "media_reachable" || result.CheckedAt == "" {
					t.Errorf("successful media check missing truthful status/time: %+v", result)
				}
			} else if result.VerificationStatus != "unavailable" || result.PrunedReason == "" {
				t.Errorf("failed media check was hidden: %+v", result)
			}
		})
	}
}

func TestPlayerMarkupDoesNotClaimVerifiedPlayback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><video src="/missing.mp4"></video></html>`)
	}))
	defer server.Close()
	result := verifyFastHTTPWithClient(context.Background(), shield.New(), CandidateSource{URL: server.URL}, server.Client())
	if result.Verified || result.VerificationStatus != "page_reachable" || !result.HasVideoDOM {
		t.Fatalf("player markup was presented as confirmed playback: %+v", result)
	}
}

func TestSearchWithRegionPreservesFreeMediaAndUnverifiedPaidListings(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	var requests, paidPageRequests, mediaRequests atomic.Int32
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		switch {
		case req.URL.Hostname() == "archive.org" && req.URL.Path == "/advancedsearch.php":
			return searchFixtureResponse(200, "application/json", `{"response":{"docs":[{"identifier":"fixture-film","title":"Fixture Film (2014)"}]}}`), nil
		case req.URL.Hostname() == "archive.org" && req.URL.Path == "/metadata/fixture-film":
			return searchFixtureResponse(200, "application/json", `{"metadata":{"identifier":"fixture-film","title":"Fixture Film","mediatype":"movies","year":"2014","runtime":"600"},"files":[{"name":"fixture-film.mp4","format":"MPEG4","length":"600"}]}`), nil
		case req.URL.Hostname() == "archive.org" && strings.HasPrefix(req.URL.Path, "/download/"):
			mediaRequests.Add(1)
			if req.Header.Get("Range") != "bytes=0-1023" {
				t.Errorf("free media did not receive an actual bounded probe")
			}
			return searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom"), nil
		case req.URL.Hostname() == "sepiasearch.org":
			return searchFixtureResponse(200, "application/json", `{"data":[]}`), nil
		case req.URL.Hostname() == "commons.wikimedia.org":
			return searchFixtureResponse(200, "application/json", `{"query":{"pages":{}}}`), nil
		case req.URL.Hostname() == "itunes.apple.com" && req.URL.Path == "/search":
			country := strings.ToLower(req.URL.Query().Get("country"))
			body := fmt.Sprintf(`{"results":[{"kind":"feature-movie","trackId":7,"trackName":"Fixture Film","trackViewUrl":"https://itunes.apple.com/%s/movie/fixture-film/id7","releaseDate":"2014-11-07T08:00:00Z","currency":"USD","trackPrice":9.99}]}`, country)
			return searchFixtureResponse(200, "application/json", body), nil
		case req.URL.Hostname() == "itunes.apple.com" && strings.Contains(req.URL.Path, "/rss/topmovies/"):
			return searchFixtureResponse(200, "application/json", `{"feed":{"entry":[]}}`), nil
		case req.URL.Hostname() == "itunes.apple.com":
			paidPageRequests.Add(1)
			return searchFixtureResponse(200, "text/html", `<div data-fake-play-wall="true">Account and payment required</div>`), nil
		default:
			return nil, fmt.Errorf("unexpected fixture request: %s", req.URL)
		}
	})}
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.httpClient = client
	s.availabilityHTTPClient = client
	s.playbackProbe = func(_ context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		return skippedPlayback(region, country, "Fixture does not contain a decodable movie.")
	}
	report := s.SearchWithRegion(context.Background(), "Fixture Film (2014)", "us")
	if report.Cached || report.Region != "US" || report.ListedCount != 2 || report.VerifiedCount != 1 || report.PrunedCount != 0 {
		t.Fatalf("unexpected first search: %+v", report)
	}
	for _, source := range report.Sources {
		switch source.AccessType {
		case "free":
			if !source.Verified || source.VerificationStatus != "media_reachable" {
				t.Errorf("free source was not checked: %+v", source)
			}
		case "buy", "paid":
			if source.Verified || !source.ListingOnly || source.VerificationStatus != "listing" || source.Region != "US" || source.Price != "Buy USD 9.99" {
				t.Errorf("paid store listing misrepresented access: %+v", source)
			}
		default:
			t.Errorf("unexpected access label: %+v", source)
		}
	}
	if mediaRequests.Load() != 1 || paidPageRequests.Load() != 1 {
		t.Fatalf("automatic movie checks did not probe each source: media=%d paid pages=%d", mediaRequests.Load(), paidPageRequests.Load())
	}

	requestCount := requests.Load()
	cached := s.SearchWithRegion(context.Background(), "fixture film (2014)", " US ")
	if !cached.Cached || requests.Load() != requestCount || cached.Query != "fixture film (2014)" || len(cached.Sources) != 2 {
		t.Fatalf("equivalent query/region did not reuse search: cached=%v requests=%d→%d", cached.Cached, requestCount, requests.Load())
	}
	otherRegion := s.SearchWithRegion(context.Background(), "Fixture Film (2014)", "KE")
	if otherRegion.Cached || otherRegion.Region != "KE" || requests.Load() <= requestCount || otherRegion.ListedCount != 2 {
		t.Fatalf("regional search reused another region's cache: %+v", otherRegion)
	}
	for _, source := range otherRegion.Sources {
		if source.ListingOnly && source.Region != "KE" {
			t.Errorf("wrong-region paid listing survived cache separation: %+v", source)
		}
	}

	s.cacheMu.Lock()
	entry := s.cache["fixture film (2014)|US"]
	entry.at = time.Now().Add(-6 * time.Minute)
	s.cache["fixture film (2014)|US"] = entry
	s.cacheMu.Unlock()
	requestCount = requests.Load()
	refreshed := s.SearchWithRegion(context.Background(), "Fixture Film (2014)", "US")
	if refreshed.Cached || requests.Load() <= requestCount {
		t.Fatal("expired search cache was reused without checking providers")
	}
}

func TestSearchWithoutMatchesDoesNotInventFallbackResults(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.httpClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		return searchFixtureResponse(200, "application/json", `{"response":{"docs":[]},"data":[],"query":{"pages":{}},"results":[]}`), nil
	})}
	s.availabilityHTTPClient = s.httpClient
	report := s.SearchWithRegion(context.Background(), "A Completely Unmatched Fixture Film", "US")
	if report.TotalCandidates != 0 || report.ListedCount != 0 || len(report.Sources) != 0 || len(report.VerifiedSources) != 0 || len(report.PrunedSources) != 0 {
		t.Fatalf("empty providers produced an unrelated movie: %+v", report)
	}
	if len(report.Providers) == 0 {
		t.Fatal("empty search hid provider coverage/status")
	}
}
