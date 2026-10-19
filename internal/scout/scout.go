package scout

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/shield"
)

// SearchReport contains both the surviving verified sources and the pruned dead/fake links.
type SearchReport struct {
	Query string `json:"query"`
	// Kind is the object type the search was for: video, or audio since
	// Phase 3. Absent in reports recorded before kinds existed.
	Kind               string                     `json:"kind,omitempty"`
	TotalCandidates    int                        `json:"totalCandidates"`
	VerifiedCount      int                        `json:"verifiedCount"`
	PrunedCount        int                        `json:"prunedCount"`
	DurationMs         int64                      `json:"durationMs"`
	VerifiedSources    []VerifiedSource           `json:"verifiedSources"`
	PrunedSources      []VerifiedSource           `json:"prunedSources"`
	Sources            []VerifiedSource           `json:"sources"`
	ListedCount        int                        `json:"listedCount"`
	Region             string                     `json:"region"`
	FullLengthOnly     bool                       `json:"fullLengthOnly"`
	Providers          []ProviderStatus           `json:"providers"`
	AvailabilityChecks *AvailabilityCheckCoverage `json:"availabilityChecks,omitempty"`
}

type ProviderStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Scout is the Federated Discovery & Headless Stream Verifier engine.
type Scout struct {
	shield                 *shield.Shield
	localServerURL         string
	httpClient             *http.Client
	availabilityHTTPClient *http.Client
	playbackProbe          func(context.Context, VerifiedSource, string, string) *PlaybackCheck
	playbackSlots          chan struct{}

	poolMu          sync.Mutex
	poolBrowser     *cdp.BrowserInstance
	countryMu       sync.Mutex
	country         string
	countryAt       time.Time
	discoveryMu     sync.Mutex
	discoveryCache  map[string]cachedMovieDiscovery
	fawesomeShelfMu sync.Mutex
	fawesomeShelf   *fawesomeShelfPlaybackState
	// drivers are the host drivers the search fans out to; see UseDrivers.
	drivers []*crawler.Driver
}

// New creates a new Scout engine.
func New(sh *shield.Shield, localServerURL string) *Scout {
	s := &Scout{
		shield:         sh,
		localServerURL: strings.TrimRight(localServerURL, "/"),
		httpClient: &http.Client{
			Timeout: 5500 * time.Millisecond,
		},
		availabilityHTTPClient: newMovieAvailabilityClient(),
		playbackSlots:          make(chan struct{}, maxAutomaticPlaybackChecks),
	}
	s.playbackProbe = s.probeFreeMoviePlayback
	return s
}

// Close shuts down the shared headless verifier browser pool if active.
func (s *Scout) Close() {
	s.closeFawesomeShelfPlayback()
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if s.poolBrowser != nil {
		s.poolBrowser.Close()
		s.poolBrowser = nil
	}
}

func (s *Scout) getPoolBrowser(ctx context.Context) *cdp.BrowserInstance {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()

	if s.poolBrowser != nil && !s.poolBrowser.IsClosed() {
		return s.poolBrowser
	}

	launchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	b, err := cdp.Launch(launchCtx, cdp.LaunchOptions{
		ID:           "scout-pool",
		Width:        1280,
		Height:       720,
		MuteAudio:    true,
		HostResolver: s.shield.Engine().HostResolverRules(),
	})
	if err != nil {
		return nil
	}
	s.poolBrowser = b
	return b
}

// Search performs Federated Seed Discovery across real open-web providers (Internet Archive, PeerTube, Wikimedia, Reference HLS)
// or deep-inspects a direct URL via Headless CDP.
func (s *Scout) Search(ctx context.Context, query string) SearchReport {
	return s.SearchWithRegion(ctx, query, "ALL")
}

func (s *Scout) SearchWithRegion(ctx context.Context, query, region string) SearchReport {
	return s.SearchWithOptions(ctx, query, region, SearchOptions{})
}

type SearchOptions struct {
	FullLengthOnly bool
	// Kind is the object type to search for: video, the default, or audio.
	// An audio search runs the audio pipeline (see searchAudio); anything
	// else runs the movie pipeline below.
	Kind string
}

func (s *Scout) SearchWithOptions(ctx context.Context, query, region string, options SearchOptions) SearchReport {
	start := time.Now()
	query = strings.TrimSpace(query)
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "ALL"
	}
	kind := strings.ToLower(strings.TrimSpace(options.Kind))
	if kind != lens.TypeAudio {
		kind = lens.TypeVideo
	}
	empty := SearchReport{Query: query, Kind: kind, Region: region, FullLengthOnly: options.FullLengthOnly, Sources: []VerifiedSource{}, VerifiedSources: []VerifiedSource{}, PrunedSources: []VerifiedSource{}, Providers: []ProviderStatus{}}
	if query == "" {
		return empty
	}
	if kind == lens.TypeAudio {
		return s.searchAudio(ctx, query, region, start)
	}

	isDirectURL := strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://")
	providerInput, isProviderMovieURL := s.providerMovieURLInput(query, region)
	if isDirectURL {
		// An explicit URL is an inspection request, even when the search form
		// currently prefers features. Echo the effective filter accurately.
		options.FullLengthOnly = false
	}
	type checkLocation struct{ country, detail string }
	locationResult := make(chan checkLocation, 1)
	if !isDirectURL || isProviderMovieURL {
		go func() {
			country, detail := s.availabilityCheckCountry(ctx)
			locationResult <- checkLocation{country, detail}
		}()
	}
	candidates, providers := s.discoverSources(ctx, query, region, isDirectURL)
	if isProviderMovieURL {
		candidates = []CandidateSource{providerInput}
	}
	if !isDirectURL {
		// Enrich only relevant candidates. Public catalog metadata can expose
		// playable free media without executing a provider's page scripts.
		relevant := candidates[:0]
		for _, candidate := range candidates {
			if relevanceScore(query, candidate) > 0 && (!options.FullLengthOnly || !knownShortMovie(candidate.Duration, 0)) {
				relevant = append(relevant, candidate)
			}
		}
		candidates = relevant
		sort.SliceStable(candidates, func(i, j int) bool {
			if a, b := relevanceScore(query, candidates[i]), relevanceScore(query, candidates[j]); a != b {
				return a > b
			}
			return candidates[i].URL < candidates[j].URL
		})
		var freeCoverage ProviderStatus
		candidates, freeCoverage = s.resolveFreeMovieSources(ctx, candidates)
		providers = append(providers, freeCoverage)
		var offerCoverage ProviderStatus
		candidates, offerCoverage = s.confirmDirectWatchOffers(ctx, candidates)
		providers = append(providers, offerCoverage)
		if options.FullLengthOnly {
			featureCandidates := candidates[:0]
			for _, candidate := range candidates {
				if !knownShortMovie(candidate.Duration, 0) {
					featureCandidates = append(featureCandidates, candidate)
				}
			}
			candidates = featureCandidates
		}
		candidates = prepareMovieWatchOptions(query, region, candidates)
	}
	candidates = rankCandidates(query, candidates, isDirectURL)
	location := checkLocation{}
	if !isDirectURL || isProviderMovieURL {
		select {
		case location = <-locationResult:
		case <-ctx.Done():
			location.detail = "Check location could not be confirmed before the search deadline."
		}
	}
	availabilityClient := s.availabilityHTTPClient
	if availabilityClient == nil {
		availabilityClient = newMovieAvailabilityClient()
	}
	pageChecks := make(map[int]bool)
	if !isDirectURL {
		for i, candidate := range candidates {
			if candidate.ListingOnly && len(pageChecks) < maxAutomaticPageChecks {
				pageChecks[i] = true
			}
		}
	}

	results := make([]VerifiedSource, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	var poolBrowser *cdp.BrowserInstance
	if isDirectURL && len(candidates) > 0 && candidates[0].DirectStreamURL == "" && !candidates[0].ListingOnly {
		poolBrowser = s.getPoolBrowser(ctx)
	}

	for i, cand := range candidates {
		wg.Add(1)
		go func(idx int, c CandidateSource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if c.ListingOnly {
				results[idx] = VerifiedSource{CandidateSource: c, DetectedManifests: []string{}, VerificationStatus: "listing", VerificationMode: "Provider listing", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
				if !isDirectURL {
					if pageChecks[idx] && ctx.Err() == nil {
						checkStart := time.Now()
						check := checkMoviePageAvailability(ctx, availabilityClient, c, region, location.country)
						results[idx].AvailabilityCheck = &check
						results[idx].VerifyDurationMs = time.Since(checkStart).Milliseconds()
						if check.Status == "page_reachable" {
							results[idx].VerificationStatus = "page_reachable"
						}
					} else {
						results[idx].AvailabilityCheck = uncheckedMovieAvailability(region, location.country, "Not checked within this search's bounded page-check budget. Open the movie page to check access.")
					}
				}
			} else if isDirectURL && c.DirectStreamURL == "" || c.SourceType == "Security Benchmark" {
				pb := poolBrowser
				if pb == nil {
					pb = s.getPoolBrowser(ctx)
				}
				results[idx] = VerifyWithCDP(ctx, pb, s.shield, c)
			} else {
				client := s.httpClient
				if !isDirectURL {
					client = availabilityClient
				}
				results[idx] = verifyFastHTTPWithClient(ctx, s.shield, c, client)
				if !isDirectURL {
					results[idx].AvailabilityCheck = mediaAvailabilityResult(results[idx], region, location.country)
				}
			}
			results[idx].RelevanceScore = relevanceScore(query, c)
			if isDirectURL {
				results[idx].RelevanceScore = 100
			}
			if results[idx].CheckedAt == "" {
				results[idx].CheckedAt = time.Now().UTC().Format(time.RFC3339)
			}
			if results[idx].VerificationStatus == "" {
				results[idx].VerificationStatus = "unavailable"
				if results[idx].Verified {
					results[idx].VerificationStatus = "page_reachable"
				}
			}
		}(i, cand)
	}
	wg.Wait()
	if !isDirectURL || isProviderMovieURL {
		s.checkFreeMoviePlayback(ctx, results, region, location.country)
	}

	verified := []VerifiedSource{}
	pruned := []VerifiedSource{}
	sources := []VerifiedSource{}
	for _, r := range results {
		observedDuration := 0.0
		if r.PlaybackCheck != nil {
			observedDuration = r.PlaybackCheck.DurationSeconds
		}
		if !isDirectURL && options.FullLengthOnly && knownShortMovie(r.Duration, observedDuration) {
			r.Verified, r.VerificationStatus = false, "unavailable"
			r.PrunedReason = "The listed or sampled video is shorter than the 40-minute full-length preference."
			pruned = append(pruned, r)
			continue
		}
		// Static reference streams that failed their check are not watch options.
		if !isDirectURL && strings.HasPrefix(r.ID, "ref-") && !r.Verified {
			pruned = append(pruned, r)
			continue
		}
		if r.Verified {
			verified = append(verified, r)
		}
		if r.VerificationStatus != "unavailable" {
			sources = append(sources, r)
		} else if !isDirectURL && r.SourceType != "Security Benchmark" {
			// A failed media probe does not prove that the provider's content page is unavailable.
			r.VerificationStatus = "listing"
			r.VerificationMode = "Listing; access check failed"
			r.AccessNote = strings.TrimSpace(r.AccessNote + " Media check: " + r.PrunedReason + ". Open the source to check access.")
			sources = append(sources, r)
		} else {
			pruned = append(pruned, r)
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		a, b := checkedWatchPriority(sources[i], region), checkedWatchPriority(sources[j], region)
		if a != b {
			return a < b
		}
		if sources[i].Verified != sources[j].Verified {
			return sources[i].Verified
		}
		return sources[i].RelevanceScore > sources[j].RelevanceScore
	})

	// Sort verified sources: prioritize sources with direct playable .m3u8/.mp4 streams, then Cleanliness Score
	sort.SliceStable(verified, func(i, j int) bool {
		if !isDirectURL {
			a, b := checkedWatchPriority(verified[i], region), checkedWatchPriority(verified[j], region)
			if a != b {
				return a < b
			}
		}
		hasDirectI := verified[i].DirectStreamURL != ""
		hasDirectJ := verified[j].DirectStreamURL != ""
		if hasDirectI != hasDirectJ {
			return hasDirectI
		}
		if verified[i].CleanlinessScore != verified[j].CleanlinessScore {
			return verified[i].CleanlinessScore > verified[j].CleanlinessScore
		}
		return verified[i].VerifyDurationMs < verified[j].VerifyDurationMs
	})

	report := SearchReport{
		Query:           query,
		Kind:            lens.TypeVideo,
		TotalCandidates: len(candidates),
		VerifiedCount:   len(verified),
		PrunedCount:     len(pruned),
		DurationMs:      time.Since(start).Milliseconds(),
		VerifiedSources: verified,
		PrunedSources:   pruned,
		Sources:         sources,
		ListedCount:     len(sources),
		Region:          region,
		FullLengthOnly:  options.FullLengthOnly,
		Providers:       providers,
	}
	if !isDirectURL || isProviderMovieURL {
		report.AvailabilityChecks = movieAvailabilityCoverage(region, location.country, location.detail, sources)
	}
	return report
}

// RunSecurityBenchmark runs a real Headless Chromium CDP verification pass against the built-in adversarial ad-trap
// and fake-registration-wall testbenches to benchmark all 3 layers of itemba-shield.
func (s *Scout) RunSecurityBenchmark(ctx context.Context) SearchReport {
	start := time.Now()
	pb := s.getPoolBrowser(ctx)

	candidates := []CandidateSource{
		{
			ID:              "bench-sketchy-1",
			Title:           "Adversarial Streaming Host Benchmark (3-Layer Ad & Clickjack Trap)",
			URL:             fmt.Sprintf("%s/testbench/sketchy-cinema?q=Security+Benchmark", s.localServerURL),
			DirectStreamURL: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
			Provider:        "ITEMBA Adversarial Suite",
			SourceType:      "Security Benchmark",
			Description:     "Fires live Layer 1 DNS ad lookups (doubleclick/popads), Layer 2 VAST/popunder XHRs, and Layer 3 z-index:999999 clickjack overlays + window.open() hijacks.",
			Resolution:      "1080p HD",
		},
		{
			ID:          "bench-fakewall-1",
			Title:       "Deceptive SEO Fake-Play Registration Wall Benchmark",
			URL:         fmt.Sprintf("%s/testbench/fake-wall?q=Security+Benchmark", s.localServerURL),
			Provider:    "ITEMBA Adversarial Suite",
			SourceType:  "Security Benchmark",
			Description: "Tests Headless CDP dead-link pruning against deceptive landing pages with fake Play buttons and zero <video> or .m3u8 manifests.",
			Resolution:  "Fake 4K",
		},
	}

	verified := []VerifiedSource{}
	pruned := []VerifiedSource{}
	sources := []VerifiedSource{}
	for _, c := range candidates {
		v := VerifyWithCDP(ctx, pb, s.shield, c)
		if v.Verified {
			verified = append(verified, v)
		}
		if v.VerificationStatus != "unavailable" {
			sources = append(sources, v)
		} else {
			pruned = append(pruned, v)
		}
	}

	return SearchReport{
		Query:           "Security & Ad-Guillotine Benchmark",
		TotalCandidates: len(candidates),
		VerifiedCount:   len(verified),
		PrunedCount:     len(pruned),
		DurationMs:      time.Since(start).Milliseconds(),
		VerifiedSources: verified,
		PrunedSources:   pruned,
		Sources:         sources,
		ListedCount:     len(sources),
	}
}

func (s *Scout) discoverSeeds(ctx context.Context, query string, isDirectURL bool) []CandidateSource {
	if isDirectURL {
		parsed, _ := url.Parse(query)
		lower := ""
		if parsed != nil {
			lower = strings.ToLower(parsed.Path)
		}
		directStream := ""
		if strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".webm") || strings.HasSuffix(lower, ".mpd") || strings.HasSuffix(lower, ".ogv") {
			directStream = query
		}
		return []CandidateSource{
			{
				ID:              "direct-target-1",
				Title:           "Direct Target Inspection: " + query,
				URL:             query,
				DirectStreamURL: directStream,
				Provider:        "Direct URL Inspector",
				SourceType:      "Open Web",
				Description:     "Live Headless Chromium CDP target inspection with Triple-Layer Shield & stream manifest extraction.",
				Resolution:      "Adaptive",
			},
		}
	}

	candidates, _ := s.discoverSources(ctx, query, "ALL", false)
	return candidates
}

func matchReferenceCatalog(query string) []CandidateSource {
	catalog := []CandidateSource{
		{
			ID:              "ref-tears-of-steel",
			Title:           "Tears of Steel (Blender Open Movie — Sci-Fi Short)",
			URL:             "https://mango.blender.org/",
			DirectStreamURL: "https://demo.unified-streaming.com/k8s/features/stable/video/tears-of-steel/tears-of-steel.ism/.m3u8",
			Provider:        "Blender Foundation / Unified Streaming CDN",
			SourceType:      "HLS Reference",
			Description:     "Multi-bitrate 1080p adaptive HLS (.m3u8) stream recorded in Amsterdam by the Blender Institute.",
			Thumbnail:       "https://upload.wikimedia.org/wikipedia/commons/thumb/7/70/Tos-poster.png/320px-Tos-poster.png",
			Duration:        "12:14",
			Resolution:      "1080p FHD",
			KnownManifests:  []string{"https://demo.unified-streaming.com/k8s/features/stable/video/tears-of-steel/tears-of-steel.ism/.m3u8"},
		},
		{
			ID:              "ref-big-buck-bunny",
			Title:           "Big Buck Bunny (1080p60 Adaptive HLS Stream)",
			URL:             "https://peach.blender.org/",
			DirectStreamURL: "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
			Provider:        "Mux / Blender Open Movie",
			SourceType:      "HLS Reference",
			Description:     "High-bitrate 1080p HLS (.m3u8) master playlist ideal for testing zero-trust HLS manifest rewriting and 60fps playback.",
			Thumbnail:       "https://upload.wikimedia.org/wikipedia/commons/thumb/c/c5/Big_buck_bunny_poster_big.jpg/320px-Big_buck_bunny_poster_big.jpg",
			Duration:        "09:56",
			Resolution:      "1080p FHD",
			KnownManifests:  []string{"https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8"},
		},
		{
			ID:              "ref-sintel",
			Title:           "Sintel (Blender Open Movie — 1080p HLS Master)",
			URL:             "https://durian.blender.org/",
			DirectStreamURL: "https://bitdash-a.akamaihd.net/content/sintel/hls/playlist.m3u8",
			Provider:        "Akamai / Blender Foundation",
			SourceType:      "HLS Reference",
			Description:     "4K/1080p adaptive HLS (.m3u8) fantasy short film with multi-track surround audio.",
			Thumbnail:       "https://upload.wikimedia.org/wikipedia/commons/thumb/8/8f/Sintel_poster.jpg/320px-Sintel_poster.jpg",
			Duration:        "14:48",
			Resolution:      "1080p FHD",
			KnownManifests:  []string{"https://bitdash-a.akamaihd.net/content/sintel/hls/playlist.m3u8"},
		},
		{
			ID:              "ref-notld-1968",
			Title:           "Night of the Living Dead (1968 — George A. Romero Feature Film)",
			URL:             "https://archive.org/embed/night_of_the_living_dead",
			DirectStreamURL: "https://archive.org/download/night_of_the_living_dead/night_of_the_living_dead_512kb.mp4",
			Provider:        "Internet Archive Cinema",
			SourceType:      "Public Domain",
			Description:     "Full-length 1968 cult classic feature film in the public domain with verified direct MP4 and sandbox embed streams.",
			Thumbnail:       "https://archive.org/services/img/night_of_the_living_dead",
			Duration:        "1:35:52",
			Resolution:      "720p HD",
			KnownManifests:  []string{"https://archive.org/download/night_of_the_living_dead/night_of_the_living_dead_512kb.mp4"},
		},
		{
			ID:              "ref-nosferatu-1922",
			Title:           "Nosferatu: A Symphony of Horror (1922 — F.W. Murnau)",
			URL:             "https://archive.org/embed/nosferatu_1922",
			DirectStreamURL: "https://archive.org/download/nosferatu_1922/nosferatu_512kb.mp4",
			Provider:        "Internet Archive Cinema",
			SourceType:      "Public Domain",
			Description:     "Landmark 1922 German Expressionist vampire film with verified direct MP4 stream.",
			Thumbnail:       "https://archive.org/services/img/nosferatu_1922",
			Duration:        "1:21:00",
			Resolution:      "720p HD",
			KnownManifests:  []string{"https://archive.org/download/nosferatu_1922/nosferatu_512kb.mp4"},
		},
		{
			ID:              "ref-charade-1963",
			Title:           "Charade (1963 — Cary Grant & Audrey Hepburn)",
			URL:             "https://archive.org/embed/Charade1963",
			DirectStreamURL: "https://archive.org/download/Charade1963/Charade1963_512kb.mp4",
			Provider:        "Internet Archive Cinema",
			SourceType:      "Public Domain",
			Description:     "Classic 1963 romantic suspense thriller directed by Stanley Donen, available in public domain.",
			Thumbnail:       "https://archive.org/services/img/Charade1963",
			Duration:        "1:53:25",
			Resolution:      "720p HD",
			KnownManifests:  []string{"https://archive.org/download/Charade1963/Charade1963_512kb.mp4"},
		},
	}

	var matched []CandidateSource
	for _, item := range catalog {
		years := map[string]int{"ref-tears-of-steel": 2012, "ref-big-buck-bunny": 2008, "ref-sintel": 2010, "ref-notld-1968": 1968, "ref-nosferatu-1922": 1922, "ref-charade-1963": 1963}
		item.ReleaseYear = years[item.ID]
		item.AccessType = "free"
		item.AccessNote = "Listed free access; regional availability may vary."
		if relevanceScore(query, item) > 0 {
			matched = append(matched, item)
		}
	}
	return matched
}
