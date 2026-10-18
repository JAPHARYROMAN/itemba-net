package scout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

// An explicit live integration of a newly supported catalog site. The normal
// test suite stays offline; this opt-in downloads only the bounded sample and
// creates its own disposable browser, without loading secrets or account state.
func TestLivePublicDomainMovieResolutionAndPlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_PUBLIC_DOMAIN_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_PUBLIC_DOMAIN_TEST=1 for the bounded public-domain-site integration")
	}
	source := CandidateSource{
		ID: "web-public-domain-movie", Title: "Night of the Living Dead", ReleaseYear: 1968,
		URL:        "https://publicdomainmovie.net/movie/night-of-the-living-dead",
		SourceType: "Open Web", AccessType: "unknown", ListingOnly: true,
	}
	runLiveResolvedFreeMoviePlayback(t, source, "Night of the Living Dead 1968", "archive_metadata")
}

func TestLivePeerTubeMovieResolutionAndPlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_FREE_SOURCE_TEST") != "1" {
		t.Skip("set ITEMBA_LIVE_FREE_SOURCE_TEST=1 for the bounded public PeerTube integration")
	}
	source := CandidateSource{
		ID: "web-blender-peer-movie", Title: "Big Buck Bunny", Duration: "9:56",
		URL:        "https://video.blender.org/videos/watch/bf1f3fb5-b119-4f9f-9930-8e20e892b898",
		SourceType: "Open Web", AccessType: "unknown", ListingOnly: true,
	}
	runLiveResolvedFreeMoviePlayback(t, source, "Big Buck Bunny", "peertube_api")
}

func runLiveResolvedFreeMoviePlayback(t *testing.T, source CandidateSource, query, basis string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	scout := &Scout{availabilityHTTPClient: client}
	country, _ := scout.availabilityCheckCountry(ctx)
	resolved, status := scout.resolveFreeMovieSources(ctx, []CandidateSource{source})
	prepared := prepareMovieWatchOptions(query, "ALL", resolved)
	if len(prepared) != 1 || prepared[0].AccessBasis != basis || prepared[0].ListingOnly || prepared[0].DirectStreamURL == "" {
		t.Fatalf("new free site did not resolve through watch preparation: %#v, %#v", status, prepared)
	}
	verified := verifyFastHTTPWithClient(ctx, shield.New(), prepared[0], client)
	if !eligibleFreePlayback(verified) {
		t.Fatalf("new free site's direct movie did not pass separate media probe: %s; %s", verified.VerificationStatus, verified.PrunedReason)
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, freePlaybackDeadline)
	defer checkCancel()
	origin, closeHarness, err := startMoviePlaybackHarness(checkCtx, verified, client)
	if err != nil {
		t.Fatal("isolated playback harness unavailable")
	}
	defer closeHarness()
	browser, err := launchFreePlaybackBrowser(checkCtx)
	if err != nil {
		t.Fatal("isolated playback browser unavailable")
	}
	defer func() {
		browser.Close()
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, err := os.Stat(browser.UserDataDir)
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil || time.Now().After(deadline) {
				t.Error("disposable playback profile cleanup could not be confirmed")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	result := runFreePlaybackSample(checkCtx, browser, verified, origin, &PlaybackCheck{
		Status: "unconfirmed", Method: "isolated_video_sample", RequestedRegion: "ALL", Country: country,
		CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL,
	})
	proof, _ := json.Marshal(struct {
		Page       string         `json:"page"`
		Media      string         `json:"media"`
		Resolution ProviderStatus `json:"resolution"`
		Sample     *PlaybackCheck `json:"sample"`
	}{source.URL, verified.DirectStreamURL, status, result})
	t.Logf("added-site proof: %s", proof)
	if result.Status != "started" || !result.PlaybackConfirmed {
		t.Fatalf("added site's bounded movie sample did not start: %s", result.Detail)
	}
}
