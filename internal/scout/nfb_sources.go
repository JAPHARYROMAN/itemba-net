package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var (
	nfbVersionYear     = regexp.MustCompile(`['"]nfb_version_year['"]\s*:\s*['"]([0-9]{4})['"]`)
	nfbVersionDuration = regexp.MustCompile(`['"]nfb_version_duration['"]\s*:\s*['"]([0-9]+(?:\.[0-9]+)?)['"]`)
	nfbISODuration     = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?$`)
)

type nfbMovieMetadata struct {
	Type     json.RawMessage `json:"@type"`
	ID       string          `json:"@id"`
	URL      string          `json:"url"`
	Name     string          `json:"name"`
	Duration string          `json:"duration"`
	EmbedURL string          `json:"embedUrl"`
}

type nfbPlayerOptions struct {
	Source string `json:"source"`
	GTM    struct {
		Title       string `json:"title"`
		RegistryID  int64  `json:"registryId"`
		VersionType string `json:"versionType"`
	} `json:"gtm"`
	Overlay struct {
		URL string `json:"url"`
	} `json:"overlay"`
}

func nfbHost(host string) bool {
	return host == "nfb.ca" || host == "www.nfb.ca" || host == "onf.ca" || host == "www.onf.ca"
}

func nfbMovieTarget(source CandidateSource) bool {
	if source.Price != "" || source.AccessType != "" && source.AccessType != "unknown" && source.AccessType != "free" {
		return false
	}
	u, safe := safeAvailabilityURL(source.URL)
	if !safe || u.Scheme != "https" || !nfbHost(u.Hostname()) {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && parts[0] == "film" && freeArchiveIdentifier.MatchString(parts[1])
}

// The primary film page names its own embed in Movie+VideoObject JSON-LD.
// That public embed supplies window.IFRAME_OPTIONS.source, gtm.registryId,
// gtm.versionType="film", gtm.title and overlay.url. Supplied signed URLs
// remain private and are used unchanged; no token, alternate origin, account,
// download licence or country override is constructed.
// Observed 2026-10-18: https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/
func inspectNFBPlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	check := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL, Detail: "NFB's public full film player could not be identified with protection enabled."}
	if !nfbMovieTarget(source) {
		return source, check
	}
	checkCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	data, page, err := fetchFreeSourceDocument(checkCtx, client, source.URL)
	requested, _ := url.Parse(source.URL)
	if err != nil || page == nil || !sameNFBMoviePage(page, requested) {
		return source, check
	}
	meta, year, duration, registry, restriction, ok := parseNFBMoviePage(data, page)
	if restriction != "" {
		check.Status, check.Reason, check.Detail = "not_started", "region_unavailable", "NFB reports that this film is unavailable in the check network's country. No media was requested."
		return source, check
	}
	if !ok {
		return source, check
	}
	match := source
	if strings.TrimSpace(match.Title) == "" {
		match.Title = meta.Name
	}
	if !matchingFreeMovieMetadata(match, meta.Name, year, duration) {
		check.Detail = "NFB's primary title, release year or full film runtime did not match this source."
		return source, check
	}
	embed, safe := safeAvailabilityURL(meta.EmbedURL)
	if !safe || embed.Scheme != "https" || embed.Host != page.Host || embed.Path != strings.TrimSuffix(page.Path, "/")+"/embed/player/" || embed.RawQuery != "" {
		return source, check
	}
	check.EvidenceURL = embed.String()
	playerData, final, err := fetchFreeSourceDocument(checkCtx, client, embed.String())
	if err != nil || final == nil || final.Host != embed.Host || final.Path != embed.Path {
		return source, check
	}
	options, restriction, ok := parseNFBPlayerOptions(playerData)
	if restriction != "" {
		check.Status, check.Reason, check.Detail = "not_started", "region_unavailable", "NFB's guest player reports a country restriction. No media was requested."
		return source, check
	}
	if !ok || options.GTM.VersionType != "film" || options.GTM.RegistryID != registry || !sameAdditionalFeatureTitle(meta.Name, options.GTM.Title) {
		return source, check
	}
	overlay, safe := safeAvailabilityURL(options.Overlay.URL)
	if !safe || !sameNFBMoviePage(overlay, page) {
		return source, check
	}
	media, safe := safeAvailabilityURL(options.Source)
	if !safe || media.Scheme != "https" || !strings.HasSuffix(strings.ToLower(media.Path), ".m3u8") || !strings.HasPrefix(media.Path, "/hls/") || !strings.Contains(media.Path, "_"+strconv.FormatInt(registry, 10)+"_") {
		return source, check
	}
	source.ID = resolvedFreeMovieID("nfb", source.URL)
	source.Title, source.ReleaseYear, source.Duration = meta.Name, year, strconv.FormatFloat(duration, 'f', -1, 64)
	source.Provider, source.SourceType = "NFB / ONF", "Provider free movie"
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "nfb_movie_metadata", embed.String()
	source.AccessNote = "The public NFB film player supplied this full film for the current guest request. Protected native playback must still be verified."
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, false, media.String()
	source.DirectStreamURL, source.KnownManifests = "", nil
	return source, nil
}

func sameNFBMoviePage(a, b *url.URL) bool {
	return a != nil && b != nil && a.Scheme == "https" && b.Scheme == "https" && nfbHost(a.Hostname()) && strings.TrimPrefix(a.Hostname(), "www.") == strings.TrimPrefix(b.Hostname(), "www.") && a.Path == b.Path
}

func nfbResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	if page == nil || media == nil || evidence == nil || source.Provider != "NFB / ONF" || source.SourceType != "Provider free movie" || source.AccessBasis != "nfb_movie_metadata" || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != media.String() || !nfbMovieTarget(CandidateSource{URL: page.String(), AccessType: "free"}) {
		return false
	}
	original, _ := url.Parse(source.URL)
	if !sameNFBMoviePage(page, original) || evidence.String() != source.AccessEvidenceURL {
		return false
	}
	_, safe := safeAvailabilityURL(media.String())
	return safe && media.Scheme == "https" && evidence.Scheme == "https" && evidence.Host == page.Host && evidence.Path == strings.TrimSuffix(page.Path, "/")+"/embed/player/" && evidence.RawQuery == ""
}

func parseNFBMoviePage(data []byte, page *url.URL) (nfbMovieMetadata, int, float64, int64, string, bool) {
	var meta nfbMovieMetadata
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return meta, 0, 0, 0, "", false
	}
	count := 0
	registry := int64(0)
	restriction := ""
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			if node.Data == "nfb-player-error-message" && nfbAttribute(node, "error") == "geoblocked" {
				restriction = "geoblocked"
			}
			if nfbAttribute(node, "id") == "download-product-button" {
				registry, _ = strconv.ParseInt(nfbAttribute(node, "data-id_registre"), 10, 64)
			}
			if node.Data == "script" && nfbAttribute(node, "type") == "application/ld+json" {
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type != html.TextNode {
						continue
					}
					var candidate nfbMovieMetadata
					if json.Unmarshal([]byte(child.Data), &candidate) != nil || !nfbMovieVideoType(candidate.Type) {
						continue
					}
					u, safe := safeAvailabilityURL(candidate.URL)
					id, idSafe := safeAvailabilityURL(candidate.ID)
					if safe && idSafe && sameNFBMoviePage(u, page) && sameNFBMoviePage(id, page) {
						meta = candidate
						count++
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	year := 0
	if match := nfbVersionYear.FindSubmatch(data); len(match) == 2 {
		year, _ = strconv.Atoi(string(match[1]))
	}
	duration := nfbDurationSeconds(meta.Duration)
	if match := nfbVersionDuration.FindSubmatch(data); len(match) == 2 {
		seconds, _ := strconv.ParseFloat(string(match[1]), 64)
		if seconds >= 40*60 && math.Abs(seconds-duration) <= math.Max(60, duration*.05) {
			duration = seconds
		}
	}
	return meta, year, duration, registry, restriction, count == 1 && registry > 0 && duration >= 40*60 && !math.IsInf(duration, 0) && !math.IsNaN(duration)
}

func nfbAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func nfbMovieVideoType(raw json.RawMessage) bool {
	var types []string
	if json.Unmarshal(raw, &types) != nil {
		return false
	}
	movie, video := false, false
	for _, kind := range types {
		movie = movie || kind == "Movie"
		video = video || kind == "VideoObject"
	}
	return movie && video
}

func nfbDurationSeconds(raw string) float64 {
	if strings.HasPrefix(raw, "PT") && strings.Contains(raw, ":") {
		return mediaDurationSeconds(strings.TrimPrefix(raw, "PT"))
	}
	match := nfbISODuration.FindStringSubmatch(raw)
	if len(match) != 4 {
		return 0
	}
	hours, _ := strconv.ParseFloat(match[1], 64)
	minutes, _ := strconv.ParseFloat(match[2], 64)
	seconds, _ := strconv.ParseFloat(match[3], 64)
	return hours*3600 + minutes*60 + seconds
}

func parseNFBPlayerOptions(data []byte) (nfbPlayerOptions, string, bool) {
	var options nfbPlayerOptions
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return options, "", false
	}
	count := 0
	restriction := ""
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "nfb-player-error-message" && nfbAttribute(node, "error") == "geoblocked" {
			restriction = "geoblocked"
		}
		if node.Type == html.ElementNode && node.Data == "script" {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.TextNode {
					continue
				}
				const marker = "window.IFRAME_OPTIONS ="
				if index := strings.Index(child.Data, marker); index >= 0 {
					var parsed nfbPlayerOptions
					if json.NewDecoder(strings.NewReader(child.Data[index+len(marker):])).Decode(&parsed) == nil {
						options = parsed
						count++
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return options, restriction, count == 1
}

// The public search page sets VUE_SEARCH_APP_API_BASE_URL=searchapi.nfb.ca;
// its observed Vue bundle calls /api/search/v4/films/ with q, per_page and
// language, then renders /film/{returned slug}/. One request, four seconds.
func (s *Scout) queryNFBMovieSites(ctx context.Context, query string) ([]CandidateSource, error) {
	searchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	title, _ := movieQuery(query)
	values := url.Values{"q": {title}, "per_page": {"6"}, "language": {"en"}, "include": {"description"}, "page": {"1"}, "sort_by": {"relevance"}, "order_by": {"desc"}}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	data, final, err := fetchFreeSourceDocument(searchCtx, client, "https://searchapi.nfb.ca/api/search/v4/films/?"+values.Encode())
	if err != nil {
		// URL errors include the query in their text; keep only the transport
		// cause in provider diagnostics. No response body is exposed.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("NFB public film search unavailable: %w", err)
	}
	if final == nil || final.Hostname() != "searchapi.nfb.ca" || final.Path != "/api/search/v4/films/" {
		return nil, errors.New("NFB public film search unavailable")
	}
	var payload struct {
		Items []struct {
			Title       string          `json:"title"`
			Slug        string          `json:"slug"`
			Year        int             `json:"year"`
			Time        string          `json:"time"`
			Description json.RawMessage `json:"description"`
			Free        bool            `json:"is_streamable_for_free"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("NFB public film search response unrecognized: %w", err)
	}
	var out []CandidateSource
	for _, item := range payload.Items {
		if len(out) >= 6 {
			break
		}
		duration := nfbDurationSeconds(item.Time)
		if !freeArchiveIdentifier.MatchString(item.Slug) || !item.Free || duration > 0 && duration < 40*60 {
			continue
		}
		source := CandidateSource{ID: resolvedFreeMovieID("nfb-search", item.Slug), Title: item.Title, URL: "https://www.nfb.ca/film/" + item.Slug + "/", ReleaseYear: item.Year, Provider: "NFB / ONF", SourceType: "Web Listing", AccessType: "unknown", ListingOnly: true, Description: nfbCatalogDescription(item.Description), AccessNote: "NFB's catalog lists free streaming; the full guest player and actual playback need separate checks."}
		if duration > 0 {
			source.Duration = strconv.FormatFloat(duration, 'f', -1, 64)
		}
		if relevanceScore(query, source) > 0 {
			out = append(out, source)
		}
	}
	return out, nil
}

// NFB's include=description response uses a language-keyed object, including
// null translations. Missing prose must not discard otherwise valid listings.
func nfbCatalogDescription(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var localized struct {
			English string `json:"en"`
		}
		if json.Unmarshal(raw, &localized) != nil {
			return ""
		}
		text = localized.English
	}
	doc, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(availabilityNodeText(doc)), " ")
}
