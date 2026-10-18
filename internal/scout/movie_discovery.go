package scout

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	movieDiscoveryLimit    = 24
	movieDiscoveryDeadline = 8 * time.Second
	movieDiscoveryCacheTTL = 15 * time.Minute
)

type MovieDiscoveryItem struct {
	ID                string         `json:"id"`
	Title             string         `json:"title"`
	ReleaseYear       int            `json:"releaseYear,omitempty"`
	ReleaseDate       string         `json:"releaseDate,omitempty"`
	CatalogDate       string         `json:"catalogDate,omitempty"`
	Query             string         `json:"query"`
	Thumbnail         string         `json:"thumbnail,omitempty"`
	SourceURL         string         `json:"sourceUrl"`
	SourceName        string         `json:"sourceName"`
	Markets           []string       `json:"markets"`
	AccessType        string         `json:"accessType,omitempty"`
	PlaybackState     string         `json:"playbackState,omitempty"`
	PlaybackCheck     *PlaybackCheck `json:"playbackCheck,omitempty"`
	PlaybackExpiresAt string         `json:"playbackExpiresAt,omitempty"`
	chartScore        float64
}

type MovieDiscoveryReport struct {
	Mode      string               `json:"mode"`
	Region    string               `json:"region"`
	Items     []MovieDiscoveryItem `json:"items"`
	Providers []ProviderStatus     `json:"providers"`
	CheckedAt string               `json:"checkedAt"`
	Cached    bool                 `json:"cached"`
	Detail    string               `json:"detail"`
}

type cachedMovieDiscovery struct {
	report MovieDiscoveryReport
	at     time.Time
}

type appleChartLabel struct {
	Label string `json:"label"`
}

type appleChartEntry struct {
	Name        appleChartLabel `json:"im:name"`
	ReleaseDate appleChartLabel `json:"im:releaseDate"`
	ContentType struct {
		Attributes struct {
			Term string `json:"term"`
		} `json:"attributes"`
	} `json:"im:contentType"`
	ID struct {
		Label      string `json:"label"`
		Attributes struct {
			ID string `json:"im:id"`
		} `json:"attributes"`
	} `json:"id"`
	Images []struct {
		Label      string `json:"label"`
		Attributes struct {
			Height string `json:"height"`
		} `json:"attributes"`
	} `json:"im:image"`
}

// DiscoverMovies reads the live, keyless Apple movie chart. It suggests search
// titles only; chart presence never establishes free access or playback.
// The current official RSS builder is https://rss.marketingtools.apple.com/.
// The legacy movie feed and its im:name/im:releaseDate/im:id contract were
// verified at https://itunes.apple.com/us/rss/topmovies/limit=25/json on
// 2026-10-18. The provider JSON reader bounds each response to 2 MiB/6 seconds;
// all markets share one 8-second deadline and at most four concurrent calls.
func (s *Scout) DiscoverMovies(ctx context.Context, mode, region string) MovieDiscoveryReport {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "recent" {
		mode = "popular"
	}
	region = movieCountry(region)
	if region == "" {
		region = "ALL"
	}
	now := time.Now().UTC()
	s.discoveryMu.Lock()
	cached, found := s.discoveryCache[region]
	if found {
		if now.Sub(cached.at) < movieDiscoveryTTL(cached.report) {
			report := projectMovieDiscovery(cached.report, mode, now)
			report.Cached = true
			s.discoveryMu.Unlock()
			return report
		}
	}
	s.discoveryMu.Unlock()
	markets := []string{region}
	if region == "ALL" {
		markets = []string{"US", "GB", "IN", "ZA", "DE", "FR", "JP", "AU"}
	}
	type marketResult struct {
		items  []MovieDiscoveryItem
		status ProviderStatus
	}
	results := make([]marketResult, len(markets))
	chartCtx, cancel := context.WithTimeout(ctx, movieDiscoveryDeadline)
	defer cancel()
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, market := range markets {
		wg.Add(1)
		go func(index int, market string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-chartCtx.Done():
				results[index].status = ProviderStatus{Name: "Apple movie chart (" + market + ")", Status: "error", Detail: "Chart request cancelled or timed out."}
				return
			}
			results[index].items, results[index].status = s.fetchMovieChart(chartCtx, market)
		}(i, market)
	}
	wg.Wait()
	base := MovieDiscoveryReport{Region: region, Items: []MovieDiscoveryItem{}, Providers: []ProviderStatus{}, CheckedAt: now.Format(time.RFC3339)}
	byID, byWork := map[string]int{}, map[string]int{}
	for _, result := range results {
		base.Providers = append(base.Providers, result.status)
		for _, item := range result.items {
			work := ""
			if item.ReleaseYear > 0 {
				work = strings.Join(titleWords(item.Title), " ") + "|" + strconv.Itoa(item.ReleaseYear)
			}
			index, exists := byID[item.ID]
			if !exists && work != "" {
				index, exists = byWork[work]
			}
			if exists {
				current := &base.Items[index]
				for _, market := range item.Markets {
					present := false
					for _, known := range current.Markets {
						present = present || known == market
					}
					if !present {
						current.Markets = append(current.Markets, market)
						current.chartScore += item.chartScore
					}
				}
				byID[item.ID] = index
				continue
			}
			byID[item.ID] = len(base.Items)
			if work != "" {
				byWork[work] = len(base.Items)
			}
			base.Items = append(base.Items, item)
		}
	}
	if len(base.Items) > 0 {
		s.discoveryMu.Lock()
		if s.discoveryCache == nil {
			s.discoveryCache = make(map[string]cachedMovieDiscovery)
		}
		s.discoveryCache[region] = cachedMovieDiscovery{report: base, at: now}
		s.discoveryMu.Unlock()
	}
	return projectMovieDiscovery(base, mode, now)
}

func movieDiscoveryTTL(report MovieDiscoveryReport) time.Duration {
	for _, provider := range report.Providers {
		if provider.Status == "error" {
			return 3 * time.Minute
		}
	}
	return movieDiscoveryCacheTTL
}

// The chart can contain a current movie's primary catalog page before Apple's
// keyword search returns it. Search the complete cached chart, not just the
// 24 suggestions projected for the shelf. Retain the actual supplied URL and
// its storefront; chart presence does not establish a price or playable offer.
func (s *Scout) queryMovieChartListings(ctx context.Context, query, region string) ([]CandidateSource, ProviderStatus) {
	report := s.DiscoverMovies(ctx, "popular", region)
	items := report.Items
	s.discoveryMu.Lock()
	if cached, ok := s.discoveryCache[report.Region]; ok && time.Since(cached.at) < movieDiscoveryTTL(cached.report) {
		items = append([]MovieDiscoveryItem{}, cached.report.Items...)
	}
	s.discoveryMu.Unlock()
	status := ProviderStatus{Name: "Apple chart movie listings", Status: "empty"}
	var out []CandidateSource
	seen := map[string]bool{}
	for _, item := range items {
		if !isAppleMovieURL(item.SourceURL) || seen[item.SourceURL] {
			continue
		}
		page, _ := url.Parse(item.SourceURL)
		if page.Port() != "" && page.Port() != "443" {
			continue
		}
		_, year := movieTitleDetails(CandidateSource{Title: item.Title})
		candidate := CandidateSource{
			ID:    "apple-chart-listing-" + strings.TrimPrefix(item.ID, "apple-chart-"),
			Title: item.Title, URL: item.SourceURL, Provider: "Apple TV / iTunes", SourceType: "Movie Store",
			Thumbnail: item.Thumbnail, ReleaseYear: year, Region: movieURLMarket(page, "apple"), ListingOnly: true,
			AccessType: "paid", AccessBasis: "catalog", AccessEvidenceURL: item.SourceURL,
			AccessNote:  "Apple movie store listing. Current price and rental, purchase or preorder offers are unconfirmed.",
			Description: "Primary movie catalog page supplied by an Apple movie chart. Chart membership does not confirm a current offer, account access or full movie playback.",
		}
		// 100 is an exact canonical title; 85 is the same title with an
		// unconfirmed requested year. Partial titles and explicit year conflicts
		// cannot introduce unrelated movies from a chart into a title search.
		if score := relevanceScore(query, candidate); score != 100 && score != 85 {
			continue
		}
		seen[item.SourceURL] = true
		out = append(out, candidate)
	}
	status.Detail = fmt.Sprintf("Matched %d primary Apple movie catalog pages from current charts. Price, rental or purchase offers, account access and playback remain unconfirmed.", len(out))
	if len(out) > 0 {
		status.Status = "ok"
	} else {
		available := false
		for _, provider := range report.Providers {
			available = available || provider.Status == "ok" || provider.Status == "empty"
		}
		if !available {
			status.Status, status.Detail = "error", "Public Apple movie charts could not supply catalog pages for this search."
		}
	}
	return out, status
}

func (s *Scout) fetchMovieChart(ctx context.Context, market string) ([]MovieDiscoveryItem, ProviderStatus) {
	status := ProviderStatus{Name: "Apple movie chart (" + market + ")", Status: "ok"}
	endpoint := "https://itunes.apple.com/" + strings.ToLower(market) + "/rss/topmovies/limit=25/json"
	var payload struct {
		Feed struct {
			Entries []appleChartEntry `json:"entry"`
		} `json:"feed"`
	}
	if err := s.fetchProviderJSON(ctx, endpoint, nil, &payload); err != nil {
		status.Status, status.Detail = "error", "Public movie chart unavailable: "+err.Error()
		return nil, status
	}
	var items []MovieDiscoveryItem
	seen := map[string]bool{}
	for rank, entry := range payload.Feed.Entries {
		if rank >= 25 {
			break
		}
		title := strings.TrimSpace(html.UnescapeString(entry.Name.Label))
		id := strings.TrimSpace(entry.ID.Attributes.ID)
		numericID, err := strconv.ParseUint(id, 10, 64)
		if err != nil || numericID == 0 || title == "" || len([]rune(title)) > 250 || !strings.EqualFold(entry.ContentType.Attributes.Term, "Movie") || !isAppleMovieURL(entry.ID.Label) || seen[id] {
			continue
		}
		sourceURL, _ := url.Parse(entry.ID.Label)
		if sourceURL.Port() != "" && sourceURL.Port() != "443" {
			continue
		}
		item := MovieDiscoveryItem{ID: "apple-chart-" + id, Title: title, Query: title, SourceURL: sourceURL.String(), SourceName: "Apple movie charts", Markets: []string{market}, chartScore: 1 / float64(rank+1)}
		// A storefront date can describe a later regional release or reissue.
		// It must not constrain title search to an invented original film year.
		_, item.ReleaseYear = movieTitleDetails(CandidateSource{Title: title})
		if date, ok := movieChartDate(entry.ReleaseDate.Label); ok {
			item.ReleaseDate = date.Format("2006-01-02")
		}
		bestHeight := 0
		for _, image := range entry.Images {
			u, safe := safeAvailabilityURL(image.Label)
			height, _ := strconv.Atoi(image.Attributes.Height)
			if safe && u.Scheme == "https" && movieHostIs(strings.ToLower(u.Hostname()), "mzstatic.com") && height > bestHeight {
				item.Thumbnail, bestHeight = u.String(), height
			}
		}
		seen[id] = true
		items = append(items, item)
	}
	status.Detail = fmt.Sprintf("%d movie titles from Apple's %s store chart. Chart membership is not streaming availability.", len(items), market)
	if len(items) == 0 {
		status.Status = "empty"
	}
	return items, status
}

func movieChartDate(raw string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if date, err := time.Parse(layout, raw); err == nil && date.Year() >= 1880 {
			return date, true
		}
	}
	return time.Time{}, false
}

func projectMovieDiscovery(base MovieDiscoveryReport, mode string, now time.Time) MovieDiscoveryReport {
	report := base
	report.Mode, report.Cached = mode, false
	report.Providers = append([]ProviderStatus{}, base.Providers...)
	report.Items = make([]MovieDiscoveryItem, 0, len(base.Items))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	cutoff := today.AddDate(-2, 0, 0)
	for _, item := range base.Items {
		if mode == "recent" {
			date, ok := movieChartDate(item.ReleaseDate)
			if !ok || date.Before(cutoff) || date.After(today) {
				continue
			}
		}
		item.Markets = append([]string{}, item.Markets...)
		report.Items = append(report.Items, item)
	}
	sort.SliceStable(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if mode == "recent" && a.ReleaseDate != b.ReleaseDate {
			return a.ReleaseDate > b.ReleaseDate
		}
		if len(a.Markets) != len(b.Markets) {
			return len(a.Markets) > len(b.Markets)
		}
		if a.chartScore != b.chartScore {
			return a.chartScore > b.chartScore
		}
		return a.ID < b.ID
	})
	if len(report.Items) > movieDiscoveryLimit {
		report.Items = report.Items[:movieDiscoveryLimit]
	}
	report.Detail = "Search suggestions from current Apple movie charts in the listed markets. Dates are provider-reported storefront catalog release dates, not original film release years, and can include older reissues. Chart ranks are combined across sampled storefronts; they do not measure worldwide popularity or confirm free access, streaming availability, or playback."
	if mode == "recent" {
		report.Detail = "Recent store releases have a provider-reported storefront catalog date within the past 24 months, excluding future dates, and are sorted newest first. These dates can differ from the original film release and include older reissues. This is a selection from current Apple charts, not a complete new-film catalog. Streaming availability and playback need a separate title search."
	}
	successes := 0
	for _, provider := range report.Providers {
		if provider.Status == "ok" {
			successes++
		}
	}
	if len(report.Items) == 0 {
		if successes == 0 {
			report.Detail = "Movie charts could not supply suggestions for these markets. Try Worldwide or retry later; you can still search any movie title."
		} else if mode == "recent" {
			report.Detail = "No chart films have a known storefront catalog release date within the past 24 months. Try Popular chart films or search a recent movie title directly."
		}
	} else if successes < len(report.Providers) {
		report.Detail += " Some requested markets did not supply chart titles; the available markets are shown on each film."
	}
	return report
}
