package scout

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func plexOfferFixture() string {
	return `<title>Watch Fixture Film (2025) - Plex</title><main><h1 data-testid="metadata-title">Fixture Film</h1><p>2025</p><a href="/watch/movie?key=movie-id">Watch for Free</a><h2>Where to Watch Fixture Film</h2><p>Other streaming providers may require payment.</p></main>`
}

func TestPrimaryPlexOfferRequiresMatchingVisibleMovieAction(t *testing.T) {
	source := CandidateSource{Title: "Fixture Film", ReleaseYear: 2025, URL: "https://watch.plex.tv/movie/fixture-film"}
	if !plexPrimaryFreeOffer([]byte(plexOfferFixture()), source) {
		t.Fatal("matching primary free offer was not recognized")
	}
	for name, mutate := range map[string]func(string) string{
		"wrong movie": func(v string) string { return strings.ReplaceAll(v, "Fixture Film", "Another Film") },
		"wrong year":  func(v string) string { return strings.ReplaceAll(v, "2025", "2024") },
		"regional restriction": func(v string) string {
			return strings.Replace(v, "<p>2025</p>", "<p>Not available in your country.</p>", 1)
		},
		"trailer destination": func(v string) string {
			return strings.ReplaceAll(v, "/watch/movie?key=movie-id", "/watch/video?uri=extras-id")
		},
		"outside provider": func(v string) string {
			return strings.ReplaceAll(v, "/watch/movie?key=movie-id", "https://outside.provider.net/watch/movie")
		},
		"hidden action":     func(v string) string { return strings.Replace(v, `<a href=`, `<a hidden href=`, 1) },
		"navigation action": func(v string) string { return strings.Replace(v, `<a href=`, `<nav><a href=`, 1) },
		"recommendation action": func(v string) string {
			return strings.Replace(v, `<a href=`, `<div class="recommendations"><a href=`, 1)
		},
		"secondary free offer": func(v string) string { return strings.Replace(v, `<a href=`, `<h2>Other movies</h2><a href=`, 1) },
		"only trailer":         func(v string) string { return strings.ReplaceAll(v, "Watch for Free", "Watch Trailer") },
		"service model only":   func(v string) string { return strings.ReplaceAll(v, "Watch for Free", "Plex has free movies") },
	} {
		t.Run(name, func(t *testing.T) {
			if plexPrimaryFreeOffer([]byte(mutate(plexOfferFixture())), source) {
				t.Fatal("secondary or unrelated evidence became a movie offer")
			}
		})
	}
}

func TestDirectWatchOfferChecksAreBoundedAndNeverAuthorizeNativeMedia(t *testing.T) {
	var requests atomic.Int32
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: freeSourceTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Error("primary-page check sent account state")
		}
		return additionalFeatureResponse(req, plexOfferFixture()), nil
	})}}
	var candidates []CandidateSource
	for _, slug := range []string{"one", "two", "three", "four"} {
		candidates = append(candidates, CandidateSource{ID: slug, Title: "Fixture Film", ReleaseYear: 2025, URL: "https://watch.plex.tv/movie/" + slug, AccessType: "unknown", ListingOnly: true})
	}
	candidates = append(candidates, CandidateSource{Title: "Fixture Film", URL: "https://www.netflix.com/title/123", AccessType: "subscription", ListingOnly: true})
	out, status := s.confirmDirectWatchOffers(context.Background(), candidates)
	if requests.Load() != 3 || status.Status != "ok" || out[3].AccessType != "unknown" || out[4].AccessType != "subscription" {
		t.Fatalf("offer budget or paid source changed: %d %+v", requests.Load(), out)
	}
	for _, source := range out[:3] {
		if source.AccessType != "free" || source.AccessBasis != "page_text" || source.AccessEvidenceURL != source.URL || !source.ListingOnly || source.DirectStreamURL != "" || eligibleFreePlayback(VerifiedSource{CandidateSource: source, Verified: true, VerificationStatus: "media_reachable"}) {
			t.Fatalf("offer text became playback proof: %+v", source)
		}
	}
}
