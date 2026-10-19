package scout

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// Title identity lives in internal/lens so Atlas, the console and scout
// agree on what a title is. These names remain so the provider code reads
// as before.
var movieYearPattern = lens.YearPattern

func movieQuery(query string) (string, int) { return lens.CanonicalQuery(query) }

func trimMovieSearchWords(query string) string { return lens.TrimSearchWords(query) }

func titleWords(value string) []string { return lens.Words(value) }

func substantiveTitleWords(value string) []string { return lens.SubstantiveWords(value) }

func movieTitleDetails(candidate CandidateSource) (string, int) {
	return lens.TitleDetails(candidate.Title, candidate.ReleaseYear)
}

func hasUnrequestedDerivative(query, candidate string) bool {
	return lens.HasUnrequestedDerivative(query, candidate)
}

var movieCreatorCreditPattern = regexp.MustCompile(`(?i)^.+?['’]s\s+`)

func relevanceScore(query string, candidate CandidateSource) int {
	title, requestedYear := movieQuery(query)
	words := substantiveTitleWords(title)
	if len(words) == 0 || hasUnrequestedDerivative(title, candidate.Title) {
		return 0
	}
	baseTitle, year := movieTitleDetails(candidate)
	if len(words) > 1 && candidate.SourceType == "Movie Store" && candidate.Provider == "Apple TV / iTunes" {
		// Store titles sometimes credit a creator, e.g. George A. Romero's Night of the Living Dead.
		// Only discard that prefix when the remaining title matches the entire requested work.
		creditless := movieCreatorCreditPattern.ReplaceAllString(baseTitle, "")
		if strings.Join(substantiveTitleWords(creditless), " ") == strings.Join(words, " ") {
			baseTitle = creditless
		}
	}
	candidateWords := substantiveTitleWords(baseTitle)
	if requestedYear != 0 && year != 0 && requestedYear != year {
		return 0
	}
	if len(candidateWords) == 0 {
		return 0
	}
	set := make(map[string]bool, len(candidateWords))
	for _, word := range candidateWords {
		set[word] = true
	}
	for _, word := range words {
		if !set[word] {
			return 0
		}
	}
	score := 80
	if strings.Join(words, " ") == strings.Join(candidateWords, " ") {
		score = 100
	} else if len(words) == 1 {
		// A one-word film title must not match every video mentioning that word.
		// A real subtitle is allowed, as in Nosferatu: A Symphony of Horror.
		mainTitle, _, hasSubtitle := strings.Cut(baseTitle, ":")
		mainWords := substantiveTitleWords(mainTitle)
		if !hasSubtitle || len(mainWords) != 1 || mainWords[0] != words[0] {
			return 0
		}
		score = 95
	} else {
		uniqueQuery := make(map[string]bool, len(words))
		for _, word := range words {
			uniqueQuery[word] = true
		}
		// Multiword keyword searches can tolerate small title variations, but
		// the requested work must account for most of the source's title.
		if len(uniqueQuery)*4 < len(set)*3 {
			return 0
		}
	}
	if requestedYear != 0 && year == 0 {
		score -= 15
	}
	return score
}

func mediaDurationSeconds(value string) float64 {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		return seconds
	}
	if duration, err := time.ParseDuration(strings.ReplaceAll(value, " ", "")); err == nil && duration > 0 {
		return duration.Seconds()
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	seconds := 0.0
	for i, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) || i > 0 && number >= 60 {
			return 0
		}
		seconds = seconds*60 + number
	}
	return seconds
}

func filterShortFeatureMatches(candidates []CandidateSource) []CandidateSource {
	// Only matching feature-store metadata or the built-in movie catalog can
	// establish the expected length. Built-in shorts also establish a threshold
	// so a DVD menu or mislabeled clip cannot displace the actual short film.
	// Use the shortest known edition so reasonable cuts are retained.
	expectedSeconds := 0.0
	for _, candidate := range candidates {
		seconds := mediaDurationSeconds(candidate.Duration)
		trusted := candidate.SourceType == "Movie Store" && candidate.Provider == "Apple TV / iTunes" && seconds >= 40*60 || strings.HasPrefix(candidate.ID, "ref-") && seconds >= 3*60
		if trusted && (expectedSeconds == 0 || seconds < expectedSeconds) {
			expectedSeconds = seconds
		}
	}
	if expectedSeconds == 0 {
		return candidates
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		seconds := mediaDurationSeconds(candidate.Duration)
		if seconds > 0 && seconds < expectedSeconds/2 {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func rankCandidates(query string, candidates []CandidateSource, direct bool) []CandidateSource {
	if !direct {
		candidates = deduplicateAppleStoreListings(candidates)
	}
	out := make([]CandidateSource, 0, len(candidates))
	seen := make(map[string]bool)
	for _, c := range candidates {
		u, err := url.Parse(c.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		key := strings.TrimRight(c.URL, "/") + "|" + c.Region
		if seen[key] || !direct && relevanceScore(query, c) == 0 {
			continue
		}
		seen[key] = true
		if c.AccessType == "" {
			c.AccessType = "unknown"
		}
		out = append(out, c)
	}
	if !direct {
		out = filterShortFeatureMatches(out)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !direct {
			a, b := watchOptionPriority(out[i]), watchOptionPriority(out[j])
			if a != b {
				return a < b
			}
		}
		a, b := relevanceScore(query, out[i]), relevanceScore(query, out[j])
		if a != b {
			return a > b
		}
		// Stable tie-breaking makes concurrent provider completion order irrelevant.
		return out[i].Title+out[i].Provider+out[i].Region+out[i].URL < out[j].Title+out[j].Provider+out[j].Region+out[j].URL
	})
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func (s *Scout) discoverSources(ctx context.Context, query, region string, direct bool) ([]CandidateSource, []ProviderStatus) {
	if direct {
		return s.discoverSeeds(ctx, query, true), []ProviderStatus{{Name: "Direct URL", Status: "ok", Detail: "Inspecting the supplied source."}}
	}
	candidates := matchReferenceCatalog(query)
	statuses := []ProviderStatus{{Name: "Open movie catalog", Status: "ok", Detail: fmt.Sprintf("%d matching reference films", len(candidates))}}
	candidates = append(candidates, creatorMovieCandidates(query)...)
	var mu sync.Mutex
	var wg sync.WaitGroup
	// Every catalog is a host driver now: the storefronts, the open archives,
	// the public-domain feature catalogs and Fawesome's guest search. The
	// built-in reference catalog, the Apple chart cache, the recently browsed
	// Fawesome shelf and the configured web provider remain beside them.
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, providers := s.queryDrivers(ctx, query, region)
		mu.Lock()
		candidates = append(candidates, items...)
		statuses = append(statuses, providers...)
		mu.Unlock()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, providers := s.discoverMovieSites(ctx, query, region)
		mu.Lock()
		candidates = append(candidates, items...)
		statuses = append(statuses, providers...)
		mu.Unlock()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		items, status := s.queryMovieChartListings(ctx, query, region)
		mu.Lock()
		candidates = append(candidates, items...)
		statuses = append(statuses, status)
		mu.Unlock()
	}()
	wg.Wait()
	if listings := s.fawesomeCatalogMovieCandidates(query); len(listings) > 0 {
		candidates = append(candidates, listings...)
		statuses = append(statuses, ProviderStatus{Name: "Fawesome browsed catalogs", Status: "ok", Detail: fmt.Sprintf("%d matching pages from recently fetched movie catalogs; availability and playback are checked afresh", len(listings))})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return candidates, statuses
}
