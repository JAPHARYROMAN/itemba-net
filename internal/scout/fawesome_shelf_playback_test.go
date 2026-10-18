package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

func shelfFixtureItems(count int) []MovieDiscoveryItem {
	items := make([]MovieDiscoveryItem, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, MovieDiscoveryItem{ID: fmt.Sprintf("fixture-%d", i), Title: fmt.Sprintf("Fixture Movie %d", i), Query: fmt.Sprintf("Fixture Movie %d", i), SourceURL: fmt.Sprintf("https://fawesome.tv/movies/%d/fixture-movie-%d", 10540876+i, i), SourceName: "Fawesome catalog", Markets: []string{}})
	}
	return items
}

func shelfFixtureScout(t *testing.T, items []MovieDiscoveryItem, probe func(context.Context, VerifiedSource, string, string) *PlaybackCheck) (*Scout, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	pages := map[string]string{}
	for _, item := range items {
		info := fawesomeFixtureInfo()
		parts := fawesomeMoviePath.FindStringSubmatch(strings.TrimPrefix(item.SourceURL, "https://fawesome.tv"))
		fmt.Sscan(parts[1], &info.NodeID)
		info.Title, info.WebURL = item.Title, strings.TrimPrefix(item.SourceURL, "https://fawesome.tv/")
		page := fawesomeFixturePage(info, fmt.Sprintf("https://ftmain.cachefly.net/files/shelf/%d/movie.mp4?token=fixture-private", info.NodeID))
		page = strings.ReplaceAll(page, fawesomeFixtureSource().URL, item.SourceURL)
		page = strings.ReplaceAll(page, `var nodeId = "10540876"`, fmt.Sprintf(`var nodeId = "%d"`, info.NodeID))
		page = strings.ReplaceAll(page, "Abduction", item.Title)
		pages[item.SourceURL] = page
	}
	s := New(shield.New(), "http://127.0.0.1:8080")
	s.country, s.countryAt = "TZ", time.Now()
	s.availabilityHTTPClient = &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if page, found := pages[req.URL.String()]; found {
			return searchFixtureResponse(200, "text/html", page), nil
		}
		if req.URL.Hostname() == "ftmain.cachefly.net" {
			return searchFixtureResponse(206, "video/mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isom"), nil
		}
		if req.URL.String() == availabilityCountryEndpoint {
			return searchFixtureResponse(200, "text/plain", "loc=??\n"), nil
		}
		t.Error("shelf checks requested a search, account, catalog API or unrelated destination")
		return searchFixtureResponse(404, "text/plain", ""), nil
	})}
	s.playbackProbe = probe
	shelfSetCatalog(s, "popular", items)
	shelfSetCatalog(s, "recent", items)
	t.Cleanup(s.Close)
	return s, requests
}

func shelfSetCatalog(s *Scout, mode string, items []MovieDiscoveryItem) {
	s.discoveryMu.Lock()
	if s.discoveryCache == nil {
		s.discoveryCache = map[string]cachedMovieDiscovery{}
	}
	s.discoveryCache["fawesome:"+mode] = cachedMovieDiscovery{at: time.Now(), report: MovieDiscoveryReport{Mode: mode, Region: "ALL", Items: append([]MovieDiscoveryItem{}, items...), Providers: []ProviderStatus{{Name: "Fawesome", Status: "ok"}}}}
	s.discoveryMu.Unlock()
}

func shelfStarted(region, country string) *PlaybackCheck {
	check := startedFixture(region, country)
	check.DurationSeconds = 5835
	check.EvidenceURL = "https://private.invalid/main.m3u8?secret=fixture"
	check.Detail = "private signed diagnostic secret=fixture"
	return check
}

func shelfWait(t *testing.T, s *Scout, id string, predicate func(FawesomeShelfPlaybackReport) bool) FawesomeShelfPlaybackReport {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		report, found := s.FawesomeShelfPlaybackStatus(id)
		if !found {
			t.Fatal("queued shelf job disappeared")
		}
		if predicate(report) {
			return report
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bounded shelf fixture did not reach expected state")
	return FawesomeShelfPlaybackReport{}
}

func TestFawesomeShelfStableVerifiedFirstAndReadOnlyPolling(t *testing.T) {
	items := shelfFixtureItems(3)
	var probes atomic.Int32
	s, requests := shelfFixtureScout(t, items, func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		if region != "ALL" || country != "TZ" || !eligibleFreePlayback(source) {
			t.Error("shelf bypassed bound movie preflight or changed the connection")
		}
		if source.URL == items[0].SourceURL {
			return &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed"}
		}
		check := shelfStarted(region, country)
		if source.URL == items[2].SourceURL {
			check.DurationSeconds = 100 // a decoded trailer must not turn green
		}
		return check
	})
	initial, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil || len(initial.JobID) != 32 || initial.Region != "ALL" || !initial.AdBlockingEnabled || initial.Total != 3 || initial.StartedCount != 0 {
		t.Fatalf("finite global shelf job was not created: %+v %v", initial, err)
	}
	report := shelfWait(t, s, initial.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if report.CheckedCount != 3 || report.StartedCount != 1 || report.CheckingCount != 0 || report.PendingCount != 0 || report.PollAfterMs != 0 || probes.Load() != 3 || report.Items[0].SourceURL != items[1].SourceURL || report.Items[1].SourceURL != items[0].SourceURL || report.Items[2].SourceURL != items[2].SourceURL {
		t.Fatalf("strict native ranking or stable provider order failed: %+v probes=%d", report, probes.Load())
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "secret=") || strings.Contains(string(encoded), "cachefly.net") || strings.Contains(string(encoded), "private.invalid") {
		t.Fatal("private media or upstream diagnostics entered shelf proof")
	}
	requestCount := requests.Load()
	for i := 0; i < 10; i++ {
		if snapshot, _ := s.FawesomeShelfPlaybackStatus(initial.JobID); snapshot.StartedCount != 1 {
			t.Fatal("poll changed the completed proof")
		}
	}
	if requests.Load() != requestCount || probes.Load() != 3 {
		t.Fatal("status polling started network or a new check round")
	}
	report.Items[0].PlaybackCheck.PlaybackConfirmed = false
	report.Items[0].Title = "mutated"
	snapshot, _ := s.FawesomeShelfPlaybackStatus(initial.JobID)
	if snapshot.StartedCount != 1 || snapshot.Items[0].Title == "mutated" || s.discoveryCache["fawesome:popular"].report.Items[0].PlaybackCheck != nil {
		t.Fatal("response mutation changed manager proof or catalog cache")
	}
}

func TestFawesomeShelfDeduplicatesModesAndCancellationKeepsOtherView(t *testing.T) {
	items := shelfFixtureItems(1)
	entered, release, cancelled := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1)
	var probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(ctx context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return shelfStarted(region, country)
		case <-ctx.Done():
			cancelled <- struct{}{}
			return nil
		}
	})
	popular, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native fixture was not reached")
	}
	recent, err := s.StartFawesomeShelfPlayback(context.Background(), "recent")
	if err != nil {
		t.Fatal(err)
	}
	s.CancelFawesomeShelfPlayback(popular.JobID)
	select {
	case <-cancelled:
		t.Fatal("closing one view cancelled the same movie needed by another")
	default:
	}
	close(release)
	report := shelfWait(t, s, recent.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if probes.Load() != 1 || report.StartedCount != 1 {
		t.Fatal("the same page was sampled twice across modes")
	}
	reused, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil || reused.State != "complete" || reused.StartedCount != 1 || probes.Load() != 1 {
		t.Fatal("current country-bound proof was not reused safely")
	}
}

func TestFawesomeShelfSharesForegroundNativeSlotsAndCloseCancelsWaiting(t *testing.T) {
	items := shelfFixtureItems(1)
	var probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(_ context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		return shelfStarted(region, country)
	})
	s.playbackSlots <- struct{}{}
	s.playbackSlots <- struct{}{}
	job, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil {
		t.Fatal(err)
	}
	shelfWait(t, s, job.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.CheckingCount == 1 })
	if probes.Load() != 0 {
		t.Fatal("background sampler bypassed both occupied foreground slots")
	}
	<-s.playbackSlots
	report := shelfWait(t, s, job.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if report.StartedCount != 1 || probes.Load() != 1 || len(s.playbackSlots) != 1 {
		t.Fatal("native slot was double-acquired, leaked or bypassed")
	}
	<-s.playbackSlots

	// Expire the prior proof so the next explicit view needs one new attempt.
	s.fawesomeShelfMu.Lock()
	s.fawesomeShelf.entries[items[0].SourceURL].expiresAt = time.Now().Add(-time.Second)
	s.fawesomeShelfMu.Unlock()
	s.playbackSlots <- struct{}{}
	s.playbackSlots <- struct{}{}
	second, err := s.StartFawesomeShelfPlayback(context.Background(), "recent")
	if err != nil {
		t.Fatal(err)
	}
	shelfWait(t, s, second.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.CheckingCount == 1 })
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Scout.Close held the manager lock or failed to cancel a slot waiter")
	}
	<-s.playbackSlots
	<-s.playbackSlots
	if probes.Load() != 1 {
		t.Fatal("closing a waiting view started another native sample")
	}
	if _, err := s.StartFawesomeShelfPlayback(context.Background(), "popular"); err == nil {
		t.Fatal("closed Scout started new shelf work")
	}
}

func TestFawesomeShelfLeaseCancelsAbandonedViewWithoutRenewingRound(t *testing.T) {
	items := shelfFixtureItems(2)
	entered, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	var probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(ctx context.Context, _ VerifiedSource, _, _ string) *PlaybackCheck {
		probes.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	})
	job, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fixture did not reach native work")
	}
	s.fawesomeShelfMu.Lock()
	deadline := s.fawesomeShelf.jobs[job.JobID].deadline
	s.fawesomeShelf.jobs[job.JobID].lease = time.Now().Add(-time.Second)
	s.fawesomeShelfMu.Unlock()
	report, _ := s.FawesomeShelfPlaybackStatus(job.JobID)
	if report.State != "expired" || report.StartedCount != 0 || report.PollAfterMs != 0 {
		t.Fatal("late heartbeat revived an abandoned finite job")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("last expired view did not cancel active playback")
	}
	for i := 0; i < 5; i++ {
		s.FawesomeShelfPlaybackStatus(job.JobID)
	}
	s.fawesomeShelfMu.Lock()
	unchanged := s.fawesomeShelf.jobs[job.JobID].deadline.Equal(deadline)
	s.fawesomeShelfMu.Unlock()
	if probes.Load() != 1 || !unchanged {
		t.Fatal("expired status queued pending work or extended the check round")
	}
}

func TestFawesomeShelfProofExpiryCountryAndShieldChangesNeverAutoRetry(t *testing.T) {
	items := shelfFixtureItems(1)
	var probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(_ context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		return shelfStarted(region, country)
	})
	first, _ := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	shelfWait(t, s, first.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	s.fawesomeShelfMu.Lock()
	s.fawesomeShelf.entries[items[0].SourceURL].expiresAt = time.Now().Add(-time.Second)
	s.fawesomeShelfMu.Unlock()
	expired, _ := s.FawesomeShelfPlaybackStatus(first.JobID)
	if expired.StartedCount != 0 || expired.CheckedCount != 1 || expired.Items[0].PlaybackState != "expired" || expired.Items[0].PlaybackCheck.PlaybackConfirmed || probes.Load() != 1 {
		t.Fatal("expired native proof stayed green or requeued on polling")
	}
	second, _ := s.StartFawesomeShelfPlayback(context.Background(), "recent")
	shelfWait(t, s, second.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	old, _ := s.FawesomeShelfPlaybackStatus(first.JobID)
	if old.StartedCount != 0 || old.CheckedCount != 1 || old.Items[0].PlaybackState != "expired" || probes.Load() != 2 {
		t.Fatal("old finite job silently followed a new proof round")
	}
	s.countryMu.Lock()
	s.country, s.countryAt = "GB", time.Now()
	s.countryMu.Unlock()
	changed, _ := s.FawesomeShelfPlaybackStatus(second.JobID)
	if changed.StartedCount != 0 || changed.Items[0].PlaybackCheck.Country != "TZ" || probes.Load() != 2 {
		t.Fatal("a different known server country reused or relabelled old proof")
	}
	third, _ := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	shelfWait(t, s, third.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	s.shield.Engine().AddCustomRule("ads.shelf-fixture.invalid")
	changed, _ = s.FawesomeShelfPlaybackStatus(third.JobID)
	if changed.StartedCount != 0 || changed.Items[0].PlaybackCheck.PlaybackConfirmed || probes.Load() != 3 {
		t.Fatal("protection revision change kept old proof green or started new work")
	}
}

func TestFawesomeShelfRejectsPublicationAfterRulesChange(t *testing.T) {
	items := shelfFixtureItems(1)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	s, _ := shelfFixtureScout(t, items, func(ctx context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		entered <- struct{}{}
		select {
		case <-release:
			return shelfStarted(region, country)
		case <-ctx.Done():
			return nil
		}
	})
	job, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native fixture was not reached")
	}
	s.shield.Engine().AddCustomRule("changed.shelf-fixture.invalid")
	close(release)
	report := shelfWait(t, s, job.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if report.StartedCount != 0 || report.Items[0].PlaybackCheck.PlaybackConfirmed {
		t.Fatal("result captured under an old protection revision was published green")
	}
	s.fawesomeShelfMu.Lock()
	internal := *s.fawesomeShelf.entries[items[0].SourceURL].check
	s.fawesomeShelfMu.Unlock()
	if internal.Reason != "protection_changed" || internal.PlaybackConfirmed {
		t.Fatal("old-policy native result was cached as confirmed")
	}
}

func TestFawesomeShelfUnknownCountryDoesNotReuseCompletedProof(t *testing.T) {
	items := shelfFixtureItems(1)
	var probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(_ context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		return shelfStarted(region, country)
	})
	s.countryMu.Lock()
	s.country, s.countryAt = "", time.Time{}
	s.countryMu.Unlock()
	first, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil {
		t.Fatal(err)
	}
	firstDone := shelfWait(t, s, first.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if firstDone.Items[0].PlaybackCheck.Country != "" {
		t.Fatal("unknown server country was fabricated")
	}
	second, err := s.StartFawesomeShelfPlayback(context.Background(), "recent")
	if err != nil {
		t.Fatal(err)
	}
	shelfWait(t, s, second.JobID, func(r FawesomeShelfPlaybackReport) bool { return r.State == "complete" })
	if probes.Load() != 2 {
		t.Fatal("another view reused a proof without an observed country identity")
	}
}

func TestFawesomeShelfBoundsJobsURLsAndUsesOnlyFreshCachedCatalog(t *testing.T) {
	items := shelfFixtureItems(72)
	entered := make(chan struct{}, 1)
	s, requests := shelfFixtureScout(t, items, func(ctx context.Context, _ VerifiedSource, _, _ string) *PlaybackCheck {
		entered <- struct{}{}
		<-ctx.Done()
		return nil
	})
	shelfSetCatalog(s, "popular", items[:24])
	shelfSetCatalog(s, "recent", items[24:48])
	first, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil || first.Total != 24 {
		t.Fatal("first finite catalog batch was not bounded to 24")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fixture did not start")
	}
	if _, err := s.StartFawesomeShelfPlayback(context.Background(), "recent"); err != nil {
		t.Fatal(err)
	}
	shelfSetCatalog(s, "popular", items[48:])
	third, err := s.StartFawesomeShelfPlayback(context.Background(), "popular")
	if err != nil || third.StartedCount != 0 {
		t.Fatal("bounded extra view failed or invented a playback result")
	}
	s.fawesomeShelfMu.Lock()
	entryCount, queueCount := len(s.fawesomeShelf.entries), len(s.fawesomeShelf.queue)
	s.fawesomeShelfMu.Unlock()
	if entryCount != 48 || queueCount > 48 {
		t.Fatalf("shelf URL or queue capacity was exceeded: entries=%d queue=%d", entryCount, queueCount)
	}
	before := requests.Load()
	if _, err := s.StartFawesomeShelfPlayback(context.Background(), "other"); err == nil {
		t.Fatal("unknown catalog mode was accepted")
	}
	s.discoveryMu.Lock()
	stale := s.discoveryCache["fawesome:popular"]
	stale.at = time.Now().Add(-movieDiscoveryCacheTTL - time.Second)
	s.discoveryCache["fawesome:popular"] = stale
	s.discoveryMu.Unlock()
	if _, err := s.StartFawesomeShelfPlayback(context.Background(), "popular"); err == nil || requests.Load() != before {
		t.Fatal("stale catalog started a search or background discovery")
	}
}

func TestFawesomeShelfConcurrentViewsStaySingleWorkerAndFinite(t *testing.T) {
	items := shelfFixtureItems(1)
	var active, maximum, probes atomic.Int32
	s, _ := shelfFixtureScout(t, items, func(ctx context.Context, _ VerifiedSource, region, country string) *PlaybackCheck {
		probes.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
		}
		select {
		case <-time.After(10 * time.Millisecond):
			return shelfStarted(region, country)
		case <-ctx.Done():
			return nil
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := "popular"
			if i%2 != 0 {
				mode = "recent"
			}
			job, err := s.StartFawesomeShelfPlayback(context.Background(), mode)
			if err != nil { // Four active view jobs are the explicit bound.
				return
			}
			for poll := 0; poll < 5; poll++ {
				s.FawesomeShelfPlaybackStatus(job.JobID)
			}
			if i%3 == 0 {
				s.CancelFawesomeShelfPlayback(job.JobID)
			}
		}(i)
	}
	wg.Wait()
	s.Close()
	if maximum.Load() > 1 || probes.Load() > 1 || active.Load() != 0 {
		t.Fatalf("concurrent views created duplicate workers or leaked attempts: maximum=%d probes=%d active=%d", maximum.Load(), probes.Load(), active.Load())
	}
}
