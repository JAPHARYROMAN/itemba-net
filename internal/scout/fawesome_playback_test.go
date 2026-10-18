package scout

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func fawesomeFixtureSource() CandidateSource {
	return CandidateSource{ID: "fawesome-abduction", Title: "Abduction", ReleaseYear: 2019, URL: "https://fawesome.tv/movies/10540876/abduction", AccessType: "unknown", ListingOnly: true}
}

func fawesomeFixtureInfo() fawesomeMovieInfo {
	drm := 0
	return fawesomeMovieInfo{NodeID: 10540876, Title: "Abduction", WebURL: "movies/10540876/abduction", ItemType: "movie", FeedType: "video", ActionType: "video", ReleaseDate: "07 June 2019 - 12:00 am", Runtime: 5835, DRM: &drm, Ad: "no", BlockItem: "false"}
}

func fawesomeEncodeFixture(raw string) string {
	data, key := []byte(raw), []byte("ft-iptv-website-#$#$")
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
	return base64.StdEncoding.EncodeToString(data)
}

func fawesomeFixturePage(info fawesomeMovieInfo, stream string) string {
	metadata, _ := json.Marshal(info)
	encoded, _ := json.Marshal(fawesomeEncodeFixture(stream))
	return `<html><h1>Abduction</h1><script type="application/ld+json">{"@type":"Movie","name":"Abduction","duration":"PT1H37M15S","datePublished":"2019-06-07T04:00:00.000Z","url":"` + fawesomeFixtureSource().URL + `"}</script>` +
		`<script type="application/ld+json">{"@type":"VideoObject","name":"Abduction","duration":"PT1H37M15S","uploadDate":"2019-06-07T04:00:00.000Z","embedUrl":"` + fawesomeFixtureSource().URL + `"}</script>` +
		`<script>var nodeId = "10540876"; var customdata = ""; var dvvideo_url = ` + string(encoded) + `; window.deeplinkMovieInfo = ` + string(metadata) + `;</script></html>`
}

func fawesomeFixtureClient(t *testing.T, body string, calls *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
		if req.URL.String() != fawesomeFixtureSource().URL || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("preflight fetched media/private API or forwarded account data")
		}
		response := searchFixtureResponse(200, "text/html", body)
		response.Request = req
		return response, nil
	})}
}

func TestFawesomePublicMainMovieContractKeepsSourcePrivatePendingNativeSample(t *testing.T) {
	media := "https://ftmain.cachefly.net/files/provider/full-movie.mp4?signature=fixture"
	calls := 0
	client := fawesomeFixtureClient(t, fawesomeFixturePage(fawesomeFixtureInfo(), media), &calls)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://fawesome.tv/")
	jar.SetCookies(origin, []*http.Cookie{{Name: "account", Value: "fixture"}})
	client.Jar = jar
	source, check := inspectFawesomePlayback(context.Background(), client, fawesomeFixtureSource())
	if check != nil || calls != 1 || source.providerStreamURL != media || !source.providerContentBound || source.providerSSAI || source.AccessType != "free" || source.AccessBasis != "fawesome_movie_metadata" || source.ReleaseYear != 2019 || source.Duration != "5835" {
		t.Fatalf("main guest contract did not bind exact feature: check=%+v calls=%d", check, calls)
	}
	encoded, _ := json.Marshal(source)
	if source.DirectStreamURL != "" || len(source.KnownManifests) != 0 || strings.Contains(string(encoded), "signature=fixture") {
		t.Fatal("private provider media leaked before native confirmation")
	}
	page, _ := url.Parse(source.URL)
	stream, _ := url.Parse(media)
	if !fawesomeResolvedPlaybackPair(source, page, stream, page) {
		t.Fatal("bound full feature pair rejected")
	}
	stream.Path = "/files/provider/trailer.mp4"
	if fawesomeResolvedPlaybackPair(source, page, stream, page) {
		t.Fatal("another media inherited main movie authorization")
	}
}

func TestFawesomePrefersDeclaredNodeBoundMainHLSWithAgreeingMP4Metadata(t *testing.T) {
	info, stream := fawesomeFixtureInfo(), "https://ftmain.cachefly.net/files/movie.mp4"
	info.VideoURL, info.VideoFLVURL = stream, stream
	info.VideoHLSURL = "https://ftmain.cachefly.net/files/a8wn4hw/vi/dd/52/10540876/s10540876.m3u8"
	calls := 0
	source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, fawesomeFixturePage(info, stream), &calls), fawesomeFixtureSource())
	if check != nil || source.providerStreamURL != info.VideoHLSURL || calls != 1 || !source.providerContentBound || source.providerSSAI || source.DirectStreamURL != "" || len(source.KnownManifests) != 0 {
		t.Fatalf("declared main HLS was not selected privately: %+v", check)
	}
	page, _ := url.Parse(source.URL)
	media, _ := url.Parse(info.VideoHLSURL)
	if !fawesomeResolvedPlaybackPair(source, page, media, page) {
		t.Fatal("exact node-bound main HLS was rejected")
	}
	source.ListingOnly = false
	if !eligibleFreePlayback(VerifiedSource{CandidateSource: source, Verified: true, VerificationStatus: "media_reachable"}) {
		t.Fatal("bound HLS cannot enter protected native sampling")
	}
	encoded, _ := json.Marshal(source)
	if strings.Contains(string(encoded), "cachefly.net") {
		t.Fatal("declared HLS media escaped public JSON")
	}
	media.Path = strings.Replace(media.Path, "/dd/", "/ff/", 1)
	if fawesomeResolvedPlaybackPair(source, page, media, page) {
		t.Fatal("changed HLS URL inherited main asset binding")
	}
}

func TestFawesomeUnsafeDeclaredHLSNeverFallsBackToMP4(t *testing.T) {
	valid := "https://ftmain.cachefly.net/files/a8wn4hw/vi/dd/52/10540876/s10540876.m3u8"
	for _, hls := range []string{
		strings.Replace(valid, "/10540876/", "/10540877/", 1),
		strings.Replace(valid, "s10540876.", "s10540877.", 1),
		strings.Replace(valid, "https:", "http:", 1),
		strings.Replace(valid, "ftmain.cachefly.net", "ftmain.cachefly.net.evil.example", 1),
		strings.Replace(valid, "ftmain.cachefly.net", "127.0.0.1", 1),
		strings.Replace(valid, "ftmain.cachefly.net", "ftmain.cachefly.net:8443", 1),
		valid + "?token=unobserved-signed-contract",
		valid + "#fragment",
		strings.Replace(valid, "/10540876/", "/%31%30%35%34%30%38%37%36/", 1),
	} {
		t.Run(hls, func(t *testing.T) {
			info := fawesomeFixtureInfo()
			info.VideoHLSURL = hls
			calls := 0
			source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, fawesomeFixturePage(info, "https://ftmain.cachefly.net/files/main.mp4"), &calls), fawesomeFixtureSource())
			if check == nil || check.PlaybackConfirmed || source.providerContentBound || source.providerStreamURL != "" || calls != 1 {
				t.Fatal("invalid declared HLS fell back to an MP4 or gained movie binding")
			}
		})
	}
}

func TestFawesomePreferredHLSStillRequiresMainFileAgreementDRMAndAdClassification(t *testing.T) {
	for name, mutate := range map[string]func(*fawesomeMovieInfo){
		"main file mismatch": func(i *fawesomeMovieInfo) { i.VideoURL = "https://ftmain.cachefly.net/files/other.mp4" },
		"DRM":                func(i *fawesomeMovieInfo) { value := 1; i.DRM = &value },
		"SSAI":               func(i *fawesomeMovieInfo) { i.Ad = "ssai" },
		"guest blocked":      func(i *fawesomeMovieInfo) { i.BlockItem = "true" },
	} {
		t.Run(name, func(t *testing.T) {
			info := fawesomeFixtureInfo()
			info.VideoHLSURL = "https://ftmain.cachefly.net/files/a8wn4hw/vi/dd/52/10540876/s10540876.m3u8"
			mutate(&info)
			candidate := fawesomeFixtureSource()
			candidate.providerContentBound = true
			candidate.providerStreamURL = "https://ftmain.cachefly.net/files/stale-main.mp4"
			calls := 0
			source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, fawesomeFixturePage(info, "https://ftmain.cachefly.net/files/main.mp4"), &calls), candidate)
			if check == nil || check.PlaybackConfirmed || source.providerContentBound || source.providerStreamURL != "" || calls != 1 {
				t.Fatal("HLS reused stale binding or bypassed required main movie checks")
			}
		})
	}
}

func TestFawesomeCatalogDateDoesNotReplaceOrInventOriginalFilmYear(t *testing.T) {
	// This provider page reports a 2026 catalog date for a film whose
	// distributor already offered it in 2020. All three provider dates agree,
	// but none of those dates establishes the original movie's year.
	for _, originalYear := range []int{2020, 0} {
		source := CandidateSource{Title: "The Knight Of Shadows", ReleaseYear: originalYear, URL: "https://fawesome.tv/movies/10772898/the-knight-of-shadows", AccessType: "free", ListingOnly: true}
		info := fawesomeFixtureInfo()
		info.Title, info.NodeID, info.WebURL = source.Title, 10772898, "movies/10772898/the-knight-of-shadows"
		info.ReleaseDate, info.Runtime = "30 March 2026 - 02:25 am", 6531
		body := fawesomeFixturePage(info, "https://ftmain.cachefly.net/files/movie.mp4")
		body = strings.NewReplacer("Abduction", source.Title, "10540876/abduction", "10772898/the-knight-of-shadows", `"10540876"`, `"10772898"`, "PT1H37M15S", "PT1H48M51S", "2019-06-07T04:00:00.000Z", "2026-03-30T06:25:00.000Z").Replace(body)
		calls := 0
		client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.String() != source.URL {
				t.Fatal("catalog-date validation requested another movie or media")
			}
			response := searchFixtureResponse(http.StatusOK, "text/html", body)
			response.Request = req
			return response, nil
		})}
		resolved, check := inspectFawesomePlayback(context.Background(), client, source)
		if check != nil || calls != 1 || !resolved.providerContentBound || resolved.ReleaseYear != originalYear {
			t.Fatalf("catalog year replaced the original year or rejected valid full movie binding: requested=%d returned=%d check=%+v", originalYear, resolved.ReleaseYear, check)
		}
		if strings.Contains(resolved.AccessNote, "original release year is unconfirmed") != (originalYear == 0) {
			t.Fatal("unknown original film year was not distinguished from an independently supplied year")
		}
	}
}

func TestFawesomeBoundMainFileSurvivesWatchPreparationButCannotBecomePaidPlayback(t *testing.T) {
	calls := 0
	stream := "https://ftmain.cachefly.net/files/movie.mp4?signature=fixture"
	source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, fawesomeFixturePage(fawesomeFixtureInfo(), stream), &calls), fawesomeFixtureSource())
	if check != nil {
		t.Fatalf("fixture main file rejected: %+v", check)
	}
	source.ListingOnly = false
	prepared := prepareMovieWatchOptions("Abduction 2019", "ALL", []CandidateSource{source})
	if len(prepared) != 1 || prepared[0].AccessBasis != "fawesome_movie_metadata" || !prepared[0].providerContentBound {
		t.Fatalf("watch preparation lost private main binding: %+v", prepared)
	}
	verified := VerifiedSource{CandidateSource: prepared[0], Verified: true, VerificationStatus: "media_reachable"}
	if !eligibleFreePlayback(verified) {
		t.Fatal("matching prepared full movie is ineligible for its native sample")
	}
	for _, mutate := range []func(*VerifiedSource){
		func(s *VerifiedSource) { s.AccessType = "rent" },
		func(s *VerifiedSource) { s.providerSSAI = true },
		func(s *VerifiedSource) { s.AccessEvidenceURL = "https://fawesome.tv/movies/10540877/other-movie" },
		func(s *VerifiedSource) { s.providerStreamURL = "https://127.0.0.1/files/movie.mp4" },
		func(s *VerifiedSource) { s.providerContentBound = false },
	} {
		copy := verified
		mutate(&copy)
		if eligibleFreePlayback(copy) {
			t.Fatal("paid, SSAI, unbound or private movie inherited native eligibility")
		}
	}
}

func TestFawesomeRejectsWrongTitleCatalogDateNodeTrailerDRMAndAmbiguity(t *testing.T) {
	for name, mutate := range map[string]func(*fawesomeMovieInfo){
		"wrong title":                         func(i *fawesomeMovieInfo) { i.Title = "Other movie" },
		"catalog date conflicts with schemas": func(i *fawesomeMovieInfo) { i.ReleaseDate = "07 June 2020 - 12:00 am" },
		"wrong node":                          func(i *fawesomeMovieInfo) { i.NodeID++ },
		"wrong route":                         func(i *fawesomeMovieInfo) { i.WebURL = "movies/10540877/other-movie" },
		"short trailer":                       func(i *fawesomeMovieInfo) { i.Runtime = 98 },
		"episode":                             func(i *fawesomeMovieInfo) { i.ItemType = "episode" },
		"DRM":                                 func(i *fawesomeMovieInfo) { value := 1; i.DRM = &value },
		"missing DRM declaration":             func(i *fawesomeMovieInfo) { i.DRM = nil },
		"unknown ad classification":           func(i *fawesomeMovieInfo) { i.Ad = "" },
		"server inserted advertising":         func(i *fawesomeMovieInfo) { i.Ad = "ssai" },
		"blocked":                             func(i *fawesomeMovieInfo) { i.BlockItem = "true" },
		"ambiguous main":                      func(i *fawesomeMovieInfo) { i.VideoURL = "https://ftmain.cachefly.net/files/other.mp4" },
	} {
		t.Run(name, func(t *testing.T) {
			info := fawesomeFixtureInfo()
			mutate(&info)
			calls := 0
			body := fawesomeFixturePage(info, "https://ftmain.cachefly.net/files/movie.mp4")
			source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, body, &calls), fawesomeFixtureSource())
			if check == nil || check.PlaybackConfirmed || source.providerStreamURL != "" || calls != 1 {
				t.Fatalf("unattributed movie was readied: %+v", check)
			}
		})
	}
	for name, mutate := range map[string]func(string) string{
		"schema runtime": func(s string) string { return strings.Replace(s, "PT1H37M15S", "PT1M38S", 1) },
		"schema catalog day": func(s string) string {
			return strings.ReplaceAll(s, "2019-06-07T04:00:00.000Z", "2019-06-08T04:00:00.000Z")
		},
		"schema catalog instant": func(s string) string {
			return strings.Replace(s, "2019-06-07T04:00:00.000Z", "2019-06-07T05:00:00.000Z", 1)
		},
		"schema page": func(s string) string {
			return strings.Replace(s, `"embedUrl":"`+fawesomeFixtureSource().URL, `"embedUrl":"https://fawesome.tv/movies/10540877/other`, 1)
		},
		"duplicate binding": func(s string) string { return s + `<script>var dvvideo_url = "ignored";</script>` },
		"split binding": func(s string) string {
			return strings.Replace(s, "window.deeplinkMovieInfo", "</script><script>window.deeplinkMovieInfo", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			body := mutate(fawesomeFixturePage(fawesomeFixtureInfo(), "https://ftmain.cachefly.net/files/movie.mp4"))
			_, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, body, &calls), fawesomeFixtureSource())
			if check == nil || check.PlaybackConfirmed {
				t.Fatal("inconsistent or ambiguous metadata authorized media")
			}
		})
	}
}

func TestFawesomeRegionBlockHasNoMediaFallback(t *testing.T) {
	calls := 0
	body := `<h2>This content is not available in your region, explore similar options</h2>` + fawesomeFixturePage(fawesomeFixtureInfo(), "https://ftmain.cachefly.net/files/movie.mp4")
	source, check := inspectFawesomePlayback(context.Background(), fawesomeFixtureClient(t, body, &calls), fawesomeFixtureSource())
	if check == nil || check.Reason != "region_unavailable" || check.Status != "not_started" || check.PlaybackConfirmed || source.providerStreamURL != "" || calls != 1 {
		t.Fatalf("country restriction ignored: %+v", check)
	}
}

func TestFawesomeDecoderRejectsArbitrarySSRFAndUnsupportedMedia(t *testing.T) {
	for _, raw := range []string{"http://ftmain.cachefly.net/files/movie.mp4", "https://127.0.0.1/files/movie.mp4", "https://ftmain.cachefly.net.evil.example/files/movie.mp4", "https://user:password@ftmain.cachefly.net/files/movie.mp4", "https://ftmain.cachefly.net/files/movie.mpd", "https://ftmain.cachefly.net/files/traverse/../movie.mp4", "https://ftmain.cachefly.net:8443/files/movie.mp4", "https://ftmain.cachefly.net/files/movie.mp4#external"} {
		if _, ok := decodeFawesomePublicMovieURL(fawesomeEncodeFixture(raw)); ok {
			t.Fatalf("unsafe/unsupported public main URL accepted: %s", raw)
		}
	}
	if _, ok := decodeFawesomePublicMovieURL("not-base64"); ok {
		t.Fatal("malformed obfuscation accepted")
	}
	for _, source := range []CandidateSource{
		{URL: fawesomeFixtureSource().URL, AccessType: "subscription"},
		{URL: fawesomeFixtureSource().URL, AccessType: "rent"},
		{URL: fawesomeFixtureSource().URL, AccessType: "free", Price: "$3"},
		{URL: "https://fawesome.tv/movies/10540876/abduction?country=US"},
		{URL: "https://fawesome.tv/search/Abduction"},
		{URL: "https://fawesome.tv.evil.example/movies/10540876/abduction"},
	} {
		if fawesomeMovieTarget(source) {
			t.Fatal("paid, discovery or noncanonical provider page targeted")
		}
	}
}

func TestLiveFawesomeProtectedFeaturePlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FAWESOME_PLAYBACK") != "1" {
		t.Skip("set ITEMBA_LIVE_FAWESOME_PLAYBACK=1 for one bounded protected public feature sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	transport := client.Transport
	var mu sync.Mutex
	var ranges []string
	client.Transport = searchFixtureTransport(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if request.Header.Get("Range") != "" && response != nil {
			mu.Lock()
			ranges = append(ranges, request.Header.Get("Range")+" -> "+response.Header.Get("Content-Range"))
			mu.Unlock()
		}
		return response, err
	})
	candidate := fawesomeFixtureSource()
	if os.Getenv("ITEMBA_LIVE_FAWESOME_RECENT") == "1" {
		candidate = CandidateSource{ID: "fawesome-secret-wife", Title: "The Secret Wife", ReleaseYear: 2024, URL: "https://fawesome.tv/movies/10650115/the-secret-wife", AccessType: "unknown", ListingOnly: true}
	}
	source, preflight := inspectFawesomePlayback(ctx, client, candidate)
	if preflight != nil {
		safe, _ := json.Marshal(preflight)
		t.Fatalf("public main feature unavailable: %s", safe)
	}
	source.ListingOnly = false
	s := &Scout{availabilityHTTPClient: client}
	country, _ := s.availabilityCheckCountry(ctx)
	verified := VerifiedSource{CandidateSource: source, Verified: true, VerificationStatus: "media_reachable"}
	check := s.probeFreeMoviePlayback(ctx, verified, "ALL", country)
	proof := struct {
		Movie         string         `json:"movie"`
		Year          int            `json:"year"`
		ListedRuntime string         `json:"listedRuntimeSeconds"`
		ShieldEnabled bool           `json:"shieldEnabled"`
		Check         *PlaybackCheck `json:"check"`
	}{source.Title, source.ReleaseYear, source.Duration, true, check}
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	t.Logf("Fawesome full feature protected native sample: %s", encoded)
	mu.Lock()
	t.Logf("Bounded native upstream ranges (no media URLs): %v", ranges)
	mu.Unlock()
	if target := os.Getenv("ITEMBA_FAWESOME_PROOF_PATH"); target != "" {
		if err := os.WriteFile(target, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if check == nil || !check.PlaybackConfirmed || check.DurationSeconds < 40*60 || check.DecodedFrames < 2 || check.SecondsAdvanced < 1 {
		t.Error("full feature produced no protected native decoded progress")
	}
	time.Sleep(600 * time.Millisecond)
}
