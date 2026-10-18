package scout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

const (
	fawesomeShelfProofTTL = 5 * time.Minute
	fawesomeShelfLease    = 30 * time.Second
	fawesomeShelfDeadline = 8 * time.Minute
	fawesomeShelfMaxURLs  = 48
	fawesomeShelfMaxJobs  = 4
)

type FawesomeShelfPlaybackReport struct {
	JobID             string               `json:"jobId"`
	Mode              string               `json:"mode"`
	Region            string               `json:"region"`
	AdBlockingEnabled bool                 `json:"adBlockingEnabled"`
	State             string               `json:"state"`
	Items             []MovieDiscoveryItem `json:"items"`
	Total             int                  `json:"total"`
	CheckedCount      int                  `json:"checkedCount"`
	StartedCount      int                  `json:"startedCount"`
	PendingCount      int                  `json:"pendingCount"`
	CheckingCount     int                  `json:"checkingCount"`
	CheckedAt         string               `json:"checkedAt"`
	ExpiresAt         string               `json:"expiresAt,omitempty"`
	PollAfterMs       int                  `json:"pollAfterMs"`
	Detail            string               `json:"detail"`
}

type fawesomeShelfPlaybackState struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	next    uint64
	jobs    map[string]*fawesomeShelfJob
	entries map[string]*fawesomeShelfEntry
	queue   []*fawesomeShelfEntry
	wake    chan struct{}
}

type fawesomeShelfJob struct {
	id, mode  string
	state     string
	items     []MovieDiscoveryItem
	versions  map[string]uint64
	completed map[string]bool
	createdAt time.Time
	updatedAt time.Time
	deadline  time.Time
	lease     time.Time
}

// Entries retain public page identity and safe proof only. Authorized main
// media URLs and bound CandidateSources exist solely inside one attempt.
type fawesomeShelfEntry struct {
	url, title string
	version    uint64
	refs       map[string]bool
	state      string
	cancel     context.CancelFunc
	abandoned  bool
	check      *PlaybackCheck
	checkedAt  time.Time
	expiresAt  time.Time
	country    string
	revision   uint64
	protection *shield.Shield
}

type fawesomeShelfEnvironment struct {
	country    string
	revision   uint64
	protection *shield.Shield
}

// Start is the only operation that can queue a finite round. It accepts no
// URLs: only exact primary pages from a still-fresh server catalog are used.
// A POST whose caller disappears is retired after its unrenewed 30-second
// lease. Status heartbeats cannot extend the fixed eight-minute round.
func (s *Scout) StartFawesomeShelfPlayback(ctx context.Context, mode string) (FawesomeShelfPlaybackReport, error) {
	if mode != "popular" && mode != "recent" {
		return FawesomeShelfPlaybackReport{}, errors.New("unsupported Fawesome shelf mode")
	}
	now := time.Now().UTC()
	s.discoveryMu.Lock()
	catalog, found := s.discoveryCache["fawesome:"+mode]
	var items []MovieDiscoveryItem
	if found && now.Sub(catalog.at) >= 0 && now.Sub(catalog.at) < fawesomeCatalogTTL(catalog.report) {
		seen := map[string]bool{}
		for _, original := range catalog.report.Items {
			if len(items) == movieDiscoveryLimit {
				break
			}
			if !fawesomeMovieTarget(CandidateSource{URL: original.SourceURL}) || strings.TrimSpace(original.Title) == "" || seen[original.SourceURL] {
				continue
			}
			item := cloneFawesomeShelfItem(original)
			item.AccessType, item.PlaybackState, item.PlaybackCheck, item.PlaybackExpiresAt = "free", "unchecked", nil, ""
			items = append(items, item)
			seen[item.SourceURL] = true
		}
	}
	s.discoveryMu.Unlock()
	if len(items) == 0 || ctx.Err() != nil {
		return FawesomeShelfPlaybackReport{}, errors.New("fresh Fawesome shelf is unavailable")
	}
	// Resolve the real server route before considering proof reuse. No country
	// supplied by a caller can change this connection or its cache identity.
	country, _ := s.availabilityCheckCountry(ctx)
	if ctx.Err() != nil {
		return FawesomeShelfPlaybackReport{}, errors.New("Fawesome shelf check was cancelled")
	}
	env := s.fawesomeShelfEnvironment()
	env.country = movieCountry(country)
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return FawesomeShelfPlaybackReport{}, errors.New("Fawesome shelf check could not be initialized")
	}
	id := hex.EncodeToString(entropy[:])
	now = time.Now().UTC()
	s.fawesomeShelfMu.Lock()
	state := s.fawesomeShelf
	if state == nil {
		state = s.newFawesomeShelfStateLocked()
	}
	if state.closed {
		s.fawesomeShelfMu.Unlock()
		return FawesomeShelfPlaybackReport{}, errors.New("Fawesome shelf checks are closed")
	}
	s.reapFawesomeShelfLocked(state, now)
	s.pruneFawesomeShelfLocked(state, now)
	if len(state.jobs) >= fawesomeShelfMaxJobs {
		s.fawesomeShelfMu.Unlock()
		return FawesomeShelfPlaybackReport{}, errors.New("Fawesome shelf check capacity is in use")
	}
	job := &fawesomeShelfJob{id: id, mode: mode, state: "running", items: items, versions: map[string]uint64{}, completed: map[string]bool{}, createdAt: now, updatedAt: now, deadline: now.Add(fawesomeShelfDeadline), lease: now.Add(fawesomeShelfLease)}
	state.jobs[id] = job
	for _, item := range items {
		entry := state.entries[item.SourceURL]
		if entry != nil && entry.state == "checked" && fawesomeShelfReusable(entry, env, now) {
			job.versions[item.SourceURL] = entry.version
			job.completed[item.SourceURL] = true
			continue
		}
		if entry != nil && !entry.abandoned && (entry.state == "queued" || entry.state == "checking") && entry.country == env.country && entry.revision == env.revision && entry.protection == env.protection {
			entry.refs[id] = true
			job.versions[item.SourceURL] = entry.version
			continue
		}
		if entry == nil && len(state.entries) >= fawesomeShelfMaxURLs {
			continue
		}
		// A new explicit view may replace an expired/environment-mismatched
		// attempt. Existing jobs keep their old version and never follow or
		// silently renew this new round on a later status request.
		if entry != nil && entry.cancel != nil {
			entry.cancel()
		}
		state.next++
		entry = &fawesomeShelfEntry{url: item.SourceURL, title: item.Title, version: state.next, refs: map[string]bool{id: true}, state: "queued", country: env.country, revision: env.revision, protection: env.protection}
		state.entries[item.SourceURL] = entry
		state.queue = append(state.queue, entry)
		job.versions[item.SourceURL] = entry.version
	}
	s.finishFawesomeShelfJobsLocked(state, now)
	report := s.fawesomeShelfSnapshotLocked(state, job, env, now)
	s.fawesomeShelfMu.Unlock()
	s.wakeFawesomeShelf(state)
	return report, nil
}

// Status is a bounded heartbeat for this existing finite view. It never
// fetches a catalog, searches, queues a URL, retries a failure or renews proof.
func (s *Scout) FawesomeShelfPlaybackStatus(id string) (FawesomeShelfPlaybackReport, bool) {
	env, now := s.fawesomeShelfEnvironment(), time.Now().UTC()
	s.fawesomeShelfMu.Lock()
	defer s.fawesomeShelfMu.Unlock()
	state := s.fawesomeShelf
	if state == nil {
		return FawesomeShelfPlaybackReport{}, false
	}
	s.reapFawesomeShelfLocked(state, now)
	job := state.jobs[id]
	if job == nil {
		return FawesomeShelfPlaybackReport{}, false
	}
	if job.state == "running" && !state.closed {
		job.lease = now.Add(fawesomeShelfLease)
		if job.lease.After(job.deadline) {
			job.lease = job.deadline
		}
	}
	return s.fawesomeShelfSnapshotLocked(state, job, env, now), true
}

func (s *Scout) CancelFawesomeShelfPlayback(id string) (FawesomeShelfPlaybackReport, bool) {
	env, now := s.fawesomeShelfEnvironment(), time.Now().UTC()
	s.fawesomeShelfMu.Lock()
	defer s.fawesomeShelfMu.Unlock()
	state := s.fawesomeShelf
	if state == nil || state.jobs[id] == nil {
		return FawesomeShelfPlaybackReport{}, false
	}
	job := state.jobs[id]
	job.state, job.updatedAt = "cancelled", now
	s.releaseFawesomeShelfRefsLocked(state, job)
	return s.fawesomeShelfSnapshotLocked(state, job, env, now), true
}

func (s *Scout) newFawesomeShelfStateLocked() *fawesomeShelfPlaybackState {
	ctx, cancel := context.WithCancel(context.Background())
	state := &fawesomeShelfPlaybackState{ctx: ctx, cancel: cancel, jobs: map[string]*fawesomeShelfJob{}, entries: map[string]*fawesomeShelfEntry{}, wake: make(chan struct{}, 1)}
	s.fawesomeShelf = state
	if s.playbackSlots == nil {
		s.playbackSlots = make(chan struct{}, maxAutomaticPlaybackChecks)
	}
	state.wg.Add(2)
	go s.runFawesomeShelfWorker(state)
	go func() {
		defer state.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.fawesomeShelfMu.Lock()
				s.reapFawesomeShelfLocked(state, now)
				s.fawesomeShelfMu.Unlock()
			}
		}
	}()
	return state
}

func (s *Scout) closeFawesomeShelfPlayback() {
	s.fawesomeShelfMu.Lock()
	state := s.fawesomeShelf
	if state == nil {
		s.fawesomeShelf = &fawesomeShelfPlaybackState{closed: true}
		s.fawesomeShelfMu.Unlock()
		return
	}
	state.closed = true
	if state.cancel != nil {
		state.cancel()
	}
	for _, job := range state.jobs {
		if job.state == "running" {
			job.state, job.updatedAt = "cancelled", time.Now().UTC()
			s.releaseFawesomeShelfRefsLocked(state, job)
		}
	}
	s.fawesomeShelfMu.Unlock()
	// A worker finishing cancellation also takes fawesomeShelfMu.
	state.wg.Wait()
}

func (s *Scout) fawesomeShelfEnvironment() fawesomeShelfEnvironment {
	s.countryMu.Lock()
	country := movieCountry(s.country)
	s.countryMu.Unlock()
	env := fawesomeShelfEnvironment{country: country, protection: s.shield}
	if env.protection != nil {
		env.revision = env.protection.Engine().Revision()
	}
	return env
}

func fawesomeShelfReusable(entry *fawesomeShelfEntry, env fawesomeShelfEnvironment, now time.Time) bool {
	// An unknown location can describe a new actual sample, but cannot reuse
	// a proof from another view without an observed route identity.
	return env.country != "" && entry.country == env.country && fawesomeShelfProofCurrent(entry, env, now)
}

func fawesomeShelfProofCurrent(entry *fawesomeShelfEntry, env fawesomeShelfEnvironment, now time.Time) bool {
	return entry.check != nil && !entry.checkedAt.After(now) && now.Before(entry.expiresAt) && entry.country == env.country && entry.revision == env.revision && entry.protection == env.protection
}

func (s *Scout) wakeFawesomeShelf(state *fawesomeShelfPlaybackState) {
	select {
	case state.wake <- struct{}{}:
	default:
	}
}

func (s *Scout) releaseFawesomeShelfRefsLocked(state *fawesomeShelfPlaybackState, job *fawesomeShelfJob) {
	for raw, version := range job.versions {
		entry := state.entries[raw]
		if entry == nil || entry.version != version {
			continue
		}
		delete(entry.refs, job.id)
		if len(entry.refs) == 0 && (entry.state == "queued" || entry.state == "checking") {
			entry.abandoned = true
			if entry.cancel != nil {
				entry.cancel()
			}
			if entry.state == "queued" {
				entry.state = "unchecked"
			}
		}
	}
}

func (s *Scout) reapFawesomeShelfLocked(state *fawesomeShelfPlaybackState, now time.Time) {
	for _, job := range state.jobs {
		if job.state == "running" && (!now.Before(job.lease) || !now.Before(job.deadline)) {
			job.state, job.updatedAt = "expired", now
			s.releaseFawesomeShelfRefsLocked(state, job)
		}
	}
}

func (s *Scout) pruneFawesomeShelfLocked(state *fawesomeShelfPlaybackState, now time.Time) {
	for id, job := range state.jobs {
		if job.state == "cancelled" || job.state == "expired" || !now.Before(job.deadline) {
			s.releaseFawesomeShelfRefsLocked(state, job)
			delete(state.jobs, id)
		}
	}
	for len(state.jobs) >= fawesomeShelfMaxJobs {
		var oldest *fawesomeShelfJob
		for _, job := range state.jobs {
			if job.state == "complete" && (oldest == nil || job.createdAt.Before(oldest.createdAt)) {
				oldest = job
			}
		}
		if oldest == nil {
			break
		}
		s.releaseFawesomeShelfRefsLocked(state, oldest)
		delete(state.jobs, oldest.id)
	}
	for raw, entry := range state.entries {
		if entry.state != "checking" && (entry.abandoned || entry.state == "unchecked" || entry.state == "checked" && !now.Before(entry.expiresAt)) {
			delete(state.entries, raw)
		}
	}
	// Discard stale queue pointers after cancellation or an explicit new round.
	queue := state.queue[:0]
	for _, entry := range state.queue {
		if state.entries[entry.url] == entry && entry.state == "queued" && !entry.abandoned {
			queue = append(queue, entry)
		}
	}
	state.queue = queue
}

func (s *Scout) finishFawesomeShelfJobsLocked(state *fawesomeShelfPlaybackState, now time.Time) {
	for _, job := range state.jobs {
		if job.state != "running" {
			continue
		}
		pending := false
		for raw, version := range job.versions {
			entry := state.entries[raw]
			pending = pending || entry != nil && entry.version == version && !entry.abandoned && (entry.state == "queued" || entry.state == "checking")
		}
		if !pending {
			job.state, job.updatedAt = "complete", now
			s.releaseFawesomeShelfRefsLocked(state, job)
		}
	}
}

func (s *Scout) runFawesomeShelfWorker(state *fawesomeShelfPlaybackState) {
	defer state.wg.Done()
	for {
		s.fawesomeShelfMu.Lock()
		s.reapFawesomeShelfLocked(state, time.Now().UTC())
		var entry *fawesomeShelfEntry
		for len(state.queue) > 0 && !state.closed {
			candidate := state.queue[0]
			state.queue = state.queue[1:]
			if state.entries[candidate.url] == candidate && candidate.state == "queued" && !candidate.abandoned && len(candidate.refs) > 0 {
				entry = candidate
				break
			}
		}
		if state.closed || state.ctx.Err() != nil {
			s.fawesomeShelfMu.Unlock()
			return
		}
		if entry == nil {
			s.fawesomeShelfMu.Unlock()
			select {
			case <-state.ctx.Done():
				return
			case <-state.wake:
				continue
			}
		}
		attemptCtx, cancel := context.WithCancel(state.ctx)
		entry.cancel, entry.state = cancel, "checking"
		s.fawesomeShelfMu.Unlock()

		check, checkedEnv := s.checkFawesomeShelfMovie(attemptCtx, entry.url, entry.title)
		current, now := s.fawesomeShelfEnvironment(), time.Now().UTC()
		cancel()
		s.fawesomeShelfMu.Lock()
		if state.entries[entry.url] == entry {
			entry.cancel = nil
			if entry.abandoned || state.closed || len(entry.refs) == 0 {
				entry.state = "unchecked"
			} else {
				check = safeFawesomeShelfCheck(check, entry.url, checkedEnv.country, now)
				if checkedEnv.protection != current.protection || checkedEnv.revision != current.revision {
					check.Status, check.Reason, check.PlaybackConfirmed = "unconfirmed", "protection_changed", false
					check.Detail = "Ad protection changed during this check. Playback remains unconfirmed under the current rules."
				} else if current.country != "" && current.country != checkedEnv.country {
					check.Status, check.Reason, check.PlaybackConfirmed = "unconfirmed", "location_changed", false
					check.Detail = "The observed server country changed during this check. Playback remains unconfirmed for the current connection."
				}
				entry.state, entry.check, entry.checkedAt, entry.expiresAt = "checked", check, now, now.Add(fawesomeShelfProofTTL)
				entry.country, entry.revision, entry.protection = checkedEnv.country, checkedEnv.revision, checkedEnv.protection
				for _, job := range state.jobs {
					if job.versions[entry.url] == entry.version {
						job.completed[entry.url], job.updatedAt = true, now
					}
				}
			}
		}
		s.finishFawesomeShelfJobsLocked(state, now)
		s.fawesomeShelfMu.Unlock()
	}
}

func (s *Scout) checkFawesomeShelfMovie(ctx context.Context, raw, title string) (*PlaybackCheck, fawesomeShelfEnvironment) {
	country, _ := s.availabilityCheckCountry(ctx)
	env := s.fawesomeShelfEnvironment()
	env.country = movieCountry(country)
	if ctx.Err() != nil {
		return nil, env
	}
	source := VerifiedSource{CandidateSource: CandidateSource{ID: resolvedFreeMovieID("fawesome-shelf", raw), URL: raw, Title: title, Provider: "Fawesome", SourceType: "Provider movie listing", AccessType: "free", ListingOnly: true}, VerificationStatus: "listing"}
	preflightCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	s.inspectProviderMoviePlayback(preflightCtx, &source, "ALL", env.country)
	preflightExpired := preflightCtx.Err() != nil
	cancel()
	if preflightExpired {
		return &PlaybackCheck{Status: "unconfirmed", Reason: "timeout", Method: "provider_playback_preflight"}, env
	}
	if source.PlaybackCheck != nil || !eligibleFreePlayback(source) || ctx.Err() != nil {
		return source.PlaybackCheck, env
	}
	// The one background worker shares the same two-slot semaphore as normal
	// foreground searches. It does not call the already-gated batch pipeline.
	select {
	case s.playbackSlots <- struct{}{}:
		defer func() { <-s.playbackSlots }()
	case <-ctx.Done():
		return nil, env
	}
	nativeCtx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	probe := s.playbackProbe
	if probe == nil {
		probe = s.probeFreeMoviePlayback
	}
	return probe(nativeCtx, source, "ALL", env.country), env
}

func safeFawesomeShelfCheck(original *PlaybackCheck, raw, country string, now time.Time) *PlaybackCheck {
	check := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight"}
	if original != nil {
		*check = *original
	}
	check.EvidenceURL, check.Country, check.RequestedRegion, check.CheckedAt = raw, movieCountry(country), "ALL", now.Format(time.RFC3339)
	if math.IsNaN(check.SecondsAdvanced) || math.IsInf(check.SecondsAdvanced, 0) {
		check.SecondsAdvanced = 0
	}
	if math.IsNaN(check.DurationSeconds) || math.IsInf(check.DurationSeconds, 0) {
		check.DurationSeconds = 0
	}
	// Native and provider details are reduced to fixed text. Never cache an
	// upstream diagnostic or a signed media URL in a public shelf response.
	if playbackStarted(VerifiedSource{CandidateSource: CandidateSource{AccessType: "free"}, PlaybackCheck: check}) && finiteFawesomeRuntime(check.DurationSeconds) {
		check.Detail = "A protected beginning sample decoded video frames and advanced normally on this server's connection. Full-film playback remains unconfirmed."
	} else {
		check.PlaybackConfirmed = false
		if check.Status == "started" {
			check.Status, check.Reason = "unconfirmed", "content_unconfirmed"
		}
		check.Detail = "A protected full-movie start was not confirmed for this server's connection. No account, country restriction or ad protection was bypassed."
	}
	return check
}

func (s *Scout) fawesomeShelfSnapshotLocked(state *fawesomeShelfPlaybackState, job *fawesomeShelfJob, env fawesomeShelfEnvironment, now time.Time) FawesomeShelfPlaybackReport {
	report := FawesomeShelfPlaybackReport{JobID: job.id, Mode: job.mode, Region: "ALL", AdBlockingEnabled: true, State: job.state, Items: []MovieDiscoveryItem{}, Total: len(job.items), CheckedAt: job.updatedAt.UTC().Format(time.RFC3339), ExpiresAt: job.deadline.UTC().Format(time.RFC3339), Detail: "Checks use this server's connection with ad blocking. A confirmed beginning sample can differ by country or later in the film."}
	for _, complete := range job.completed {
		if complete {
			report.CheckedCount++
		}
	}
	for _, original := range job.items {
		item := cloneFawesomeShelfItem(original)
		item.AccessType, item.PlaybackState, item.PlaybackCheck, item.PlaybackExpiresAt = "free", "unchecked", nil, ""
		entry := state.entries[item.SourceURL]
		version := job.versions[item.SourceURL]
		if entry != nil && version != 0 && entry.version == version {
			if !entry.expiresAt.IsZero() {
				item.PlaybackExpiresAt = entry.expiresAt.UTC().Format(time.RFC3339)
			}
			if entry.check != nil {
				check := *entry.check
				item.PlaybackCheck = &check
			}
			switch {
			case job.state == "cancelled":
				item.PlaybackState = "unchecked"
			case job.state == "expired" || entry.abandoned:
				item.PlaybackState = "expired"
			case entry.state == "queued":
				item.PlaybackState = "queued"
				report.PendingCount++
			case entry.state == "checking":
				item.PlaybackState = "checking"
				report.CheckingCount++
			case entry.state == "checked" && fawesomeShelfProofCurrent(entry, env, now):
				item.PlaybackState = "checked"
			default:
				if entry.check != nil {
					item.PlaybackState = "expired"
				}
			}
		} else if version != 0 {
			item.PlaybackState = "expired"
		}
		if item.PlaybackCheck != nil && item.PlaybackState != "checked" {
			item.PlaybackCheck.Status, item.PlaybackCheck.Reason, item.PlaybackCheck.PlaybackConfirmed = "expired", "expired", false
			item.PlaybackCheck.Detail = "This earlier check is no longer current for this view, connection or protection rules. Playback remains unconfirmed."
		}
		if fawesomeShelfItemStarted(item) {
			report.StartedCount++
		}
		report.Items = append(report.Items, item)
	}
	// Stable ordering retains provider order among every unconfirmed item.
	sort.SliceStable(report.Items, func(i, j int) bool {
		return fawesomeShelfItemStarted(report.Items[i]) && !fawesomeShelfItemStarted(report.Items[j])
	})
	if report.State == "running" {
		report.PollAfterMs = 2000
	}
	return report
}

func fawesomeShelfItemStarted(item MovieDiscoveryItem) bool {
	return item.PlaybackState == "checked" && item.PlaybackCheck != nil && finiteFawesomeRuntime(item.PlaybackCheck.DurationSeconds) && playbackStarted(VerifiedSource{CandidateSource: CandidateSource{AccessType: item.AccessType}, PlaybackCheck: item.PlaybackCheck})
}

func cloneFawesomeShelfItem(item MovieDiscoveryItem) MovieDiscoveryItem {
	item.Markets = append([]string{}, item.Markets...)
	if item.PlaybackCheck != nil {
		check := *item.PlaybackCheck
		item.PlaybackCheck = &check
	}
	return item
}
