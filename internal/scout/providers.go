package scout

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	appleSearchEndpoint = "https://itunes.apple.com/search"
	braveSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"
	providerResultLimit = 20
)

var errInvalidProviderJSON = errors.New("invalid provider JSON response")

// These are page-title conventions, not evidence that a movie is available.
// Limit rewriting to the destination site's own brand and official host.
var webMovieTitleBrands = []struct {
	host   string
	brands []string
}{
	{"netflix.com", []string{"Netflix", "Netflix Official Site"}},
	{"primevideo.com", []string{"Prime Video", "PrimeVideo", "Amazon Prime Video"}},
	{"amazon.com", []string{"Amazon", "Amazon.com", "Prime Video", "Amazon Prime Video"}},
	{"disneyplus.com", []string{"Disney+", "Disney Plus"}},
	{"hulu.com", []string{"Hulu"}},
	{"paramountplus.com", []string{"Paramount+", "Paramount Plus"}},
	{"plex.tv", []string{"Plex"}},
	{"pluto.tv", []string{"Pluto TV", "Pluto"}},
	{"filmzie.com", []string{"Filmzie"}},
	{"fawesome.tv", []string{"Fawesome", "Fawesome TV"}},
	{"nfb.ca", []string{"NFB", "NFB.ca", "National Film Board of Canada"}},
	{"onf.ca", []string{"ONF", "ONF.ca"}},
	{"plus.nasa.gov", []string{"NASA+", "NASA Plus"}},
	{"catsuka.com", []string{"Catsuka"}},
	{"rakuten.tv", []string{"Rakuten TV", "Rakuten"}},
	{"tubitv.com", []string{"Tubi", "Tubi TV"}},
	{"peacocktv.com", []string{"Peacock", "Peacock TV"}},
	{"max.com", []string{"Max", "HBO Max"}},
	{"hbomax.com", []string{"Max", "HBO Max"}},
	{"therokuchannel.roku.com", []string{"The Roku Channel", "Roku"}},
	{"tv.apple.com", []string{"Apple TV", "Apple TV+"}},
	{"youtube.com", []string{"YouTube"}},
	{"youtu.be", []string{"YouTube"}},
	{"dstv.com", []string{"DStv", "DStv Stream"}},
	{"azamtvmax.com", []string{"AzamTV MAX", "Azam TV MAX"}},
	{"hotstar.com", []string{"JioHotstar", "Disney+ Hotstar", "Hotstar"}},
	{"jiohotstar.com", []string{"JioHotstar"}},
	{"athome.fandango.com", []string{"Fandango at Home", "Fandango", "Fandango (Vudu)", "Fandango at Home (Vudu)", "Vudu"}},
	{"vudu.com", []string{"Fandango at Home", "Fandango", "Fandango (Vudu)", "Vudu"}},
	{"mubi.com", []string{"MUBI"}},
	{"archive.org", []string{"Internet Archive", "Free Download, Borrow, and Streaming"}},
	{"publicdomainmovie.net", []string{"Public Domain Movies", "PublicDomainMovie.net", "Public Domain Movie"}},
	{"imdb.com", []string{"IMDb"}},
}

func normalizeWebMovieTitle(title, host string) string {
	var brands []string
	host = strings.ToLower(host)
	for _, site := range webMovieTitleBrands {
		if host == site.host || strings.HasSuffix(host, "."+site.host) {
			brands = site.brands
			break
		}
	}
	if len(brands) == 0 && movieAmazonHost(host) {
		brands = []string{"Amazon", "Prime Video", "Amazon Prime Video"}
	}
	if len(brands) == 0 {
		return title
	}
	normalized := strings.TrimSpace(title)
	for {
		stripped := false
		for _, separator := range []string{" | ", " - ", " – ", " — ", " : "} {
			if i := strings.LastIndex(normalized, separator); i > 0 {
				suffix := strings.TrimSpace(normalized[i+len(separator):])
				for _, brand := range brands {
					brandSuffix := suffix
					if n := len(suffix); n >= 5 && suffix[n-4] == '(' && suffix[n-1] == ')' && isASCIIAlpha(suffix[n-3]) && isASCIIAlpha(suffix[n-2]) {
						brandSuffix = strings.TrimSpace(suffix[:n-4])
					}
					matched := strings.EqualFold(brandSuffix, brand)
					for _, action := range []string{"Watch on ", "Stream on ", "Watch Full Movie on "} {
						matched = matched || strings.EqualFold(brandSuffix, action+brand)
					}
					if matched {
						normalized, stripped = strings.TrimSpace(normalized[:i]), true
						break
					}
				}
			}
			if stripped {
				break
			}
		}
		if !stripped {
			break
		}
	}
	if movieHostIs(host, "plex.tv") && strings.HasPrefix(strings.ToLower(normalized), "where to watch ") {
		normalized = strings.TrimSpace(normalized[len("where to watch "):])
	}
	for _, action := range []string{"watch, rent, or buy ", "watch ", "stream ", "rent ", "buy "} {
		if strings.HasPrefix(strings.ToLower(normalized), action) && len(normalized) > len(action) {
			normalized = strings.TrimSpace(normalized[len(action):])
			break
		}
	}
	for _, brand := range brands {
		for _, label := range []string{" full movie free on ", " full movie on ", " online on "} {
			suffix := label + brand
			if strings.HasSuffix(strings.ToLower(normalized), strings.ToLower(suffix)) && len(normalized) > len(suffix) {
				normalized = strings.TrimSpace(normalized[:len(normalized)-len(suffix)])
			}
		}
	}
	if (movieHostIs(host, "athome.fandango.com") || movieHostIs(host, "vudu.com")) && strings.HasSuffix(strings.ToLower(normalized), " at home + online") {
		normalized = strings.TrimSpace(normalized[:len(normalized)-len(" at home + online")])
	}
	for _, label := range []string{" full movie online", " watch online", " movie online", " online"} {
		if strings.HasSuffix(strings.ToLower(normalized), label) && len(normalized) > len(label) {
			normalized = strings.TrimSpace(normalized[:len(normalized)-len(label)])
			break
		}
	}
	// Streaming sites decorate single-word film titles too. Keep this rewriting
	// bound to recognized destinations so ordinary pages cannot hide extra words.
	for _, label := range []string{" streaming online", " full movie", " streaming", " - movies"} {
		if strings.HasSuffix(strings.ToLower(normalized), label) && len(normalized) > len(label) {
			normalized = strings.TrimSpace(normalized[:len(normalized)-len(label)])
			break
		}
	}
	return normalized
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func webMovieDescription(originalTitle, title, description string) string {
	if title != originalTitle {
		description = "Page title: " + originalTitle + ". " + description
	}
	runes := []rune(description)
	if len(runes) > 1000 {
		description = string(runes[:997]) + "..."
	}
	return strings.TrimSpace(description)
}

// discoverMovieSites fetches destination pages, never promotional preview streams.
// ALL samples storefronts across several markets alongside unrestricted web
// discovery. Storefront coverage never means universal title availability.
// discoverMovieSites runs the configured web-search provider. The storefront
// catalogs it used to fan out to are the apple-movies driver now.
func (s *Scout) discoverMovieSites(ctx context.Context, query, region string) ([]CandidateSource, []ProviderStatus) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "ALL"
	}
	return s.queryMovieWebProvider(ctx, query, region)
}

func isAppleMovieURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "itunes.apple.com" || host == "tv.apple.com") && strings.Contains(u.Path, "/movie/")
}

func (s *Scout) queryBraveMovieSites(ctx context.Context, query, region string) ([]CandidateSource, ProviderStatus) {
	region = movieCountry(region)
	if region == "" {
		region = "ALL"
	}
	status := ProviderStatus{Name: "Brave web search", Status: "not_configured", Detail: "Broader web discovery is not configured on this server. Movie store and open-media catalogs still work."}
	key := strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY"))
	if key == "" {
		return nil, status
	}
	params := url.Values{
		"q":                {movieWatchWebQuery(query, region) + " -trailer -review -site:imdb.com -site:wikipedia.org -site:justwatch.com -site:reelgood.com"},
		"country":          {"ALL"},
		"count":            {strconv.Itoa(providerResultLimit)},
		"result_filter":    {"web"},
		"text_decorations": {"false"},
		"spellcheck":       {"false"},
	}
	headers := http.Header{"X-Subscription-Token": {key}}
	if region != "ALL" {
		// Location headers accept all ISO alpha-2 codes, including countries
		// outside Brave's smaller list of country-biased search indexes.
		headers.Set("X-Loc-Country", region)
	}
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := s.fetchProviderJSON(ctx, braveSearchEndpoint+"?"+params.Encode(), headers, &payload); err != nil {
		status.Status, status.Detail = "error", "Web search failed: "+err.Error()
		if errors.Is(err, errInvalidProviderJSON) {
			status.Detail = "Web search returned an unreadable response. Try again later."
		}
		var httpErr providerHTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				status.Detail = fmt.Sprintf("Web search authentication failed (HTTP %d). Check the server's Brave API key and that its plan includes Web Search.", httpErr.StatusCode)
			case http.StatusTooManyRequests:
				status.Detail = "Web search limit reached (HTTP 429). Wait and try again, or check the Brave plan quota."
			}
		}
		return nil, status
	}
	var out []CandidateSource
	seen := make(map[string]bool)
	for _, result := range payload.Web.Results {
		if len(out) >= providerResultLimit {
			break
		}
		u, err := url.Parse(result.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || result.Title == "" || seen[result.URL] {
			continue
		}
		originalTitle := html.UnescapeString(result.Title)
		// Keep derivative evidence before removing a site's presentation labels.
		if hasUnrequestedDerivative(query, originalTitle) || !isMovieWebDestination(u, originalTitle) {
			continue
		}
		title := normalizeWebMovieTitle(originalTitle, u.Hostname())
		seen[result.URL] = true
		id := sha256.Sum256([]byte(result.URL))
		out = append(out, CandidateSource{
			ID:          fmt.Sprintf("web-%x", id[:8]),
			Title:       title,
			URL:         result.URL,
			Provider:    u.Hostname(),
			SourceType:  "Web Listing",
			Description: webMovieDescription(originalTitle, title, html.UnescapeString(result.Description)),
			AccessType:  "unknown",
			AccessNote:  "Search result. Hosting, price, regional availability and playback have not been verified.",
			Region:      "Unknown",
			ListingOnly: true,
		})
	}
	status.Status = "ok"
	status.Detail = fmt.Sprintf("%d destination links from the global web index; availability and prices are unverified.", len(out))
	if len(out) == 0 {
		status.Status = "empty"
	}
	return out, status
}

// Drop clear theater-ticket and channel pages without excluding the providers'
// individual movie pages. A retained link is still only an unverified listing.
func isMovieWebDestination(u *url.URL, title string) bool {
	host := strings.ToLower(u.Hostname())
	if host == "fandango.com" || host == "www.fandango.com" {
		lowerTitle := strings.ToLower(title)
		if strings.Contains(lowerTitle, "tickets") && strings.Contains(lowerTitle, "showtimes") {
			return false
		}
	}
	if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 1 && parts[0] == "watch" {
			return u.Query().Get("v") != ""
		}
		return len(parts) == 2 && parts[1] != "" && (parts[0] == "embed" || parts[0] == "shorts" || parts[0] == "live")
	}
	return true
}

type providerHTTPError struct {
	StatusCode int
}

func (err providerHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d", err.StatusCode)
}

// fetchProviderJSON bounds both elapsed time and response size. Provider failures
// become status messages; error bodies and authentication headers are never exposed.
func (s *Scout) fetchProviderJSON(ctx context.Context, endpoint string, headers http.Header, dest any) error {
	return s.fetchProviderJSONRequest(ctx, http.MethodGet, endpoint, headers, nil, dest)
}

func (s *Scout) fetchProviderJSONRequest(ctx context.Context, method, endpoint string, headers http.Header, body io.Reader, dest any) error {
	return fetchProviderJSONRequestWithClient(ctx, s.httpClient, 6*time.Second, method, endpoint, headers, body, dest)
}

func fetchProviderJSONRequestWithClient(ctx context.Context, client *http.Client, timeout time.Duration, method, endpoint string, headers http.Header, body io.Reader, dest any) error {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("invalid provider request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ITEMBA-NET-Scout/1.0")
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return fmt.Errorf("request canceled or timed out")
		}
		return fmt.Errorf("network request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerHTTPError{StatusCode: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dest); err != nil {
		return errInvalidProviderJSON
	}
	return nil
}
