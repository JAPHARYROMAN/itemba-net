package scout

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func watchOptionFixture(raw string) CandidateSource {
	return CandidateSource{Title: "Fixture Film", URL: raw, SourceType: "Web Listing", AccessType: "unknown", Region: "Unknown", ListingOnly: true}
}

func TestWatchOptionsRequireIndividualMovieOrPlayerDestinations(t *testing.T) {
	for _, raw := range []string{
		"https://www.netflix.com/title/123", "https://www.netflix.com/tz-en/watch/123",
		"https://www.primevideo.com/detail/123", "https://www.primevideo.com/region/eu/detail/123",
		"https://www.amazon.com/gp/video/detail/123", "https://www.amazon.co.uk/gp/video/aw/d/123",
		"https://tv.apple.com/us/movie/fixture-film/123", "https://itunes.apple.com/tz/movie/fixture-film/id1",
		"https://www.youtube.com/watch?v=123", "https://www.youtube.com/embed/123", "https://www.youtube.com/live/123", "https://youtu.be/123",
		"https://archive.org/details/fixture-film", "https://archive.org/embed/fixture-film",
		"https://tubitv.com/en/movies/123/fixture-film", "https://mubi.com/en/films/fixture-film",
		"https://www.dstv.com/en-tz/watch/movies/fixture-film",
		"https://source.example/watch/fixture-film", "https://source.example/movies/fixture-film",
		"https://source.example/video/123", "https://source.example/player/123", "https://peertube.example/videos/embed/123",
	} {
		t.Run(raw, func(t *testing.T) {
			got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{watchOptionFixture(raw)})
			if len(got) != 1 || got[0].DestinationType != "watch" || got[0].URL != raw {
				t.Fatalf("individual destination lost or rewritten: %+v", got)
			}
		})
	}
	for _, raw := range []string{
		"https://www.netflix.com/", "https://www.netflix.com/browse", "https://www.netflix.com/title",
		"https://www.primevideo.com/storefront", "https://www.amazon.com/dp/DVD123", "https://www.amazon.com/gp/video/storefront",
		"https://tv.apple.com/us/search", "https://youtube.com/results?search_query=fixture", "https://youtube.com/watch",
		"https://archive.org/search?query=fixture", "https://archive.org/details", "https://mubi.com/en/films", "https://tubitv.com/movies",
		"https://web.azamtvmax.com/Movies", "https://source.example/", "https://source.example/movies", "https://source.example/search/movie/fixture",
		"https://source.example/shop/movies/fixture", "https://source.example/movie/fixture/tickets", "https://watch.plex.tv/movie/fixture",
		"https://www.amctheatres.com/movies/fixture-film-123",
		"https://www.showmax.com/movie/fixture", "https://www.dstv.com/en-tz/watch/showmax/",
		"file:///tmp/movie", "https://user:password@source.example/watch/fixture",
	} {
		t.Run("exclude/"+raw, func(t *testing.T) {
			if got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{watchOptionFixture(raw)}); len(got) != 0 {
				t.Fatalf("generic, guide, merchandise, or obsolete destination retained: %+v", got)
			}
		})
	}
}

func TestCurrentCommercialMovieRoutesKeepServiceModelsSeparateFromOffers(t *testing.T) {
	for _, tc := range []struct{ raw, access string }{
		{"https://www.disneyplus.com/browse/entity-f51dce23-7d8f-490a-9f0c-be1b5432c2a9", "subscription"},
		{"https://www.disneyplus.com/en-gb/movies/fixture-film/6PU7Aod2NS2P", "subscription"},
		{"https://www.hulu.com/movie/prey-b6d4da76-29fe-4771-9a60-c8e1c46e816e", "subscription"},
		{"https://www.hulu.com/movie/55349764-323e-4d0e-898f-a4c12c9bf615", "subscription"},
		{"https://www.peacocktv.com/stream-movies/jurassic-world-rebirth", "subscription"},
		{"https://www.peacocktv.com/watch-online/movies/drama/tuscaloosa/c2e453cf-4dc6-3a26-b2b3-db619d00b904", "subscription"},
		{"https://play.hbomax.com/movie/12345678-1234-1234-1234-123456789abc", "subscription"},
		{"https://www.max.com/movies/fixture-film/12345678-1234-1234-1234-123456789abc", "subscription"},
		{"https://www.paramountplus.com/movies/video/Alcn0hcGx0HosdhcawKteH8DXh3RiOF7/", "subscription"},
		{"https://athome.fandango.com/content/browse/details/Super/1161328", "unknown"},
		{"https://www.vudu.com/content/browse/details/Super/1161328", "unknown"},
		{"https://www.rakuten.tv/uk/movies/interstellar", "unknown"},
		{"https://pluto.tv/us/on-demand/movies/61c2323f68b5ad001a6211e1/details", "free"},
		{"https://pluto.tv/uk/watch/on-demand/movies/fixture-film-2014", "free"},
		{"https://filmzie.com/content/imprint-2007", "free"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{watchOptionFixture(tc.raw)})
			if len(got) != 1 || got[0].AccessType != tc.access || got[0].AccessBasis != "provider_model" || got[0].AccessEvidenceURL == "" || got[0].Price != "" || got[0].AvailabilityStatus != "unknown" || !strings.Contains(got[0].AccessNote, "unconfirmed") {
				t.Fatalf("service model became a title/region/price claim or route was lost: %+v", got)
			}
			checked := VerifiedSource{CandidateSource: got[0], Verified: true, VerificationStatus: "media_reachable"}
			checked.DirectStreamURL = "https://movie-cdn.example/fixture.mp4"
			if eligibleFreePlayback(checked) {
				t.Fatal("a provider access model must not authorize a native playback sample")
			}
		})
	}
}

func TestCommercialProviderGuidesCollectionsAndTrailersAreExcluded(t *testing.T) {
	for _, raw := range []string{
		"https://www.disneyplus.com/browse", "https://www.disneyplus.com/browse/entity-not-a-title", "https://www.disneyplus.com/browse/collection-12345678-1234-1234-1234-123456789abc", "https://help.disneyplus.com/movies/fixture/1234567890",
		"https://www.hulu.com/hub/movies/collections/11122", "https://www.hulu.com/guides/movie/fixture", "https://www.hulu.com/movie/fixture", "https://www.hulu.com/movie/55349764-323e-4d0e-898f-a4c12c9bf615/trailer",
		"https://www.peacocktv.com/blog/jurassic-world-rebirth", "https://www.peacocktv.com/watch-online/movies", "https://www.peacocktv.com/stream-movies", "https://www.peacocktv.com/stream-movies/fixture/trailer",
		"https://help.hbomax.com/movie/12345678-1234-1234-1234-123456789abc", "https://www.hbomax.com/collections/whats-new", "https://play.hbomax.com/movie/12345678-1234-1234-1234-123456789abc/reviews",
		"https://www.paramountplus.com/movies", "https://www.paramountplus.com/movies/trailer/video/IyIURrRaJeLBQiCZEOAbYStCRRxLmLAW/", "https://www.paramountplus.com/shows/video/Alcn0hcGx0HosdhcawKteH8DXh3RiOF7/",
		"https://www.fandango.com/fixture-movie-123/movie-overview", "https://athome.fandango.com/content/browse/details/fixture", "https://athome.fandango.com/content/browse/details/fixture/123/trailer", "https://www.rakuten.tv/uk/movies", "https://www.rakuten.tv/uk/search/movie/fixture",
		"https://pluto.tv/us/movies/", "https://pluto.tv/us/watch/live-tv/", "https://pluto.tv/us/on-demand/movies", "https://pluto.tv/us/on-demand/movies/fixture/trailer", "https://filmzie.com/home", "https://filmzie.com/content", "https://filmzie.com/content/fixture/trailer",
	} {
		if got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{watchOptionFixture(raw)}); len(got) != 0 {
			t.Errorf("non-movie destination survived: %s → %+v", raw, got)
		}
	}
}

func TestPlexRequiresExactPrimaryMovieOfferInsteadOfSearchText(t *testing.T) {
	for _, access := range []string{"free", "rent", "buy", "paid"} {
		candidate := watchOptionFixture("https://watch.plex.tv/en-GB/movie/fixture-film")
		candidate.AccessType, candidate.AccessBasis, candidate.AccessEvidenceURL = access, "page_text", candidate.URL
		got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{candidate})
		if len(got) != 1 || got[0].AccessType != access || got[0].AccessBasis != "page_text" || eligibleFreePlayback(VerifiedSource{CandidateSource: got[0], Verified: true, VerificationStatus: "media_reachable"}) {
			t.Fatalf("primary Plex offer was lost or implied playback: %+v", got)
		}
	}
	for _, mutate := range []func(*CandidateSource){
		func(c *CandidateSource) {
			c.AccessBasis = "search_excerpt"
			c.Description = "Watch this full movie free on Plex"
		},
		func(c *CandidateSource) { c.AccessEvidenceURL = "https://watch.plex.tv/movie/another-film" },
		func(c *CandidateSource) { c.AccessType = "subscription" },
		func(c *CandidateSource) { c.AccessType = "unknown" },
		func(c *CandidateSource) { c.URL += "/review/123"; c.AccessEvidenceURL = c.URL },
	} {
		candidate := watchOptionFixture("https://watch.plex.tv/movie/fixture-film")
		candidate.AccessType, candidate.AccessBasis, candidate.AccessEvidenceURL = "free", "page_text", candidate.URL
		mutate(&candidate)
		if got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{candidate}); len(got) != 0 {
			t.Fatalf("guide/search evidence became a Plex watch offer: %+v", got)
		}
	}
}

func TestWatchOptionsExcludeGuideHostsEvenWithMovieRoutes(t *testing.T) {
	for _, host := range []string{"imdb.com", "en.wikipedia.org", "justwatch.com", "reelgood.com", "flicks.co.uk", "useflickd.com", "ontvtonight.com", "rottentomatoes.com", "letterboxd.com", "web.playpilot.com"} {
		candidate := watchOptionFixture("https://" + host + "/movie/fixture")
		candidate.DirectStreamURL = "https://cdn.example/fixture.mp4"
		candidate.SourceType = "HLS Reference"
		if got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{candidate}); len(got) != 0 {
			t.Errorf("guide host retained despite destination policy: %s", host)
		}
	}
}

func TestTanzaniaWatchOptionsPrioritizeEvidenceWithoutInventingAvailability(t *testing.T) {
	local := watchOptionFixture("https://itunes.apple.com/tz/movie/fixture/id1")
	local.Region, local.SourceType, local.Provider = "TZ", "Movie Store", "Apple TV / iTunes"
	local.AccessType, local.Price = "buy", "Buy TZS 5000.00"
	foreign := watchOptionFixture("https://tv.apple.com/us/movie/fixture/123")
	got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{
		foreign,
		watchOptionFixture("https://www.primevideo.com/detail/123"),
		watchOptionFixture("https://www.netflix.com/title/123"),
		local,
		watchOptionFixture("https://tubitv.com/en/movies/123/fixture"),
	})
	if len(got) != 4 {
		t.Fatalf("known unsupported Tubi must be omitted: %+v", got)
	}
	want := []string{"regional_listing", "service_available", "service_available", "other_region"}
	for i, source := range got {
		if source.AvailabilityStatus != want[i] || source.DestinationType != "watch" {
			t.Errorf("wrong priority/status at %d: %+v", i, source)
		}
	}
	if got[0].AccessType != "buy" || got[0].Price != local.Price || got[0].AccessBasis != "catalog" || got[0].AccessEvidenceURL != local.URL {
		t.Errorf("regional catalog price/evidence was replaced: %+v", got[0])
	}
	if got[1].AccessType != "subscription" || got[1].AccessBasis != "provider_model" || got[1].AvailabilityEvidenceURL != netflixMarketEvidence || got[1].AccessEvidenceURL != netflixAccessEvidence || !strings.Contains(got[1].AvailabilityNote, "this title is unconfirmed") {
		t.Errorf("Netflix service evidence overstated title availability: %+v", got[1])
	}
	if got[2].AccessType != "paid" || got[2].Price != "" || got[2].AvailabilityEvidenceURL != primeTanzaniaEvidence {
		t.Errorf("Prime access/availability misrepresented: %+v", got[2])
	}
	if got[3].Region != "US" || got[3].URL != foreign.URL || got[3].AvailabilityEvidenceURL != foreign.URL {
		t.Errorf("foreign market was lost or URL rewritten: %+v", got[3])
	}
}

func TestTanzaniaServiceAndAccessEvidenceRemainSeparate(t *testing.T) {
	for _, tc := range []struct {
		raw, region, wantStatus, wantMarket string
	}{
		{"https://www.netflix.com/us/title/123", "Unknown", "other_region", "US"},
		{"https://www.netflix.com/title/123", "ZA", "other_region", "ZA"},
		{"https://tv.apple.com/movie/fixture/123", "Unknown", "service_available", "Unknown"},
		{"https://source.tz/watch/fixture", "Unknown", "unknown", "Unknown"},
		{"https://source.example/watch/fixture?country=TZ", "TZ", "unknown", "TZ"},
		{"https://mubi.com/de/films/fixture", "Unknown", "unknown", "Unknown"},
	} {
		candidate := watchOptionFixture(tc.raw)
		candidate.Region = tc.region
		got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{candidate})
		if len(got) != 1 || got[0].AvailabilityStatus != tc.wantStatus || got[0].Region != tc.wantMarket {
			t.Errorf("market context inferred unsupported availability: %s → %+v", tc.raw, got)
		}
	}
	negative := watchOptionFixture("https://www.netflix.com/title/123")
	negative.Description = "Sorry, this title is not available in your country."
	got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{negative})
	if len(got) != 1 || got[0].AvailabilityStatus != "unknown" || !strings.Contains(got[0].AvailabilityNote, "regional restriction") {
		t.Fatalf("ambiguous search excerpt became a Tanzania availability claim: %+v", got)
	}
}

func TestTubiUnsupportedInTanzaniaButFreeModelIsExplicitlyAdSupported(t *testing.T) {
	for _, raw := range []string{"https://tubitv.com/en/movies/123/fixture", "https://tubitv.com/us/movies/123/fixture"} {
		candidate := watchOptionFixture(raw)
		u, _ := movieWebURL(raw)
		enrichMovieAccess(&candidate, "tubi")
		enrichMovieAvailability(&candidate, u, "tubi", "TZ")
		if candidate.AvailabilityStatus != "unavailable" || candidate.AvailabilityEvidenceURL != tubiMarketEvidence || candidate.AccessType != "free" || !strings.Contains(candidate.AccessNote, "ads") {
			t.Errorf("Tubi market/model evidence misstated: %+v", candidate)
		}
		if got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{watchOptionFixture(raw)}); len(got) != 0 {
			t.Error("unsupported service remained a primary Tanzania watch option")
		}
	}
}

func TestYouTubeAndPlexDoNotAutomaticallyBecomeFree(t *testing.T) {
	youtube := watchOptionFixture("https://youtube.com/watch?v=123")
	youtube.AccessType = "free" // A bare classification has no supporting evidence.
	plex := watchOptionFixture("https://watch.plex.tv/movie/fixture")
	plex.DirectStreamURL = "https://cdn.example/fixture.mp4"
	got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{youtube, plex})
	if len(got) != 2 {
		t.Fatalf("individual YouTube or Plex with explicit media evidence was lost: %+v", got)
	}
	for _, source := range got {
		if source.AccessType != "unknown" || source.AvailabilityStatus != "unknown" {
			t.Errorf("platform was inferred free or locally available: %+v", source)
		}
	}
	youtube.AccessBasis, youtube.AccessEvidenceURL = "page_text", youtube.URL
	got = prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{youtube})
	if len(got) != 1 || got[0].AccessType != "free" || got[0].AccessBasis != "page_text" {
		t.Fatal("explicit page evidence was discarded")
	}
}

func TestKnownOpenMediaCatalogRemainsAvailableWithoutLocalAvailabilityClaims(t *testing.T) {
	catalog := watchOptionFixture("https://mango.blender.org/")
	catalog.ID, catalog.SourceType, catalog.AccessType = "ref-tears-of-steel", "HLS Reference", "free"
	catalog.DirectStreamURL = "https://cdn.example/tears-of-steel.m3u8"
	got := prepareMovieWatchOptions("Fixture Film", "TZ", []CandidateSource{catalog})
	if len(got) != 1 || got[0].AccessType != "free" || got[0].AccessBasis != "catalog" || got[0].AvailabilityStatus != "unknown" {
		t.Fatalf("open media reference was lost or called locally available: %+v", got)
	}
}

func TestWatchOptionsGlobalModeIsUnknownDeterministicAndUntruncated(t *testing.T) {
	var candidates []CandidateSource
	for i := 0; i < 40; i++ {
		candidate := watchOptionFixture(fmt.Sprintf("https://source.example/watch/fixture-%02d", i))
		candidates = append(candidates, candidate)
	}
	foreign := watchOptionFixture("https://tv.apple.com/us/movie/fixture/123")
	foreign.Region = "US"
	candidates = append(candidates, foreign)
	first := prepareMovieWatchOptions("Fixture Film", "ALL", candidates)
	for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}
	second := prepareMovieWatchOptions("Fixture Film", "ALL", candidates)
	if len(first) != 41 || !reflect.DeepEqual(first, second) {
		t.Fatal("watch option preparation truncated results or depended on provider completion order")
	}
	for _, source := range first {
		if source.AvailabilityStatus != "unknown" {
			t.Errorf("global search inferred regional availability: %+v", source)
		}
	}
}

func TestGlobalWatchOptionsKeepAllMarketsWithoutTanzaniaPreferences(t *testing.T) {
	got := prepareMovieWatchOptions("Fixture Film", "ALL", []CandidateSource{
		watchOptionFixture("https://tv.apple.com/us/movie/fixture/123"),
		watchOptionFixture("https://tubitv.com/en/movies/123/fixture"),
		watchOptionFixture("https://www.netflix.com/tz/title/123"),
		watchOptionFixture("https://www.netflix.com/jp/title/456"),
	})
	if len(got) != 4 {
		t.Fatalf("global search excluded a country's movie option: %+v", got)
	}
	markets := make(map[string]bool)
	for _, source := range got {
		markets[source.Region] = true
		if source.AvailabilityStatus != "unknown" || source.AvailabilityEvidenceURL != "" || strings.Contains(source.AvailabilityNote, "Tanzania") {
			t.Errorf("global result retained a Tanzania availability preference: %+v", source)
		}
		if strings.Contains(source.URL, "tubitv.com") && source.AccessType != "free" {
			t.Error("free provider access model was lost in global mode")
		}
	}
	for _, market := range []string{"US", "TZ", "JP"} {
		if !markets[market] {
			t.Errorf("offer's market metadata was not preserved: %s", market)
		}
	}
}

func TestResolvedMetadataEvidenceSurvivesWatchPreparation(t *testing.T) {
	const videoID = "12345678-1234-1234-1234-123456789abc"
	for _, tc := range []struct {
		name, page, basis string
	}{
		{"Archive", "https://archive.org/details/fixture-2014", "archive_metadata"},
		{"Public Domain Movies", "https://publicdomainmovie.net/movie/fixture-film", "archive_metadata"},
		{"PeerTube CDN", "https://video.blender.org/videos/watch/" + videoID, "peertube_api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: movieProviderTransport(func(req *http.Request) (*http.Response, error) {
				var response *http.Response
				switch req.URL.Hostname() {
				case "publicdomainmovie.net":
					response = providerResponse(200, `<main><div class="field-name-download"><a href="/movie.php?id=fixture-2014&amp;type=.mp4">Download Movie</a></div></main>`)
				case "archive.org":
					response = providerResponse(200, `{"metadata":{"identifier":"fixture-2014","title":"Fixture Film","mediatype":"movies","year":"2014","runtime":"600"},"files":[{"name":"fixture.mp4","length":"600","size":"1234567"}]}`)
				case "video.blender.org":
					response = providerResponse(200, `{"uuid":"`+videoID+`","name":"Fixture Film","duration":600,"privacy":{"id":1},"files":[{"fileUrl":"https://cdn.blender.org/fixture.mp4","resolution":{"id":360}}]}`)
				default:
					t.Fatalf("unexpected fixture request: %s", req.URL)
					return nil, fmt.Errorf("unexpected fixture request")
				}
				response.Request = req
				return response, nil
			})}
			source := watchOptionFixture(tc.page)
			source.Title, source.ReleaseYear = "Fixture Film (2014)", 2014
			resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
			if !ok || resolved.AccessBasis != tc.basis {
				t.Fatalf("fixture metadata did not resolve: %+v", resolved)
			}
			prepared := prepareMovieWatchOptions("Fixture Film 2014", "ALL", []CandidateSource{resolved})
			if len(prepared) != 1 || prepared[0].AccessBasis != resolved.AccessBasis || prepared[0].AccessEvidenceURL != resolved.AccessEvidenceURL {
				t.Fatalf("watch preparation lost exact metadata evidence: %+v", prepared)
			}
			checked := VerifiedSource{CandidateSource: prepared[0], Verified: true, VerificationStatus: "media_reachable"}
			if !eligibleFreePlayback(checked) {
				t.Fatalf("resolved, reachable media is no longer eligible: %+v", checked)
			}
			resolved.AccessEvidenceURL = "https://unrelated.example/metadata/fixture-2014"
			prepared = prepareMovieWatchOptions("Fixture Film 2014", "ALL", []CandidateSource{resolved})
			if len(prepared) != 1 {
				t.Fatal("invalid metadata evidence should leave a source listing")
			}
			checked.CandidateSource = prepared[0]
			if checked.AccessBasis == tc.basis || eligibleFreePlayback(checked) {
				t.Fatalf("unrelated metadata evidence survived as a playback association: %+v", checked)
			}
		})
	}
}
