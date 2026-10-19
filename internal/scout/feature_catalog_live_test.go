package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// Discovery integration is opt-in; the normal suite never contacts providers.
// This checks current catalog/metadata identity and full feature runtime, while
// decoding and actual playback require the separate isolated sample test.
func TestLiveFeatureMovieDiscovery(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FEATURE_DISCOVERY") != "1" {
		t.Skip("set ITEMBA_LIVE_FEATURE_DISCOVERY=1 for bounded full feature metadata discovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	s := &Scout{httpClient: &http.Client{Timeout: 5 * time.Second}, availabilityHTTPClient: newMovieAvailabilityClient()}
	archive, status := s.queryDriver(ctx, s.driverFetcher(), testDriver(t, "internet-archive"), "His Girl Friday 1940", "His Girl Friday", "")
	if status.Status == "error" {
		t.Fatal(status.Detail)
	}
	full := 0
	for _, source := range archive {
		if source.DirectStreamURL != "" && source.AccessBasis == "archive_metadata" && mediaDurationSeconds(source.Duration) >= 40*60 {
			full++
			proof, _ := json.Marshal(struct{ Page, Media, Duration, Evidence string }{source.URL, source.DirectStreamURL, source.Duration, source.AccessEvidenceURL})
			t.Logf("feature metadata: %s", proof)
		}
	}
	if full == 0 {
		t.Fatalf("no public full feature edition resolved from %d catalog matches", len(archive))
	}
	catalog, catalogStatus := s.queryDriver(ctx, s.driverFetcher(), testDriver(t, "public-domain-movies"), "His Girl Friday 1940", "His Girl Friday", "")
	if catalogStatus.Status == "error" || len(catalog) == 0 {
		t.Fatalf("observed PDM browse did not discover feature: %+v", catalogStatus)
	}
	for _, source := range catalog {
		t.Logf("public feature browse: %s (%d) %s", source.Title, source.ReleaseYear, source.URL)
	}
}

func TestLiveFullFeaturePlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FEATURE_PLAYBACK") != "1" {
		t.Skip("set ITEMBA_LIVE_FEATURE_PLAYBACK=1 for a bounded full feature playback sample")
	}
	source := CandidateSource{
		ID: "web-full-feature-his-girl-friday", Title: "His Girl Friday", ReleaseYear: 1940, Duration: "1:31:44",
		URL: "https://archive.org/details/his_girl_friday", SourceType: "Open Web", AccessType: "unknown", ListingOnly: true,
	}
	runLiveResolvedFreeMoviePlayback(t, source, "His Girl Friday 1940", "archive_metadata")
}
