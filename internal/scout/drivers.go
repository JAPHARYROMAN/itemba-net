package scout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
)

// UseDrivers gives the search its host drivers. The catalogs that used to be
// hand-written here (Apple's storefronts, Internet Archive, PeerTube,
// Wikimedia Commons, NASA+, the NFB, the public-domain feature catalogs,
// ArcoirisTV and Fawesome's guest search) are driver files under drivers/,
// shared with the crawler. Without drivers the search covers only the
// built-in reference catalog and the configured web provider.
func (s *Scout) UseDrivers(drivers []*crawler.Driver) {
	s.drivers = drivers
}

// driver returns the loaded driver of that name, or nil.
func (s *Scout) driver(name string) *crawler.Driver {
	for _, d := range s.drivers {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// driverFetcher builds a tier-one fetcher over the guarded availability
// transport, which refuses private and local destinations, so a driver can
// never be steered at the host's own network. Tests that swap either client
// reach the drivers through the same transport.
func (s *Scout) driverFetcher() *crawler.Fetcher {
	var transport http.RoundTripper
	if s.availabilityHTTPClient != nil && s.availabilityHTTPClient.Transport != nil {
		transport = s.availabilityHTTPClient.Transport
	} else if s.httpClient != nil {
		transport = s.httpClient.Transport
	}
	fetcher := crawler.NewFetcher(transport)
	// One burst must not let a refusal from one storefront silence the others.
	fetcher.DisableBackoff()
	return fetcher
}

type driverJob struct {
	driver *crawler.Driver
	market string
}

type driverResult struct {
	candidates []CandidateSource
	status     ProviderStatus
}

// driverSearch runs one driver's search in one market for the fan-out and
// returns its candidates with the provider status the console shows.
type driverSearch func(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, market string) ([]CandidateSource, ProviderStatus)

// queryDrivers runs every video driver for the query, storefront drivers
// once per market, and returns candidates with one provider status per
// search.
func (s *Scout) queryDrivers(ctx context.Context, query, region string) ([]CandidateSource, []ProviderStatus) {
	title, _ := movieQuery(query)
	if title == "" {
		title = query
	}
	return s.runDriverSearches(ctx, lens.TypeVideo, region, func(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, market string) ([]CandidateSource, ProviderStatus) {
		return s.queryDriver(ctx, fetcher, d, query, title, market)
	})
}

// runDriverSearches fans a search out to the drivers of one kind, storefront
// drivers once per market, and collects one provider status per search.
func (s *Scout) runDriverSearches(ctx context.Context, kind, region string, search driverSearch) ([]CandidateSource, []ProviderStatus) {
	if len(s.drivers) == 0 {
		return nil, nil
	}
	region = strings.ToUpper(strings.TrimSpace(region))
	fetcher := s.driverFetcher()

	var jobs []driverJob
	for _, d := range s.drivers {
		if d.Spec == nil || d.Kind != kind {
			// A driver that only checks pages has nothing to search, and a
			// driver of another kind lists other objects.
			continue
		}
		if !d.HasMarkets() {
			jobs = append(jobs, driverJob{d, ""})
			continue
		}
		markets := d.Markets()
		if region != "" && region != "ALL" {
			markets = []string{region}
		}
		for _, market := range markets {
			jobs = append(jobs, driverJob{d, market})
		}
	}

	// Storefront markets share one deadline and a fixed concurrency limit, so
	// adding storefront coverage cannot keep extending discovery's elapsed time.
	marketCtx, cancelMarkets := context.WithTimeout(ctx, 10*time.Second)
	defer cancelMarkets()
	marketSlots := make(chan struct{}, 4)
	results := make([]driverResult, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(idx int, j driverJob) {
			defer wg.Done()
			jobCtx := ctx
			if j.driver.HasMarkets() {
				jobCtx = marketCtx
				select {
				case marketSlots <- struct{}{}:
					defer func() { <-marketSlots }()
				case <-marketCtx.Done():
					results[idx].status = ProviderStatus{Name: j.driver.StatusName(j.market), Status: "error", Detail: "Search canceled or timed out."}
					return
				}
			}
			results[idx].candidates, results[idx].status = search(jobCtx, fetcher, j.driver, j.market)
		}(i, j)
	}
	wg.Wait()

	var candidates []CandidateSource
	statuses := make([]ProviderStatus, 0, len(results))
	for _, result := range results {
		candidates = append(candidates, result.candidates...)
		statuses = append(statuses, result.status)
	}
	return candidates, statuses
}

// queryDriver runs one driver search in one market and applies the search
// pipeline's rules: the relevance the driver asks for, the cap it asks for,
// and the Archive edition scoring and metadata resolution that precede
// verification.
func (s *Scout) queryDriver(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, query, title, market string) ([]CandidateSource, ProviderStatus) {
	status := ProviderStatus{Name: d.StatusName(market), Status: "ok"}
	listings, err := d.Search(ctx, fetcher, title, market)
	if err != nil {
		status.Status, status.Detail = "error", "Provider unavailable; results may be incomplete."
		return nil, status
	}
	_, requestedYear := movieQuery(query)
	candidates := make([]CandidateSource, 0, len(listings))
	tags := make(map[string][]string, len(listings))
	for _, listing := range listings {
		candidate := candidateFromListing(d, listing)
		switch d.Spec.RequireRelevance {
		case "score":
			// The requested film, not a trailer, a DVD menu or a compilation
			// that mentions it.
			if archiveNonMovieEdition(query, candidate.Title) || relevanceScore(query, candidate) == 0 {
				continue
			}
		case "exact":
			// Feature catalogs list the exact film, possibly with a release
			// label and marketing after it; the canonical title must match and
			// a known year must agree. A requested year stays a constraint on
			// the resolver when the listing names none.
			if !lens.SameFeatureTitle(query, candidate.Title) {
				continue
			}
			canonical, year := lens.TitleDetails(candidate.Title, candidate.ReleaseYear)
			if requestedYear != 0 && year != 0 && requestedYear != year {
				continue
			}
			if year == 0 {
				year = requestedYear
			}
			candidate.Title, candidate.ReleaseYear = canonical, year
		}
		tags[candidate.URL] = listing.Tags
		candidates = append(candidates, candidate)
	}
	if d.Spec.Keep > 0 && len(candidates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool {
			left, right := relevanceScore(query, candidates[i]), relevanceScore(query, candidates[j])
			if left != right {
				return left > right
			}
			return candidates[i].URL < candidates[j].URL
		})
		if len(candidates) > d.Spec.Keep {
			candidates = candidates[:d.Spec.Keep]
		}
	}
	if d.Name == "internet-archive" {
		candidates = s.resolveArchiveCandidates(ctx, archiveFeatureCandidates(query, candidates, tags))
	}
	status.Detail = fmt.Sprintf("%d catalog entries returned; regional access may vary", len(candidates))
	if len(candidates) == 0 {
		status.Status = "empty"
	}
	return candidates, status
}

// candidateFromListing is the bridge from a driver listing to the search
// pipeline's candidate. Driver IDs keep the prefixes the playback and
// resolution rules recognise (ia-, pt-, wm-, apple-, nasa-, nfb-).
func candidateFromListing(d *crawler.Driver, listing atlas.Listing) CandidateSource {
	id := listing.ID
	if id == "" {
		sum := sha256.Sum256([]byte(listing.URL))
		id = d.Name + "-" + hex.EncodeToString(sum[:6])
	}
	candidate := CandidateSource{
		ID:                id,
		Kind:              listing.Kind,
		Title:             listing.Title,
		Creator:           listing.Creator,
		License:           listing.License,
		URL:               listing.URL,
		DirectStreamURL:   listing.MediaURL,
		Provider:          listing.Provider,
		SourceType:        listing.SourceType,
		Description:       listing.Description,
		Thumbnail:         listing.Thumbnail,
		Duration:          listing.Duration,
		Resolution:        listing.Resolution,
		AccessType:        listing.AccessType,
		AccessNote:        listing.AccessNote,
		AccessBasis:       listing.AccessBasis,
		AccessEvidenceURL: listing.AccessEvidenceURL,
		Price:             listing.Price,
		Region:            listing.Market,
		ReleaseYear:       listing.Year,
		ListingOnly:       listing.ListingOnly,
	}
	if listing.MediaURL != "" {
		candidate.KnownManifests = []string{listing.MediaURL}
	}
	return candidate
}

// archiveFeatureCandidates keeps the Archive listings that are the requested
// feature rather than a trailer, menu or compilation, prefers full feature
// collections and known full runtimes, and caps the set that will spend
// metadata requests.
func archiveFeatureCandidates(query string, candidates []CandidateSource, tags map[string][]string) []CandidateSource {
	type match struct {
		source CandidateSource
		score  int
	}
	var matches []match
	for _, candidate := range candidates {
		if candidate.Title == "" || archiveNonMovieEdition(query, candidate.Title) {
			continue
		}
		score := relevanceScore(query, candidate)
		if score == 0 {
			continue
		}
		// Full feature collections improve coverage without excluding independent
		// community films. A known full runtime outranks otherwise equal shorts.
		for _, collection := range tags[candidate.URL] {
			switch strings.ToLower(collection) {
			case "feature_films", "moviesandfilms", "thevideocellarcollection":
				score += 15
			}
		}
		if mediaDurationSeconds(candidate.Duration) >= 40*60 {
			score += 10
		}
		matches = append(matches, match{candidate, score})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if len(matches) > maxArchiveMovieMetadata {
		matches = matches[:maxArchiveMovieMetadata]
	}
	out := make([]CandidateSource, 0, len(matches))
	for _, item := range matches {
		out = append(out, item.source)
	}
	return out
}

// resolveArchiveCandidates reads each kept item's public metadata so a
// listing becomes a playable source only when the Archive says which file
// holds the feature.
func (s *Scout) resolveArchiveCandidates(ctx context.Context, candidates []CandidateSource) []CandidateSource {
	if len(candidates) == 0 {
		return candidates
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	out := make([]CandidateSource, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, candidate CandidateSource) {
			defer wg.Done()
			out[index] = candidate
			page, err := url.Parse(candidate.URL)
			if err != nil {
				return
			}
			identifier := strings.TrimPrefix(page.Path, "/details/")
			if resolved, ok := resolveFreeArchiveSource(ctx, client, candidate, identifier, false); ok {
				out[index] = resolved
			}
		}(i, candidate)
	}
	wg.Wait()
	return out
}
