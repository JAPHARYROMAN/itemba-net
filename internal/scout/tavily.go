package scout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const tavilySearchEndpoint = "https://api.tavily.com/search"
const tavilyRequestTimeout = 10 * time.Second

// Tavily takes documented country names rather than ISO alpha-2 codes. These
// preferences rank sources; they never establish where a movie can be watched.
var tavilyCountryNames = map[string]string{
	"US": "united states",
	"GB": "united kingdom",
	"IN": "india",
	"ZA": "south africa",
	"KE": "kenya",
	"TZ": "tanzania",
	"CA": "canada",
	"AU": "australia",
	"DE": "germany",
	"FR": "france",
}

// Choose one web provider to keep one search from consuming two providers'
// quotas. A failed configured provider is reported without silently charging
// another account for an automatic retry.
func (s *Scout) queryMovieWebProvider(ctx context.Context, query, region string) ([]CandidateSource, []ProviderStatus) {
	if strings.TrimSpace(os.Getenv("TAVILY_API_KEY")) != "" {
		candidates, status := s.queryTavilyMovieSites(ctx, query, region)
		return candidates, []ProviderStatus{status}
	}
	if strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY")) != "" {
		candidates, status := s.queryBraveMovieSites(ctx, query, region)
		return candidates, []ProviderStatus{status}
	}
	_, tavilyStatus := s.queryTavilyMovieSites(ctx, query, region)
	_, braveStatus := s.queryBraveMovieSites(ctx, query, region)
	return nil, []ProviderStatus{tavilyStatus, braveStatus}
}

type tavilyMovieResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func (s *Scout) queryTavilyMovieSites(ctx context.Context, query, region string) ([]CandidateSource, ProviderStatus) {
	status := ProviderStatus{Name: "Tavily web search", Status: "not_configured", Detail: "Tavily web discovery is not configured on this server. Movie store and open-media catalogs still work."}
	key := strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	if key == "" {
		return nil, status
	}
	region = strings.ToUpper(strings.TrimSpace(region))
	country := tavilyCountryNames[region]
	payload := struct {
		Query              string   `json:"query"`
		SearchDepth        string   `json:"search_depth"`
		Topic              string   `json:"topic"`
		MaxResults         int      `json:"max_results"`
		ChunksPerSource    int      `json:"chunks_per_source"`
		AutoParameters     bool     `json:"auto_parameters"`
		IncludeAnswer      bool     `json:"include_answer"`
		IncludeRawContent  bool     `json:"include_raw_content"`
		IncludeImages      bool     `json:"include_images"`
		Country            string   `json:"country,omitempty"`
		IncludeDomains     []string `json:"include_domains,omitempty"`
		IncludeDomainsMode string   `json:"include_domains_mode,omitempty"`
		ExcludeDomains     []string `json:"exclude_domains"`
	}{
		Query:           movieWatchWebQuery(query, region),
		SearchDepth:     "basic",
		Topic:           "general",
		MaxResults:      providerResultLimit,
		ChunksPerSource: 1,
		Country:         country,
		IncludeDomains:  preferredMovieWatchDomains(region),
		ExcludeDomains:  []string{"imdb.com", "wikipedia.org", "justwatch.com", "reelgood.com", "rottentomatoes.com", "useflickd.com", "ontvtonight.com", "flicks.co.nz", "flicks.com.au", "tvguide.com", "moviefone.com", "playpilot.com", "watchmode.com", "yidio.com", "letterboxd.com", "metacritic.com", "themoviedb.org"},
	}
	if len(payload.IncludeDomains) > 0 {
		payload.IncludeDomainsMode = "prefer"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		status.Status, status.Detail = "error", "Could not prepare the web search request. Try again."
		return nil, status
	}
	headers := http.Header{
		"Authorization": {"Bearer " + key},
		"Content-Type":  {"application/json"},
	}
	var response struct {
		Results *[]tavilyMovieResult `json:"results"`
	}
	// Web discovery can take longer than a catalog lookup. Copy the client so
	// this budget does not change concurrent catalog/probe timeouts. The search
	// context still caps the whole request, with time left for media checks.
	client := http.DefaultClient
	if s.httpClient != nil {
		client = s.httpClient
	}
	webClient := *client
	webClient.Timeout = tavilyRequestTimeout
	if err := fetchProviderJSONRequestWithClient(ctx, &webClient, tavilyRequestTimeout, http.MethodPost, tavilySearchEndpoint, headers, bytes.NewReader(encoded), &response); err != nil {
		status.Status, status.Detail = "error", tavilySearchError(err)
		return nil, status
	}
	if response.Results == nil {
		status.Status, status.Detail = "error", tavilySearchError(errInvalidProviderJSON)
		return nil, status
	}
	var candidates []CandidateSource
	seen := make(map[string]bool)
	for _, result := range *response.Results {
		if len(candidates) >= providerResultLimit {
			break
		}
		u, err := url.Parse(result.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.TrimSpace(result.Title) == "" || seen[result.URL] {
			continue
		}
		originalTitle := html.UnescapeString(result.Title)
		if hasUnrequestedDerivative(query, originalTitle) || !isMovieWebDestination(u, originalTitle) {
			continue
		}
		title := normalizeWebMovieTitle(originalTitle, u.Hostname())
		seen[result.URL] = true
		id := sha256.Sum256([]byte(result.URL))
		candidates = append(candidates, CandidateSource{
			ID:          fmt.Sprintf("tavily-%x", id[:8]),
			Title:       title,
			URL:         result.URL,
			Provider:    u.Hostname(),
			SourceType:  "Web Listing",
			Description: webMovieDescription(originalTitle, title, html.UnescapeString(result.Content)),
			AccessType:  "unknown",
			AccessNote:  "Search result. Hosting, price, regional availability and playback have not been verified.",
			Region:      "Unknown",
			ListingOnly: true,
		})
	}
	status.Status = "ok"
	if len(candidates) == 0 {
		status.Status = "empty"
	}
	status.Detail = fmt.Sprintf("%d web candidates returned; only matching movie watch pages are kept. Prices and title availability need confirmation.", len(candidates))
	if country != "" {
		status.Detail += " Country preference: " + country + "; this ranks sources and does not verify regional availability."
	} else if region != "" && region != "ALL" {
		status.Detail += " No country preference is available for " + region + "; global results were used."
	}
	return candidates, status
}

func movieWatchWebQuery(query, region string) string {
	search := query + " watch full movie online official streaming subscription rent buy free"
	if strings.EqualFold(strings.TrimSpace(region), "TZ") {
		search += " Tanzania"
	}
	return search
}

func preferredMovieWatchDomains(region string) []string {
	// Global discovery must be open to providers in every market instead of
	// preferring a short list of familiar domestic services.
	if movieCountry(region) == "" {
		return nil
	}
	domains := []string{"netflix.com", "primevideo.com", "tv.apple.com", "youtube.com", "archive.org"}
	if strings.EqualFold(strings.TrimSpace(region), "TZ") {
		return append(domains, "dstv.com", "azam.tv", "azamtvmax.com")
	}
	return append(domains, "tubitv.com", "mubi.com", "athome.fandango.com")
}

func tavilySearchError(err error) string {
	if errors.Is(err, errInvalidProviderJSON) {
		return "Tavily returned an unreadable response. Try again later."
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Sprintf("Tavily authentication or access failed (HTTP %d). Check the server's API key and account access.", httpErr.StatusCode)
		case http.StatusTooManyRequests:
			return "Tavily search limit reached (HTTP 429). Wait and try again, or check the account's request limits."
		case 432:
			return "Tavily's plan usage limit was reached (HTTP 432). Check the account's remaining search credits."
		case 433:
			return "Tavily's pay-as-you-go limit was reached (HTTP 433). Check the usage limit in the Tavily dashboard."
		default:
			return fmt.Sprintf("Tavily search failed (HTTP %d). Try again later.", httpErr.StatusCode)
		}
	}
	// Errors from the shared transport contain no response body, URL or header.
	return "Tavily search failed: " + err.Error() + ". Try again."
}
