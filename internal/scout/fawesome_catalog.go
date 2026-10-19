package scout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DiscoverFawesomeMovies reads the provider's ordinary anonymous guest catalog.
// Popular uses the public site's getPopularSearchURL contract. New arrivals
// follows a matching named row from the current home sub-categories response;
// no category identifier, private endpoint or alternate location is invented.
// Cold requests: popular four, new arrivals five; warm configuration saves two.
// All requests share one eight-second deadline and the guarded guest client.
// fawesomeDiscoveryDeadline bounds one shelf feed fetch through the public guest client.
const fawesomeDiscoveryDeadline = 8 * time.Second

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
	candidates, data, err := s.fetchFawesomeCatalog(catalogCtx, mode)
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
		report.Items = fawesomeCatalogItems(candidates, data)
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

func (s *Scout) fetchFawesomeCatalog(ctx context.Context, mode string) ([]CandidateSource, []byte, error) {
	guest, err := s.newFawesomeGuest(ctx)
	if err != nil {
		return nil, nil, err
	}
	params := url.Values{"searchType": {"popular"}}
	if mode == "recent" {
		rows, err := guest.fetch(ctx, "sub-categories.php", url.Values{"parent": {"home"}})
		if err != nil {
			return nil, nil, err
		}
		var ok bool
		params, ok = fawesomeNewArrivalFeed(rows, guest.config.apiBase)
		if !ok {
			return nil, nil, errors.New("Fawesome's current new-arrivals movie row was not identified in its public catalog")
		}
	}
	data, err := guest.fetch(ctx, "recipes.php", params)
	if err != nil {
		return nil, nil, err
	}
	candidates, err := fawesomeDiscoveryCandidates(data, "")
	return candidates, data, err
}

// The public bundle's home() requests sub-categories.php?parent=home. Its
// parseSubCategories reads channels[].name/url; dataManager.fetchData sends a
// supplied recipes.php feed through the same-origin apiBase, retaining query
// parameters. Only an unambiguous new-arrivals movie row is followed.
func fawesomeNewArrivalFeed(data []byte, apiBase string) (url.Values, bool) {
	var payload struct {
		Status   string `json:"status"`
		Channels []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"channels"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Status != "ok" {
		return nil, false
	}
	base, err := url.Parse(apiBase)
	if err != nil {
		return nil, false
	}
	var selected url.Values
	for _, row := range payload.Channels {
		if strings.ToLower(strings.Join(strings.Fields(row.Name), " ")) != "new arrivals" {
			continue
		}
		u, safe := safeAvailabilityURL(row.URL)
		if !safe || u.Scheme != "https" || u.User != nil || u.Fragment != "" || path.Base(u.Path) != "recipes.php" || (u.Host != "rapi.ifood.tv" || u.Path != "/recipes.php") && (u.Host != base.Host || u.Path != base.Path+"recipes.php") {
			continue
		}
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(values) == 0 || selected != nil {
			return nil, false
		}
		selected, safe = fawesomeCatalogFeedParams(values)
		if !safe {
			return nil, false
		}
	}
	return selected, selected != nil
}

var (
	fawesomeCatalogFeedNumber  = regexp.MustCompile(`^[1-9][0-9]{0,11}$`)
	fawesomeCatalogRouteNumber = regexp.MustCompile(`^-?[0-9]{1,12}(?:-[0-9]{1,12}){0,8}$`)
	fawesomeCatalogVersion     = regexp.MustCompile(`^sv[0-9]{1,2}\.[0-9]{1,2}$`)
)

// Allow only the catalog fields observed in the provider's returned recipes
// feeds. Its public app identifiers are replaced by fresh guest configuration;
// a returned feed cannot introduce a session, account, device or location.
func fawesomeCatalogFeedParams(values url.Values) (url.Values, bool) {
	out := url.Values{}
	for key, entries := range values {
		if len(entries) != 1 || len(entries[0]) > 160 || strings.ContainsAny(entries[0], "\r\n\x00") {
			return nil, false
		}
		value := entries[0]
		switch key {
		case "appId", "siteId", "auth-token", "platform_id":
			continue
		case "deviceModel", "deviceId", "session-id":
			if value != "" {
				return nil, false
			}
			continue
		case "searchType":
			if value != "new-qid" && value != "mixedcontent" {
				return nil, false
			}
		case "keys":
			if !fawesomeCatalogFeedNumber.MatchString(value) {
				return nil, false
			}
		case "start-index", "max-results":
			number, err := strconv.Atoi(value)
			if value != "" && (err != nil || number < 0 || number > 200) {
				return nil, false
			}
		case "highlight":
			if value != "" && value != "0" && value != "1" {
				return nil, false
			}
		case "order":
			if value != "0" && value != "1" && value != "2" && value != "" {
				return nil, false
			}
		case "dltype":
			if value != "0" && value != "1" {
				return nil, false
			}
		case "rowType":
			if value != "mixedcontent" {
				return nil, false
			}
		case "sort_type", "logic":
			if value != "" {
				return nil, false
			}
		case "akp":
			if value != "" && !fawesomeCatalogRouteNumber.MatchString(value) {
				return nil, false
			}
		case "gridstyle":
			if value != "" && value != "flat-movie" {
				return nil, false
			}
		case "version":
			if value != "" && !fawesomeCatalogVersion.MatchString(value) {
				return nil, false
			}
		case "parent":
			if value != "" && value != "Home" && value != "home" {
				return nil, false
			}
		case "userType":
			if value != "" && value != "new" {
				return nil, false
			}
		default:
			return nil, false
		}
		out[key] = []string{value}
	}
	return out, (out.Get("searchType") == "new-qid" || out.Get("searchType") == "mixedcontent") && fawesomeCatalogFeedNumber.MatchString(out.Get("keys"))
}

func fawesomeCatalogItems(candidates []CandidateSource, data []byte) []MovieDiscoveryItem {
	var payload struct {
		Results []struct {
			NodeID int64  `json:"node_id"`
			Date   string `json:"date"`
		} `json:"results"`
	}
	_ = json.Unmarshal(data, &payload)
	dates := map[string]string{}
	for _, row := range payload.Results {
		if date, err := time.Parse("Jan. 2, 2006", row.Date); err == nil && date.Year() >= 1880 && date.Before(time.Now().AddDate(1, 0, 0)) {
			dates[strconv.FormatInt(row.NodeID, 10)] = date.Format("2006-01-02")
		}
	}
	items := []MovieDiscoveryItem{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if len(items) >= movieDiscoveryLimit {
			break
		}
		if !fawesomeMovieTarget(candidate) || !finiteFawesomeRuntime(mediaDurationSeconds(candidate.Duration)) || seen[candidate.URL] {
			continue
		}
		u, _ := url.Parse(candidate.URL)
		id := fawesomeMoviePath.FindStringSubmatch(u.Path)[1]
		item := MovieDiscoveryItem{ID: "fawesome-catalog-" + id, Title: candidate.Title, Query: candidate.Title, SourceURL: candidate.URL, SourceName: "Fawesome catalog", Markets: []string{}, CatalogDate: dates[id], Thumbnail: candidate.Thumbnail}
		// release_date, catalog date and updated are not independent proof of an
		// original film year. Keep title search broad; preflight checks the film.
		items = append(items, item)
		seen[candidate.URL] = true
	}
	return items
}
