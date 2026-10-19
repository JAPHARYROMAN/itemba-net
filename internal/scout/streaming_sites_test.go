package scout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const arcoirisFeaturePage = "https://www.arcoiris.tv/scheda/en/766/"
const arcoirisFeatureMedia = "https://streaming.arcoiris.tv/store_06/lowres/en/publicdomaintorrents_com/sci-fi_fantasy/Night_of_the_Living_Dead.mp4"

func additionalFeatureCandidate(raw string) CandidateSource {
	return CandidateSource{ID: "web-feature", Title: "Night of the Living Dead", ReleaseYear: 1968, URL: raw, AccessType: "unknown", ListingOnly: true}
}

func arcoirisFeatureFixture() string {
	metadata, _ := json.Marshal(map[string]any{"@type": "VideoObject", "name": "Night of the Living Dead", "description": "1968 - Horror / Sci-Fi / Thriller", "contentUrl": arcoirisFeatureMedia, "embedUrl": arcoirisFeaturePage})
	return `<html><head><script type="application/ld+json">` + string(metadata) + `</script></head><body><h1>Night of the Living Dead</h1><ul><li>Fonte: <a>Public Domain</a> | Durata: 96,32 min | Pubblicato il: 1968</li></ul></body></html>`
}

func additionalFeatureResponse(request *http.Request, html string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(html)), Request: request}
}

func TestAdditionalFeatureTargetsRejectPaidAndUnsafeRoutes(t *testing.T) {
	for _, raw := range []string{
		"https://arcoiris.tv.evil.net/scheda/en/766/", "https://www.arcoiris.tv/scheda/12/766/", "https://www.arcoiris.tv/scheda/en/000766/", "https://www.arcoiris.tv/scheda/en/766/?src=other", "http://www.arcoiris.tv/scheda/en/766/", "https://publicdomainmovie.org/public-domain-movies-list/", "https://publicdomainmovie.org/night-of-the-living-dead-1968/?video=other", "http://127.0.0.1/scheda/en/766/",
	} {
		if _, _, ok := freeResolutionTarget(additionalFeatureCandidate(raw)); ok {
			t.Fatalf("unsupported catalog route accepted: %s", raw)
		}
	}
	paid := additionalFeatureCandidate(arcoirisFeaturePage)
	paid.AccessType = "subscription"
	if _, _, ok := freeResolutionTarget(paid); ok {
		t.Fatal("paid listing must never become a free source resolution target")
	}
	if _, ok := resolveAdditionalFreeMovie(context.Background(), nil, paid); ok {
		t.Fatal("paid listing must remain unchanged by direct adapter calls")
	}
}

func TestAdditionalFeatureUnresolvedMoviePagesRemainUnconfirmedListings(t *testing.T) {
	for _, page := range []string{arcoirisFeaturePage, "https://publicdomainmovie.org/night-of-the-living-dead-1968/"} {
		prepared := prepareMovieWatchOptions("Night of the Living Dead 1968", "ALL", []CandidateSource{additionalFeatureCandidate(page)})
		if len(prepared) != 1 || prepared[0].AccessType != "unknown" || !prepared[0].ListingOnly || prepared[0].DirectStreamURL != "" || eligibleFreePlayback(VerifiedSource{CandidateSource: prepared[0], Verified: true, VerificationStatus: "page_reachable"}) {
			t.Fatalf("unresolved public movie page must remain an unconfirmed listing: %#v", prepared)
		}
	}
}

func TestArcoirisFullFeatureMetadataResolvesWithoutProviderCode(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != arcoirisFeaturePage {
			t.Fatalf("resolver executed or requested provider code/media: %s", request.URL)
		}
		return additionalFeatureResponse(request, arcoirisFeatureFixture()), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, additionalFeatureCandidate(arcoirisFeaturePage))
	if !ok || requests != 1 || resolved.DirectStreamURL != arcoirisFeatureMedia || resolved.AccessBasis != "arcoiris_video_metadata" || mediaDurationSeconds(resolved.Duration) < 5700 || resolved.ListingOnly {
		t.Fatalf("expected bounded metadata-backed feature: %#v", resolved)
	}
	verified := VerifiedSource{CandidateSource: resolved, Verified: true, VerificationStatus: "media_reachable"}
	if !eligibleFreePlayback(verified) {
		t.Fatal("catalog-owned full feature should be sample eligible after media check")
	}
	assertPreparedAdditionalFeature(t, resolved)
	for _, mutate := range []func(*VerifiedSource){
		func(v *VerifiedSource) {
			v.DirectStreamURL = "https://streaming.arcoiris.tv.evil.net/store_06/lowres/en/movie.mp4"
		},
		func(v *VerifiedSource) { v.AccessEvidenceURL = "https://www.arcoiris.tv/scheda/en/777/" },
		func(v *VerifiedSource) { v.Duration = "180" },
		func(v *VerifiedSource) { v.AccessType = "subscription" },
	} {
		copy := verified
		mutate(&copy)
		if eligibleFreePlayback(copy) {
			t.Fatal("unsupported evidence must not authorize automatic playback")
		}
	}
}

func TestArcoirisRejectsShortHiddenAmbiguousWrongMovieAndPrivateMedia(t *testing.T) {
	tests := map[string]func(string) string{
		"short video":     func(v string) string { return strings.Replace(v, "96,32 min", "3,2 min", 1) },
		"hidden runtime":  func(v string) string { return strings.Replace(v, "<li>", "<li hidden>", 1) },
		"hidden metadata": func(v string) string { return strings.Replace(v, `<script type=`, `<script hidden type=`, 1) },
		"wrong heading": func(v string) string {
			return strings.Replace(v, "<h1>Night of the Living Dead</h1>", "<h1>Dawn of the Dead</h1>", 1)
		},
		"wrong metadata title": func(v string) string {
			return strings.Replace(v, `"name":"Night of the Living Dead"`, `"name":"Night of the Living Dead Documentary"`, 1)
		},
		"different release year":   func(v string) string { return strings.Replace(v, "1968 - Horror", "1990 - Horror", 1) },
		"missing release evidence": func(v string) string { return strings.Replace(v, "1968 - Horror", "Horror", 1) },
		"wrong media host":         func(v string) string { return strings.Replace(v, "streaming.arcoiris.tv", "127.0.0.1", 1) },
		"different page association": func(v string) string {
			return strings.Replace(v, `"embedUrl":"`+arcoirisFeaturePage+`"`, `"embedUrl":"https://www.arcoiris.tv/scheda/en/777/"`, 1)
		},
		"trailer filename": func(v string) string {
			return strings.Replace(v, "Night_of_the_Living_Dead.mp4", "Night_of_the_Living_Dead_trailer.mp4", 1)
		},
		"ambiguous runtime": func(v string) string {
			return strings.Replace(v, "</ul>", "<li>Fonte: Public Domain | Durata: 120 min</li></ul>", 1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return additionalFeatureResponse(request, mutate(arcoirisFeatureFixture())), nil
			})}
			resolved, ok := resolveFreeMovieSource(context.Background(), client, additionalFeatureCandidate(arcoirisFeaturePage))
			if ok || resolved.DirectStreamURL != "" || resolved.AccessType != "unknown" {
				t.Fatalf("untrusted metadata promoted listing: %#v", resolved)
			}
		})
	}
}

func publicDomainOrgFixture() string {
	return `<h1>Night of the Living Dead (1968) – Watch Free in HD</h1><div class="entry-content"><iframe src="https://archive.org/embed/night_feature"></iframe></div>`
}

func TestPublicDomainMovieOrgResolvesOnlyPrimaryMatchingArchiveFeature(t *testing.T) {
	page := "https://publicdomainmovie.org/night-of-the-living-dead-1968/"
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == page {
			return additionalFeatureResponse(request, publicDomainOrgFixture()+`<footer><iframe src="https://archive.org/embed/another_movie"></iframe></footer>`), nil
		}
		if request.URL.String() != "https://archive.org/metadata/night_feature" {
			t.Fatalf("unexpected linked media request: %s", request.URL)
		}
		return freeSourceJSONResponse(request, archiveResolutionFixture("night_feature")), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, additionalFeatureCandidate(page))
	if !ok || resolved.Provider != "PublicDomainMovie.org" || resolved.DirectStreamURL != "https://archive.org/download/night_feature/Night_Of_The_Living_Dead_512kb.mp4" || !eligibleFreePlayback(VerifiedSource{CandidateSource: resolved, Verified: true, VerificationStatus: "media_reachable"}) {
		t.Fatalf("primary full feature should resolve: %#v", resolved)
	}
	assertPreparedAdditionalFeature(t, resolved)
	for name, mutate := range map[string]func(string) string{
		"hidden player": func(v string) string {
			return strings.Replace(v, `class="entry-content"`, `class="entry-content" hidden`, 1)
		},
		"outside primary body": func(v string) string { return strings.Replace(v, `class="entry-content"`, `class="sidebar"`, 1) },
		"wrong year":           func(v string) string { return strings.Replace(v, "(1968)", "(1990)", 1) },
		"only trailer": func(v string) string {
			return strings.Replace(v, `class="entry-content"`, `class="entry-content trailers"`, 1)
		},
		"ambiguous players": func(v string) string {
			return strings.Replace(v, "</div>", `<iframe src="https://archive.org/embed/other_movie"></iframe></div>`, 1)
		},
		"private iframe": func(v string) string {
			return strings.Replace(v, "https://archive.org/embed/night_feature", "http://127.0.0.1/embed/night_feature", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := publicDomainOrgArchiveItem([]byte(mutate(publicDomainOrgFixture())), additionalFeatureCandidate(page)); ok {
				t.Fatal("non-primary or mismatching player must not be resolved")
			}
		})
	}
}

func assertPreparedAdditionalFeature(t *testing.T, resolved CandidateSource) {
	t.Helper()
	prepared := prepareMovieWatchOptions("Night of the Living Dead 1968", "ALL", []CandidateSource{resolved})
	if len(prepared) != 1 || prepared[0].AccessType != "free" || prepared[0].AccessBasis != resolved.AccessBasis || !eligibleFreePlayback(VerifiedSource{CandidateSource: prepared[0], Verified: true, VerificationStatus: "media_reachable"}) {
		t.Fatalf("movie preparation lost supported feature evidence: %#v", prepared)
	}
	corrupt := resolved
	corrupt.AccessEvidenceURL = "https://www.arcoiris.tv/scheda/en/999/"
	if corrupt.AccessBasis == "archive_metadata" {
		corrupt.AccessEvidenceURL = "https://archive.org/metadata/a_different_film"
	}
	prepared = prepareMovieWatchOptions("Night of the Living Dead 1968", "ALL", []CandidateSource{corrupt})
	if len(prepared) == 1 && eligibleFreePlayback(VerifiedSource{CandidateSource: prepared[0], Verified: true, VerificationStatus: "media_reachable"}) {
		t.Fatal("preparation must not make an unrelated metadata/media association eligible")
	}
}

// Opt-in public metadata + 12-second, 6 MiB native sample. Ordinary tests never
// launch Chromium or contact these sites, and no account cookies/keys are used.
func TestLiveAdditionalFreeFeaturePlaybackArcoiris(t *testing.T) {
	liveAdditionalFreeFeaturePlayback(t, arcoirisFeaturePage)
}

func TestLiveAdditionalFreeFeaturePlaybackPublicDomainOrg(t *testing.T) {
	liveAdditionalFreeFeaturePlayback(t, "https://publicdomainmovie.org/night-of-the-living-dead-1968/")
}

func liveAdditionalFreeFeaturePlayback(t *testing.T, raw string) {
	t.Helper()
	if os.Getenv("ITEMBA_LIVE_FEATURE_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_FEATURE_TEST=1 to run a bounded public feature sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	source, ok := resolveFreeMovieSource(ctx, client, additionalFeatureCandidate(raw))
	if !ok {
		t.Fatal("public full feature metadata could not be resolved")
	}
	scout := &Scout{availabilityHTTPClient: client}
	country, _ := scout.availabilityCheckCountry(ctx)
	verified := VerifiedSource{CandidateSource: source, Verified: true, VerificationStatus: "media_reachable"}
	result := scout.probeFreeMoviePlayback(ctx, verified, "ALL", country)
	encoded, _ := json.Marshal(result)
	t.Logf("full-feature metadata runtime: %s seconds; sample: %s", source.Duration, encoded)
	if result == nil || !result.PlaybackConfirmed || result.DecodedFrames <= 0 || result.SecondsAdvanced <= 0 || result.DurationSeconds < minimumAdditionalFeatureDuration {
		t.Error("full feature playback did not produce native decoded progress")
	}
	// Browser.Close removes its disposable profile asynchronously after process exit.
	time.Sleep(600 * time.Millisecond)
}

func TestAdditionalFeaturePlaybackPairRejectsCrossMovieArchiveFile(t *testing.T) {
	page, _ := url.Parse("https://publicdomainmovie.org/night-of-the-living-dead-1968/")
	evidence, _ := url.Parse("https://archive.org/metadata/night_feature")
	media, _ := url.Parse("https://archive.org/download/another_feature/movie.mp4")
	source := CandidateSource{ID: "pdmorg-resolved-example", Provider: "PublicDomainMovie.org", SourceType: "Public Domain feature", AccessBasis: "archive_metadata", Duration: "5700"}
	if additionalResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("metadata for one item must not authorize a different item's media")
	}
}

func TestAdditionalFeatureArticleMarketingDoesNotHideARealFeature(t *testing.T) {
	if !sameAdditionalFeatureTitle("His Girl Friday 1940", "His Girl Friday (1940) Full Movie Review, Plot, Cast & Free Comedy Classic") {
		t.Fatal("an article's release-label suffix must not hide its independently verified full feature")
	}
	if sameAdditionalFeatureTitle("His Girl Friday", "His Girl Friday Trailer (1940)") {
		t.Fatal("a trailer title must remain distinct from the feature")
	}
}
