package scout

import (
	"strings"
	"time"
)

// A selected live catalog title can retain its actual provider page even when
// the provider's first search page does not include it. These are short-lived
// listings; every availability, metadata and native playback check stays fresh.
func (s *Scout) fawesomeCatalogMovieCandidates(query string) []CandidateSource {
	now := time.Now()
	s.discoveryMu.Lock()
	var items []MovieDiscoveryItem
	for _, key := range []string{"fawesome:popular", "fawesome:recent"} {
		cached, found := s.discoveryCache[key]
		if !found || now.Sub(cached.at) >= fawesomeCatalogTTL(cached.report) {
			continue
		}
		items = append(items, cached.report.Items...)
	}
	s.discoveryMu.Unlock()
	var matches []CandidateSource
	seen := map[string]bool{}
	for _, item := range items {
		candidate := CandidateSource{
			ID: resolvedFreeMovieID("fawesome-catalog", item.SourceURL), Title: strings.TrimSpace(item.Title), URL: item.SourceURL,
			Provider: "Fawesome", SourceType: "Provider movie listing", Thumbnail: item.Thumbnail,
			AccessType: "free", AccessBasis: "provider_model", AccessEvidenceURL: "https://fawesome.tv/",
			AccessNote:  "Listed in Fawesome's recently fetched public movie catalog. Current movie availability and protected playback are checked separately; the original release year remains unconfirmed.",
			ListingOnly: true, DestinationType: "watch", Region: "Unknown",
		}
		if seen[item.SourceURL] || candidate.Title == "" || !fawesomeMovieTarget(candidate) || relevanceScore(query, candidate) == 0 {
			continue
		}
		matches = append(matches, candidate)
		seen[item.SourceURL] = true
		if len(matches) >= 12 {
			break
		}
	}
	return matches
}
