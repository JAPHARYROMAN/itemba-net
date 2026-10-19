package scout

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/atlas"
)

// fawesomeDiscoveryDeadline bounds one shelf feed; the driver's own feed
// timeout is the same eight seconds.
const fawesomeDiscoveryDeadline = 8 * time.Second

// DiscoverFawesomeMovies reads the provider's ordinary anonymous guest
// catalog through the fawesome driver's feeds: popular is the public site's
// getPopularSearchURL contract, recent follows the home row the site itself
// names New Arrivals. The driver supplies the public configuration, a
// request-scoped guest token and the feed's parameters; nothing here invents
// a category identifier, a private endpoint or an alternate location. Cold
// requests: popular four, new arrivals five; a warm configuration saves two.
func (s *Scout) DiscoverFawesomeMovies(ctx context.Context, mode, region string) MovieDiscoveryReport {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "recent" {
		mode = "popular"
	}
	region = movieCountry(region)
	if region == "" {
		region = "ALL"
	}
	key := "fawesome:" + mode
	now := time.Now().UTC()
	s.discoveryMu.Lock()
	cached, found := s.discoveryCache[key]
	if found && now.Sub(cached.at) < fawesomeCatalogTTL(cached.report) {
		report := copyFawesomeCatalogReport(cached.report, region)
		report.Cached = true
		s.discoveryMu.Unlock()
		return report
	}
	s.discoveryMu.Unlock()
	catalogCtx, cancel := context.WithTimeout(ctx, fawesomeDiscoveryDeadline)
	defer cancel()
	listings, err := s.fawesomeFeed(catalogCtx, mode)
	status := ProviderStatus{Name: "Fawesome public catalog", Status: "ok"}
	report := MovieDiscoveryReport{Mode: mode, Region: "ALL", CheckedAt: now.Format(time.RFC3339), Items: []MovieDiscoveryItem{}}
	if mode == "recent" {
		report.Detail = "New on Fawesome follows the provider's current new-arrivals catalog order. A catalog date is not an original film release date. Availability in your country and protected full movie playback are checked when you search a title."
	} else {
		report.Detail = "Popular on Fawesome follows the provider's current popular catalog order. Catalog membership does not confirm country availability or playback. Search a title to check its full movie with ad protection enabled."
	}
	if err != nil {
		status.Status, status.Detail = "error", err.Error()
	} else {
		report.Items = fawesomeCatalogItems(listings)
		status.Detail = fmt.Sprintf("%d full-length movie listings from Fawesome's public guest catalog. Playback and country access remain unconfirmed.", len(report.Items))
		if len(report.Items) == 0 {
			status.Status = "empty"
		}
	}
	report.Providers = []ProviderStatus{status}
	s.discoveryMu.Lock()
	if s.discoveryCache == nil {
		s.discoveryCache = make(map[string]cachedMovieDiscovery)
	}
	s.discoveryCache[key] = cachedMovieDiscovery{report: report, at: now}
	s.discoveryMu.Unlock()
	return copyFawesomeCatalogReport(report, region)
}

// fawesomeFeed reads one shelf through the fawesome driver's feed of that
// name, over the guarded availability transport.
func (s *Scout) fawesomeFeed(ctx context.Context, mode string) ([]atlas.Listing, error) {
	d := s.driver("fawesome")
	if d == nil || !d.HasFeed(mode) {
		return nil, errors.New("the fawesome driver is not loaded, so its public catalog cannot be read")
	}
	return d.Feed(ctx, s.driverFetcher(), mode)
}

func fawesomeCatalogTTL(report MovieDiscoveryReport) time.Duration {
	for _, provider := range report.Providers {
		if provider.Status == "error" {
			return time.Minute
		}
	}
	return movieDiscoveryCacheTTL
}

func copyFawesomeCatalogReport(base MovieDiscoveryReport, region string) MovieDiscoveryReport {
	report := base
	report.Region = region
	report.Providers = append([]ProviderStatus(nil), base.Providers...)
	report.Items = append([]MovieDiscoveryItem{}, base.Items...)
	for i := range report.Items {
		report.Items[i].Markets = append([]string{}, base.Items[i].Markets...)
	}
	return report
}

// fawesomeCatalogItems turns a feed's listings into shelf items. Only exact
// movie pages with a known feature runtime are advertised; the catalog date
// the feed lists is kept as such, never as a release year.
func fawesomeCatalogItems(listings []atlas.Listing) []MovieDiscoveryItem {
	items := []MovieDiscoveryItem{}
	seen := map[string]bool{}
	for _, listing := range listings {
		if len(items) >= movieDiscoveryLimit {
			break
		}
		candidate := CandidateSource{URL: listing.URL, AccessType: listing.AccessType, Price: listing.Price}
		if strings.TrimSpace(listing.Title) == "" || !fawesomeMovieTarget(candidate) || !finiteFawesomeRuntime(mediaDurationSeconds(listing.Duration)) || seen[listing.URL] {
			continue
		}
		u, _ := url.Parse(listing.URL)
		id := fawesomeMoviePath.FindStringSubmatch(u.Path)[1]
		items = append(items, MovieDiscoveryItem{ID: "fawesome-catalog-" + id, Title: listing.Title, Query: listing.Title, SourceURL: listing.URL, SourceName: "Fawesome catalog", Markets: []string{}, CatalogDate: listing.CatalogDate, Thumbnail: listing.Thumbnail})
		seen[listing.URL] = true
	}
	return items
}
