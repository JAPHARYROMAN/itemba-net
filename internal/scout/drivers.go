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
)

// UseDrivers gives the search its host drivers. The JSON catalog providers
// that used to be hand-written here (Apple's storefronts, Internet Archive,
// PeerTube, Wikimedia Commons, NASA+ and the NFB) are driver files under
// drivers/, shared with the crawler. Without drivers the search covers only
// the built-in reference catalog, the HTML catalogs and the web provider.
func (s *Scout) UseDrivers(drivers []*crawler.Driver) {
	s.drivers = drivers
}

// driverFetcher builds a tier-one fetcher over the search's transport, so
// tests that swap the transport reach the drivers too.
func (s *Scout) driverFetcher() *crawler.Fetcher {
	var transport http.RoundTripper
	if s.httpClient != nil {
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

// queryDrivers runs every driver for the query, storefront drivers once per
// market, and returns candidates with one provider status per search.
func (s *Scout) queryDrivers(ctx context.Context, query, region string) ([]CandidateSource, []ProviderStatus) {
	if len(s.drivers) == 0 {
		return nil, nil
	}
	title, _ := movieQuery(query)
	if title == "" {
		title = query
	}
	region = strings.ToUpper(strings.TrimSpace(region))
	fetcher := s.driverFetcher()

	var jobs []driverJob
	for _, d := range s.drivers {
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
			results[idx].candidates, results[idx].status = s.queryDriver(jobCtx, fetcher, j.driver, query, title, j.market)
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
// pipeline's rules: relevance where the driver asks for it, and the Archive
// edition scoring and metadata resolution that precede verification.
func (s *Scout) queryDriver(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, query, title, market string) ([]CandidateSource, ProviderStatus) {
	status := ProviderStatus{Name: d.StatusName(market), Status: "ok"}
	listings, err := d.Search(ctx, fetcher, title, market)
	if err != nil {
		status.Status, status.Detail = "error", "Provider unavailable; results may be incomplete."
		return nil, status
	}
	candidates := make([]CandidateSource, 0, len(listings))
	tags := make(map[string][]string, len(listings))
	for _, listing := range listings {
		candidate := candidateFromListing(d, listing)
		if d.Spec.RequireRelevance && relevanceScore(query, candidate) == 0 {
			continue
		}
		tags[candidate.URL] = listing.Tags
		candidates = append(candidates, candidate)
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
		ID:              id,
		Title:           listing.Title,
		URL:             listing.URL,
		DirectStreamURL: listing.MediaURL,
		Provider:        listing.Provider,
		SourceType:      listing.SourceType,
		Description:     listing.Description,
		Thumbnail:       listing.Thumbnail,
		Duration:        listing.Duration,
		Resolution:      listing.Resolution,
		AccessType:      listing.AccessType,
		AccessNote:      listing.AccessNote,
		Price:           listing.Price,
		Region:          listing.Market,
		ReleaseYear:     listing.Year,
		ListingOnly:     listing.ListingOnly,
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
