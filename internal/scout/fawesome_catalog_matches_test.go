package scout

import (
	"testing"
	"time"
)

func TestFawesomeSelectedDynamicCatalogTitleKeepsExactPageAsUncheckedListing(t *testing.T) {
	item := MovieDiscoveryItem{Title: "Beyond The Examples", Query: "Beyond The Examples", SourceURL: "https://fawesome.tv/movies/12345678/beyond-the-examples", CatalogDate: "2026-10-01"}
	s := &Scout{discoveryCache: map[string]cachedMovieDiscovery{
		"fawesome:popular": {at: time.Now(), report: MovieDiscoveryReport{Items: []MovieDiscoveryItem{item}}},
		"fawesome:recent":  {at: time.Now(), report: MovieDiscoveryReport{Items: []MovieDiscoveryItem{item}}},
	}}
	matches := s.fawesomeCatalogMovieCandidates("Beyond The Examples")
	if len(matches) != 1 || matches[0].URL != item.SourceURL || matches[0].ReleaseYear != 0 || !matches[0].ListingOnly || matches[0].AccessBasis != "provider_model" || matches[0].providerContentBound || matches[0].providerStreamURL != "" || matches[0].DirectStreamURL != "" {
		t.Fatalf("a selected catalog title lost its exact page or inherited a movie year/media/playback claim: %+v", matches)
	}
	if len(s.fawesomeCatalogMovieCandidates("Entirely Different Film")) != 0 {
		t.Fatal("unrelated catalog films leaked into title search")
	}
	s.discoveryCache["fawesome:popular"] = cachedMovieDiscovery{at: time.Now().Add(-time.Hour), report: MovieDiscoveryReport{Items: []MovieDiscoveryItem{item}}}
	s.discoveryCache["fawesome:recent"] = s.discoveryCache["fawesome:popular"]
	if len(s.fawesomeCatalogMovieCandidates("Beyond The Examples")) != 0 {
		t.Fatal("expired catalog pages were silently retained")
	}
}
