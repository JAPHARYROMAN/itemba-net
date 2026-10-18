package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/itemba-net/core/internal/lens"
)

const minimumAdditionalFeatureDuration = 40 * 60

var arcoirisRuntimePattern = regexp.MustCompile(`(?i)\bDurata:\s*([0-9]+(?:[.,][0-9]+)?)\s*min\b`)
var arcoirisMediaPath = regexp.MustCompile(`^/store_[0-9]+/(?:lowres|hires)/(?:[a-z]{2})/.+\.(?:mp4|webm|ogv)$`)
var arcoirisLanguage = regexp.MustCompile(`^[a-z]{2}$`)
var leadingFeatureYear = regexp.MustCompile(`^(18[0-9]{2}|19[0-9]{2}|20[0-9]{2})(?:\s|\s*[-–])`)

// These adapters use the sites' observed public search forms, not a guessed
// private player API. Contracts checked 2026-10-18: Arcoiris' search form
// redirects to /search/s/<title>/type/all/ and PublicDomainMovie.org submits s
// with GET. Search results remain listings until their own media metadata and
// a separate native-video sample have been checked.
func (s *Scout) queryAdditionalFeatureSites(ctx context.Context, query string) ([]CandidateSource, []ProviderStatus) {
	searchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	title, _ := movieQuery(query)
	type catalog struct{ name, raw string }
	catalogs := []catalog{
		{"ArcoirisTV", "https://www.arcoiris.tv/search/s/" + url.PathEscape(title) + "/type/all/"},
		{"PublicDomainMovie.org", "https://publicdomainmovie.org/?s=" + url.QueryEscape(title)},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var candidates []CandidateSource
	statuses := make([]ProviderStatus, len(catalogs))
	for index, catalog := range catalogs {
		wg.Add(1)
		go func(index int, name, raw string) {
			defer wg.Done()
			status := ProviderStatus{Name: name + " feature catalog", Status: "empty"}
			data, final, err := fetchFreeSourceDocument(searchCtx, client, raw)
			var found []CandidateSource
			requested, _ := url.Parse(raw)
			if err != nil || final == nil || final.Hostname() != requested.Hostname() {
				status.Status, status.Detail = "error", "The public feature catalog could not be checked."
			} else {
				found = additionalFeatureSearchResults(data, final, query)
				status.Detail = fmt.Sprintf("%d matching movie pages; full-length media and playback are checked separately.", len(found))
				if len(found) > 0 {
					status.Status = "ok"
				}
			}
			mu.Lock()
			candidates = append(candidates, found...)
			statuses[index] = status
			mu.Unlock()
		}(index, catalog.name, catalog.raw)
	}
	wg.Wait()
	return candidates, statuses
}

func additionalFreeResolutionTarget(page *url.URL) (string, bool) {
	if page == nil || page.RawQuery != "" || page.Fragment != "" || page.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(page.Hostname())
	parts := strings.Split(strings.Trim(page.Path, "/"), "/")
	if (host == "www.arcoiris.tv" || host == "arcoiris.tv") && len(parts) == 3 && parts[0] == "scheda" && arcoirisLanguage.MatchString(parts[1]) {
		if id, err := strconv.Atoi(parts[2]); err == nil && id > 0 && strconv.Itoa(id) == parts[2] {
			return "arcoiris", true
		}
	}
	if (host == "publicdomainmovie.org" || host == "www.publicdomainmovie.org") && len(parts) == 1 && freeArchiveIdentifier.MatchString(parts[0]) && movieYearPattern.MatchString(strings.ReplaceAll(parts[0], "-", " ")) {
		return "public_domain_movie_org", true
	}
	return "", false
}

func additionalFeatureSearchResults(data []byte, page *url.URL, query string) []CandidateSource {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil || page == nil {
		return nil
	}
	_, requestedYear := movieQuery(query)
	seen := map[string]bool{}
	var found []CandidateSource
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if availabilityIgnoredNode(node) || len(found) >= 6 {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" && node.Parent != nil && (node.Parent.Data == "h2" || node.Parent.Data == "h3") {
			label := availabilityNodeText(node)
			ref, err := url.Parse(streamingSiteAttribute(node, "href"))
			if err == nil {
				u := page.ResolveReference(ref)
				kind, target := additionalFreeResolutionTarget(u)
				canonical, year := movieTitleDetails(CandidateSource{Title: label})
				if target && u.Hostname() == page.Hostname() && !seen[u.String()] && sameAdditionalFeatureTitle(query, label) && (requestedYear == 0 || year == 0 || requestedYear == year) {
					provider := "ArcoirisTV"
					if kind == "public_domain_movie_org" {
						provider = "PublicDomainMovie.org"
					}
					if year == 0 {
						year = requestedYear // A requested year remains a constraint on the resolver.
					}
					found = append(found, CandidateSource{ID: resolvedFreeMovieID("catalog", u.String()), Title: canonical, ReleaseYear: year, URL: u.String(), Provider: provider, SourceType: "Feature film catalog", AccessType: "unknown", ListingOnly: true, DestinationType: "watch", Description: "A public feature-film catalog listing. Runtime, access and short playback observations are shown separately."})
					seen[u.String()] = true
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return found
}

// A movie article can add "Full Movie Review" after its release-year label
// while still embedding the feature. Its canonical title must match exactly;
// the independently checked media metadata must also identify the full film.
func sameAdditionalFeatureTitle(left, right string) bool {
	return lens.SameFeatureTitle(left, right)
}

func resolveAdditionalFreeMovie(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, bool) {
	page, safe := safeAvailabilityURL(source.URL)
	kind, _, supported := freeResolutionTarget(source)
	if !safe || !supported || kind != "arcoiris" && kind != "public_domain_movie_org" {
		return source, false
	}
	data, final, err := fetchFreeSourceDocument(ctx, client, source.URL)
	if err != nil || final.Hostname() != page.Hostname() || final.Path != page.Path || final.RawQuery != "" {
		return source, false
	}
	if kind == "arcoiris" {
		return resolveArcoirisFeature(data, final, source)
	}
	id, ok := publicDomainOrgArchiveItem(data, source)
	if !ok {
		return source, false
	}
	resolved, ok := resolveFreeArchiveSource(ctx, client, source, id, false)
	if !ok || mediaDurationSeconds(resolved.Duration) < minimumAdditionalFeatureDuration {
		return source, false
	}
	resolved.ID, resolved.Provider, resolved.SourceType = resolvedFreeMovieID("pdmorg", source.URL), "PublicDomainMovie.org", "Public Domain feature"
	resolved.AccessNote = "This movie page embeds the matching full-length Archive item. Public movie metadata lists its media; playback is checked separately and rights vary by item and country."
	return resolved, true
}

func streamingSiteAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func streamingSiteClass(node *html.Node, class string) bool {
	return strings.Contains(" "+strings.Join(strings.Fields(streamingSiteAttribute(node, "class")), " ")+" ", " "+class+" ")
}

func publicDomainOrgArchiveItem(data []byte, source CandidateSource) (string, bool) {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return "", false
	}
	items := map[string]bool{}
	headingMatches := false
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, primary bool) {
		if availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode {
			primary = primary || streamingSiteClass(node, "entry-content")
			if node.Data == "h1" {
				label := availabilityNodeText(node)
				_, pageYear := movieTitleDetails(CandidateSource{Title: label})
				_, sourceYear := movieTitleDetails(source)
				headingMatches = headingMatches || sameAdditionalFeatureTitle(source.Title, label) && (sourceYear == 0 || pageYear == sourceYear)
			}
			if primary && node.Data == "iframe" {
				if u, safe := safeAvailabilityURL(streamingSiteAttribute(node, "src")); safe && u.Scheme == "https" && u.Hostname() == "archive.org" && u.RawQuery == "" {
					parts := strings.Split(strings.Trim(u.Path, "/"), "/")
					if len(parts) == 2 && parts[0] == "embed" && freeArchiveIdentifier.MatchString(parts[1]) {
						items[parts[1]] = true
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, primary)
		}
	}
	walk(doc, false)
	if headingMatches && len(items) == 1 {
		for id := range items {
			return id, true
		}
	}
	return "", false
}

func resolveArcoirisFeature(data []byte, page *url.URL, source CandidateSource) (CandidateSource, bool) {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return source, false
	}
	type videoObject struct {
		Type        string `json:"@type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		ContentURL  string `json:"contentUrl"`
		EmbedURL    string `json:"embedUrl"`
	}
	var objects []videoObject
	duration := float64(0)
	runtimeCount := 0
	headingMatches := false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" && strings.EqualFold(streamingSiteAttribute(node, "type"), "application/ld+json") {
			visible := *node
			visible.Data = "span"
			if availabilityIgnoredNode(&visible) {
				return
			}
			var object videoObject
			if node.FirstChild != nil && json.Unmarshal([]byte(node.FirstChild.Data), &object) == nil && object.Type == "VideoObject" {
				objects = append(objects, object)
			}
			return
		}
		if availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode && node.Data == "h1" {
			headingMatches = headingMatches || sameAdditionalFeatureTitle(source.Title, availabilityNodeText(node))
		}
		if node.Type == html.ElementNode && node.Data == "li" {
			text := availabilityNodeText(node)
			if strings.Contains(text, "Fonte:") && (strings.Contains(text, "Public Domain") || strings.Contains(text, "archive.org")) {
				if match := arcoirisRuntimePattern.FindStringSubmatch(text); match != nil {
					minutes, _ := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
					duration = minutes * 60
					runtimeCount++
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if !headingMatches || duration < minimumAdditionalFeatureDuration || runtimeCount != 1 || len(objects) != 1 {
		return source, false
	}
	object := objects[0]
	embed, embedSafe := safeAvailabilityURL(object.EmbedURL)
	media, mediaSafe := safeAvailabilityURL(object.ContentURL)
	if !embedSafe || !mediaSafe || embed.String() != page.String() || !arcoirisPublicMedia(media) || !sameAdditionalFeatureTitle(source.Title, object.Name) || hasUnrequestedDerivative("movie", strings.ReplaceAll(path.Base(media.Path), "_", " ")) {
		return source, false
	}
	year := 0
	if match := leadingFeatureYear.FindStringSubmatch(strings.TrimSpace(object.Description)); match != nil {
		year, _ = strconv.Atoi(match[1])
	}
	_, requestedYear := movieTitleDetails(source)
	if requestedYear != 0 && year != requestedYear || !matchingFreeMovieMetadata(source, object.Name, year, duration) {
		return source, false
	}
	source.ID, source.Provider, source.SourceType = resolvedFreeMovieID("arcoiris", source.URL), "ArcoirisTV", "Public Domain feature"
	source.DirectStreamURL, source.KnownManifests = media.String(), []string{media.String()}
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "arcoiris_video_metadata", page.String()
	source.AccessNote = "This catalog's public video metadata lists a full-length movie and its public media. Playback is checked separately; rights vary by item and country."
	source.Duration, source.ListingOnly, source.DestinationType = strconv.FormatFloat(duration, 'f', -1, 64), false, "watch"
	if source.ReleaseYear == 0 {
		source.ReleaseYear = year
	}
	return source, true
}

func arcoirisPublicMedia(media *url.URL) bool {
	return media != nil && media.Scheme == "https" && media.Hostname() == "streaming.arcoiris.tv" && media.RawQuery == "" && media.Fragment == "" && path.Clean(media.Path) == media.Path && arcoirisMediaPath.MatchString(strings.ToLower(media.Path))
}

func additionalResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	kind, supported := additionalFreeResolutionTarget(page)
	if !supported || mediaDurationSeconds(source.Duration) < minimumAdditionalFeatureDuration {
		return false
	}
	if kind == "arcoiris" {
		return source.AccessBasis == "arcoiris_video_metadata" && source.Provider == "ArcoirisTV" && source.SourceType == "Public Domain feature" && strings.HasPrefix(source.ID, "arcoiris-resolved-") && evidence.String() == page.String() && arcoirisPublicMedia(media)
	}
	if source.AccessBasis != "archive_metadata" || source.Provider != "PublicDomainMovie.org" || source.SourceType != "Public Domain feature" || !strings.HasPrefix(source.ID, "pdmorg-resolved-") || evidence.Hostname() != "archive.org" || evidence.RawQuery != "" || media.Hostname() != "archive.org" || !freePublicMediaExtension(media.Path, false) {
		return false
	}
	id := strings.TrimPrefix(evidence.Path, "/metadata/")
	return freeArchiveIdentifier.MatchString(id) && evidence.Path == "/metadata/"+id && strings.HasPrefix(media.Path, "/download/"+id+"/")
}
