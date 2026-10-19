package scout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var fawesomePublicBundlePath = regexp.MustCompile(`^/home/new/(v[1-9][0-9]{0,5})/assets/js/iptv-main\.js$`)
var fawesomePublicAppID = regexp.MustCompile(`\bappConfig\.appId=([1-9][0-9]{0,5})(?:[,;])`)
var fawesomePublicSiteID = regexp.MustCompile(`\bappConfig\.siteId=([1-9][0-9]{0,5})(?:[,;])`)
var fawesomePublicPlatformID = regexp.MustCompile(`\bwindow\.platform_id\|\|\(appConfig\.platform_id=([1-9][0-9]{0,11})\)`)
var fawesomePublicBuild = regexp.MustCompile(`\bbuildVersion:"(v[1-9][0-9]{0,5})"`)
var fawesomePublicAPIEnv = regexp.MustCompile(`\bappConfig\.apiEnv="([a-z]{1,24})",appConfig\.appId=`)
var fawesomeEffectiveGuestDefaults = regexp.MustCompile(`var a=\{iptv:!0,siteId:([1-9][0-9]{0,5}),platform_id:([1-9][0-9]{0,11}),buildVer:appInfo\.buildVersion\};return o\[e\]\?o\[e\]:a\[e\]\}`)
var fawesomeEpisodeLabel = regexp.MustCompile(`(?i)^S[0-9]{1,3}\s*E[0-9]{1,3}\b`)
var fawesomePublicAPIPath = regexp.MustCompile(`^/home/new/v[1-9][0-9]{0,5}/api/$`)

// Only public website configuration is cached. A fresh anonymous guest token
// stays in its request-scoped client and is never saved in Scout or results.
type fawesomePublicConfig struct {
	apiBase, appID, siteID, platformID, apiEnv string
	expiresAt                                  time.Time
}

type fawesomeGuestClient struct {
	client *http.Client
	config fawesomePublicConfig
	token  string
}

func (s *Scout) newFawesomeGuest(ctx context.Context) (*fawesomeGuestClient, error) {
	config, err := s.loadFawesomePublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guest := &fawesomeGuestClient{client: client, config: config}
	data, err := guest.request(ctx, "getSecurityToken.php", nil, false)
	if err != nil {
		return nil, err
	}
	var payload struct {
		SecurityToken string `json:"securityToken"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.SecurityToken == "" || len(payload.SecurityToken) > 4096 || strings.ContainsAny(payload.SecurityToken, "\r\n\x00") {
		return nil, errors.New("Fawesome's public guest initialization was unavailable")
	}
	guest.token = payload.SecurityToken
	return guest, nil
}

func (s *Scout) loadFawesomePublicConfig(ctx context.Context) (fawesomePublicConfig, error) {
	s.fawesomeMu.Lock()
	cached := s.fawesomeConfig
	s.fawesomeMu.Unlock()
	if ctx.Err() != nil {
		return fawesomePublicConfig{}, errors.New("Fawesome's public configuration check timed out")
	}
	if cached.apiBase != "" && time.Now().Before(cached.expiresAt) {
		return cached, nil
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	shell, final, err := fetchFreeSourceDocument(ctx, client, "https://fawesome.tv/")
	if err != nil || final == nil || final.Scheme != "https" || final.Host != "fawesome.tv" || final.Path != "/" || final.RawQuery != "" {
		return fawesomePublicConfig{}, errors.New("Fawesome's public website configuration was unavailable")
	}
	bundle, ok := fawesomeCurrentBundle(shell, final)
	if !ok {
		return fawesomePublicConfig{}, errors.New("Fawesome's current public player bundle was not identified")
	}
	data, bundleFinal, err := fetchFreeSourceDocument(ctx, client, bundle.String())
	if err != nil || bundleFinal == nil || bundleFinal.String() != bundle.String() {
		return fawesomePublicConfig{}, errors.New("Fawesome's current public player configuration was unavailable")
	}
	config, ok := parseFawesomePublicConfig(data, bundle)
	if !ok {
		return fawesomePublicConfig{}, errors.New("Fawesome's public guest configuration changed")
	}
	config.expiresAt = time.Now().Add(5 * time.Minute)
	s.fawesomeMu.Lock()
	s.fawesomeConfig = config
	s.fawesomeMu.Unlock()
	return config, nil
}

func fawesomeCurrentBundle(data []byte, page *url.URL) (*url.URL, bool) {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, false
	}
	var found []*url.URL
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" {
			ref, err := url.Parse(streamingSiteAttribute(node, "src"))
			if err == nil && ref.String() != "" {
				u := page.ResolveReference(ref)
				if u.Scheme == "https" && u.Host == "fawesome.tv" && u.User == nil && u.Fragment == "" && fawesomePublicBundlePath.MatchString(u.Path) {
					q := u.Query()
					valid := len(q) == 0
					if len(q) == 1 && len(q["ver"]) == 1 {
						_, e := strconv.ParseUint(q.Get("ver"), 10, 32)
						valid = e == nil
					}
					if valid {
						found = append(found, u)
					}
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(found) != 1 {
		return nil, false
	}
	return found[0], true
}

func parseFawesomePublicConfig(data []byte, bundle *url.URL) (fawesomePublicConfig, bool) {
	var config fawesomePublicConfig
	path := fawesomePublicBundlePath.FindStringSubmatch(bundle.Path)
	if len(path) != 2 {
		return config, false
	}
	text := string(data)
	values := []*regexp.Regexp{fawesomePublicAppID, fawesomePublicSiteID, fawesomePublicPlatformID, fawesomePublicBuild}
	parsed := make([]string, len(values))
	for i, pattern := range values {
		matches := pattern.FindAllStringSubmatch(text, -1)
		if len(matches) != 1 {
			return config, false
		}
		parsed[i] = matches[0][1]
	}
	if parsed[3] != path[1] || !strings.Contains(text, "function getSearchURL()") || !strings.Contains(text, "getSecurityToken.php") || !strings.Contains(text, "i.token=appConfig.securityToken") {
		return config, false
	}
	// Constructor siteId=1 belongs to the shared iFood application. The plain
	// Fawesome website's initAllParams replaces it with this URL helper's
	// explicit current guest defaults. Parsing the constructor alone selects a
	// different catalog. Bind both the override and its normal runtime use.
	start := strings.Index(text, "getIptvParameterFromUrl:function(e){")
	if start < 0 || !strings.Contains(text, `appConfig.siteId=helper.getIptvParameterFromUrl("siteId")`) {
		return config, false
	}
	end := strings.Index(text[start:], ",getCustomParameterFromCustomUrl:")
	if end < 0 || end > 2000 {
		return config, false
	}
	defaults := fawesomeEffectiveGuestDefaults.FindAllStringSubmatch(text[start:start+end], -1)
	envs := fawesomePublicAPIEnv.FindAllStringSubmatch(text, -1)
	if len(defaults) != 1 || defaults[0][2] != parsed[2] || len(envs) != 1 {
		return config, false
	}
	config.apiBase = "https://fawesome.tv/home/new/" + path[1] + "/api/"
	config.appID, config.siteID, config.platformID, config.apiEnv = parsed[0], defaults[0][1], parsed[2], envs[0][1]
	return config, true
}

func (g *fawesomeGuestClient) fetch(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	if endpoint != "recipes.php" && endpoint != "index.php" && endpoint != "sub-categories.php" {
		return nil, errors.New("Unsupported Fawesome public guest catalog endpoint")
	}
	if g.token == "" {
		return nil, errors.New("Fawesome's anonymous guest was not initialized")
	}
	return g.request(ctx, endpoint, params, true)
}

func (g *fawesomeGuestClient) request(ctx context.Context, endpoint string, params url.Values, withToken bool) ([]byte, error) {
	base, err := url.Parse(g.config.apiBase)
	if err != nil || base.Scheme != "https" || base.Host != "fawesome.tv" || base.User != nil || base.Opaque != "" || base.RawQuery != "" || base.Fragment != "" || !fawesomePublicAPIPath.MatchString(base.Path) {
		return nil, errors.New("Fawesome's public guest origin was invalid")
	}
	values := url.Values{}
	if endpoint != "recipes.php" && endpoint != "index.php" && endpoint != "sub-categories.php" && endpoint != "getSecurityToken.php" {
		return nil, errors.New("Unsupported Fawesome public guest catalog endpoint")
	}
	for k, items := range params {
		// The connection itself supplies location. Neither user queries nor a
		// public catalog feed can replace it with a country/IP/device identity.
		lower := strings.ToLower(k)
		if strings.Contains(lower, "country") || strings.Contains(lower, "ip_address") || strings.Contains(lower, "device") || lower == "token" || lower == "session-id" || lower == "auth-token" || lower == "appid" || lower == "siteid" || lower == "platform_id" {
			continue
		}
		values[k] = append([]string(nil), items...)
	}
	values.Set("appId", g.config.appID)
	values.Set("siteId", g.config.siteID)
	values.Set("auth-token", g.config.platformID)
	if g.config.apiEnv != "" {
		values.Set("apiEnv", g.config.apiEnv)
	}
	if endpoint == "recipes.php" && params.Get("searchType") == "search" {
		values.Set("platform_id", g.config.platformID)
	}
	base.Path += endpoint
	base.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, errors.New("Fawesome's public guest request was invalid")
	}
	request.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	request.Header.Set("User-Agent", "ITEMBA-NET/0.1 (public guest movie discovery)")
	request.Header.Set("Referer", "https://fawesome.tv/")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	if withToken {
		request.Header.Set("token", g.token)
	}
	client := *g.client
	client.Jar = nil
	if client.Transport == nil {
		client.Transport = newMovieAvailabilityClient().Transport
	}
	client.Timeout = 4 * time.Second
	// Redirects would forward a guest header and expose public identifiers in
	// diagnostics. The observed same-origin guest endpoints return directly.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Fawesome guest redirects are unsupported")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("Fawesome's public guest request was unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Fawesome's public guest catalog was unavailable")
	}
	limit := int64(availabilityPageLimit)
	if !withToken {
		limit = 32 << 10
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Fawesome's public guest response exceeded its bound")
	}
	return data, nil
}

type fawesomeDiscoveryItem struct {
	NodeID          int64   `json:"node_id"`
	Title           string  `json:"title"`
	WebURL          string  `json:"web_url"`
	Runtime         float64 `json:"runtime"`
	ItemType        string  `json:"item_type"`
	FeedType        string  `json:"feed_type"`
	ActionType      string  `json:"actiontype"`
	SeriesName      string  `json:"series_name"`
	BlockItem       string  `json:"blockItem"`
	Description     string  `json:"description"`
	Picture         string  `json:"main_picture"`
	runtimeSupplied bool
}

func (item *fawesomeDiscoveryItem) UnmarshalJSON(data []byte) error {
	type plain fawesomeDiscoveryItem
	var raw struct {
		*plain
		Runtime json.RawMessage `json:"runtime"`
	}
	raw.plain = (*plain)(item)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value := strings.TrimSpace(string(raw.Runtime))
	if value == "" || value == "null" {
		item.Runtime = 0
		item.runtimeSupplied = false
		return nil
	}
	if strings.HasPrefix(value, `"`) {
		var text string
		if json.Unmarshal(raw.Runtime, &text) != nil {
			return errors.New("Unsupported public movie runtime")
		}
		if strings.TrimSpace(text) == "" {
			item.Runtime = 0
			item.runtimeSupplied = false
			return nil
		}
		value = text
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return errors.New("Unsupported public movie runtime")
	}
	item.Runtime = seconds
	item.runtimeSupplied = true
	return nil
}

func fawesomeDiscoveryCandidates(data []byte, query string) ([]CandidateSource, error) {
	var payload struct {
		Status  string            `json:"status"`
		Type    string            `json:"type"`
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Status != "ok" || payload.Type != "result" || len(payload.Results) > 200 {
		return nil, errors.New("Fawesome did not return a recognized public movie catalog")
	}
	var candidates []CandidateSource
	seen := map[string]bool{}
	limit := 12
	if query == "" {
		limit = 24
	}
	for _, raw := range payload.Results {
		if len(candidates) >= limit {
			break
		}
		var item fawesomeDiscoveryItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		parts := fawesomeMoviePath.FindStringSubmatch("/" + item.WebURL)
		if len(parts) != 3 || parts[1] != strconv.FormatInt(item.NodeID, 10) || item.ItemType != "movie" || item.FeedType != "video" || item.ActionType != "video" || item.SeriesName != "" || fawesomeEpisodeLabel.MatchString(item.Title) || item.BlockItem == "true" || strings.TrimSpace(item.Title) == "" || len(item.Title) > 240 || strings.ContainsAny(item.Title, "<>\r\n\x00") || item.runtimeSupplied && !finiteFawesomeRuntime(item.Runtime) {
			continue
		}
		raw := "https://fawesome.tv/" + item.WebURL
		if seen[raw] || !fawesomeMovieTarget(CandidateSource{URL: raw}) {
			continue
		}
		candidate := CandidateSource{ID: resolvedFreeMovieID("fawesome-listing", raw), Title: item.Title, URL: raw, Provider: "Fawesome", SourceType: "Provider movie listing", AccessType: "free", AccessBasis: "provider_model", AccessEvidenceURL: "https://fawesome.tv/", AccessNote: "Fawesome lists free guest movies; current main-movie availability and protected playback are checked separately.", ListingOnly: true, DestinationType: "watch", Region: "Unknown", Description: strings.TrimSpace(item.Description)}
		if item.Runtime > 0 {
			candidate.Duration = strconv.FormatFloat(item.Runtime, 'f', -1, 64)
		}
		if picture, safe := safeAvailabilityURL(item.Picture); safe && picture.Scheme == "https" {
			candidate.Thumbnail = picture.String()
		}
		if query != "" && (relevanceScore(query, candidate) == 0 || hasUnrequestedDerivative(query, item.Title)) {
			continue
		}
		seen[raw] = true
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}
