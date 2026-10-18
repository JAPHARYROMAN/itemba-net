package scout

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func playbackFixture(id string) VerifiedSource {
	return VerifiedSource{CandidateSource: CandidateSource{
		ID: "ia-" + id, Title: "Fixture Film", URL: "https://archive.org/embed/" + id,
		DirectStreamURL: "https://archive.org/download/" + id + "/film.mp4", Provider: "Internet Archive", SourceType: "Archive video",
		AccessType: "free", Duration: "600", AvailabilityStatus: "unknown",
	}, Verified: true, VerificationStatus: "media_reachable", AvailabilityCheck: &AvailabilityCheck{Status: "media_reachable", Country: "TZ", Method: "http_media_probe"}}
}

func TestConcurrentSearchesShareThePlaybackProcessBudget(t *testing.T) {
	var active, maximum atomic.Int32
	entered, release := make(chan struct{}, 4), make(chan struct{})
	s := &Scout{playbackSlots: make(chan struct{}, 2), playbackProbe: func(ctx context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		defer active.Add(-1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return startedFixture(region, country)
	}}
	var wg sync.WaitGroup
	for search := 0; search < 2; search++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.checkFreeMoviePlayback(context.Background(), []VerifiedSource{playbackFixture("one"), playbackFixture("two")}, "TZ", "TZ")
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			wg.Wait()
			t.Fatal("playback slots did not start")
		}
	}
	select {
	case <-entered:
		close(release)
		wg.Wait()
		t.Fatal("more than two browsers were admitted")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if maximum.Load() != 2 || active.Load() != 0 {
		t.Fatalf("global budget/cleanup incorrect: peak=%d active=%d", maximum.Load(), active.Load())
	}
}

func startedFixture(region, country string) *PlaybackCheck {
	return &PlaybackCheck{Status: "started", RequestedRegion: region, Country: country, Method: "isolated_video_sample", PlaybackConfirmed: true, SecondsAdvanced: 1.2, DecodedFrames: 30, Width: 640, Height: 360}
}

func TestPlaybackBudgetSpansSitesAndDoesNotRepeatIdenticalStreams(t *testing.T) {
	first, second, third := playbackFixture("one"), playbackFixture("two"), playbackFixture("three")
	third.URL, third.Provider = "https://commons.wikimedia.org/wiki/File:Fixture.webm", "Wikimedia Commons"
	results := []VerifiedSource{first, second, third}
	selected := diversePlaybackIndexes(results, []int{0, 1, 2})
	if len(selected) != 2 || selected[0] != 0 || selected[1] != 2 {
		t.Fatalf("one site consumed the whole sample budget: %v", selected)
	}
	results[2].DirectStreamURL = first.DirectStreamURL
	selected = diversePlaybackIndexes(results, []int{0, 1, 2})
	if len(selected) != 2 || selected[0] != 0 || selected[1] != 1 {
		t.Fatalf("identical video sampled twice or distinct video lost: %v", selected)
	}
}

func TestPlaybackSamplesDiscoveredHostBeforeReferenceCDN(t *testing.T) {
	archive := playbackFixture("one")
	archive.RelevanceScore = 100
	reference := VerifiedSource{CandidateSource: matchReferenceCatalog("Big Buck Bunny")[0], Verified: true, VerificationStatus: "media_reachable", RelevanceScore: 100, AvailabilityCheck: &AvailabilityCheck{Status: "media_reachable", Country: "TZ"}}
	peer := playbackFixture("peer")
	peer.ID, peer.Provider, peer.SourceType = "pt-resolved-fixture", "PeerTube (video.blender.org)", "Federated PeerTube"
	peer.URL = "https://video.blender.org/videos/watch/12345678-1234-1234-1234-123456789abc"
	peer.DirectStreamURL = "https://video.blender.org/files/film.mp4"
	peer.AccessBasis, peer.AccessEvidenceURL = "peertube_api", "https://video.blender.org/api/v1/videos/12345678-1234-1234-1234-123456789abc"
	peer.RelevanceScore = 100
	var mu sync.Mutex
	called := map[string]bool{}
	s := &Scout{playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		mu.Lock()
		called[source.ID] = true
		mu.Unlock()
		return startedFixture(region, country)
	}}
	s.checkFreeMoviePlayback(context.Background(), []VerifiedSource{archive, reference, peer}, "ALL", "TZ")
	if !called[archive.ID] || !called[peer.ID] || called[reference.ID] {
		t.Fatalf("reference fallback consumed a discovered host's sample slot: %v", called)
	}
}

func TestAutomaticPlaybackBudgetAndPaidExclusion(t *testing.T) {
	results := []VerifiedSource{playbackFixture("one"), playbackFixture("two"), playbackFixture("three"), playbackFixture("paid"), playbackFixture("listing")}
	results[0].RelevanceScore, results[1].RelevanceScore, results[2].RelevanceScore = 20, 80, 90
	results[3].AccessType = "buy"
	results[4].ListingOnly = true
	var mu sync.Mutex
	called := map[string]bool{}
	s := &Scout{playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		mu.Lock()
		called[source.ID] = true
		mu.Unlock()
		return startedFixture(region, country)
	}}
	s.checkFreeMoviePlayback(context.Background(), results, "TZ", "TZ")
	if len(called) != 2 || !called["ia-two"] || !called["ia-three"] {
		t.Fatalf("sample budget or relevance selection incorrect: %v", called)
	}
	if results[3].PlaybackCheck != nil || results[0].PlaybackCheck.Status != "skipped" || results[4].PlaybackCheck.Status != "skipped" || results[0].PlaybackCheck.CheckedAt != "" {
		t.Fatal("paid, unchecked or unsupported source was presented as tested")
	}
	coverage := movieAvailabilityCoverage("TZ", "TZ", "", results)
	if coverage.PlaybackCheckedCount != 2 || coverage.PlaybackStartedCount != 2 {
		t.Fatalf("sample coverage incorrect: %+v", coverage)
	}
}

func TestPlaybackPriorityRequiresDecodedProgressAndActualCountry(t *testing.T) {
	media := playbackFixture("media")
	started := playbackFixture("started")
	started.PlaybackCheck = startedFixture("TZ", "TZ")
	if checkedWatchPriority(started, "TZ") >= checkedWatchPriority(media, "TZ") {
		t.Fatal("actual Tanzania playback did not rank above media reachability")
	}
	for _, country := range []string{"", "US"} {
		started.PlaybackCheck.Country = country
		if checkedWatchPriority(started, "TZ") < checkedWatchPriority(media, "TZ") {
			t.Fatalf("%q playback was promoted as Tanzania evidence", country)
		}
	}
	started.PlaybackCheck.Country = "TZ"
	started.PlaybackCheck.DecodedFrames = 0
	if playbackStarted(started) {
		t.Fatal("a clock without decoded frames was accepted")
	}
	started.PlaybackCheck = startedFixture("TZ", "TZ")
	started.AccessType = "buy"
	if playbackStarted(started) {
		t.Fatal("a paid listing was accepted as free playback")
	}
	started.AccessType = "free"
	started.PlaybackCheck.PlaybackConfirmed = false
	if playbackStarted(started) {
		t.Fatal("an unconfirmed playback status was accepted")
	}
}

func TestFailedPlaybackRetainsReachabilityAndCancelledSearchDoesNotLaunch(t *testing.T) {
	results := []VerifiedSource{playbackFixture("one")}
	s := &Scout{playbackProbe: func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		return &PlaybackCheck{Status: "not_started", Country: country, RequestedRegion: region, Method: "isolated_video_sample"}
	}}
	s.checkFreeMoviePlayback(context.Background(), results, "TZ", "TZ")
	if results[0].AvailabilityCheck.Status != "media_reachable" || !results[0].Verified || results[0].PlaybackCheck.Status != "not_started" {
		t.Fatal("failed sample erased independent media reachability")
	}
	if coverage := movieAvailabilityCoverage("TZ", "TZ", "", results); coverage.PlaybackCheckedCount != 1 || coverage.PlaybackStartedCount != 0 {
		t.Fatalf("failed sample counts incorrect: %+v", coverage)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.playbackProbe = func(context.Context, VerifiedSource, string, string) *PlaybackCheck {
		t.Fatal("cancelled search launched playback")
		return nil
	}
	s.checkFreeMoviePlayback(ctx, results, "TZ", "TZ")
	if results[0].PlaybackCheck.Status != "skipped" {
		t.Fatal("cancelled search claimed a fresh sample")
	}
}
