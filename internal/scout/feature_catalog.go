package scout

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const maxArchiveMovieMetadata = 6

type archiveMovieDocument struct {
	Identifier string `json:"identifier"`
	Title      any    `json:"title"`
	Year       any    `json:"year"`
	Collection any    `json:"collection"`
	Runtime    any    `json:"runtime"`
}

// The documented Advanced Search endpoint accepts title/mediatype fields,
// sorting and selected metadata fields. Metadata is only a discovery hint;
// public file identity and full edition length are checked separately.
// https://archive.org/developers/ (Item Search APIs), checked 2026-10-18.
func archiveMovieSearchURL(query string) string {
	title, _ := movieQuery(query)
	// Treat user input as a literal title phrase, never Lucene operators.
	title = strings.NewReplacer(`\`, " ", `"`, " ", "\r", " ", "\n", " ").Replace(title)
	values := url.Values{
		"q":    {`title:("` + title + `") AND mediatype:movies`},
		"fl[]": {"identifier,title,year,collection,runtime"},
		"rows": {"30"}, "page": {"1"}, "output": {"json"}, "sort[]": {"downloads desc"},
	}
	return "https://archive.org/advancedsearch.php?" + values.Encode()
}

func archiveFeatureCandidates(query string, documents []archiveMovieDocument) []CandidateSource {
	type match struct {
		source CandidateSource
		score  int
	}
	var matches []match
	seen := map[string]bool{}
	for _, doc := range documents {
		if !freeArchiveIdentifier.MatchString(doc.Identifier) || seen[doc.Identifier] {
			continue
		}
		title := scalarMetadataString(doc.Title)
		if title == "" || archiveNonMovieEdition(query, title) {
			continue
		}
		year, _ := strconv.Atoi(movieYearPattern.FindString(scalarMetadataString(doc.Year)))
		candidate := CandidateSource{
			ID: "ia-discovered-" + doc.Identifier, Title: title,
			URL:      "https://archive.org/details/" + url.PathEscape(doc.Identifier),
			Provider: "Internet Archive", SourceType: "Archive video", AccessType: "unknown",
			AccessNote:  "Archive listing; public full movie media and playback are unconfirmed until checked.",
			Description: "Movie listing from Internet Archive. Access and rights depend on the item.",
			Thumbnail:   "https://archive.org/services/img/" + url.PathEscape(doc.Identifier),
			Duration:    scalarMetadataString(doc.Runtime), ReleaseYear: year, ListingOnly: true,
		}
		score := relevanceScore(query, candidate)
		if score == 0 {
			continue
		}
		// Full feature collections improve coverage without excluding independent
		// community films. A known full runtime outranks otherwise equal shorts.
		for _, collection := range archiveMetadataStrings(doc.Collection) {
			switch strings.ToLower(collection) {
			case "feature_films", "moviesandfilms", "thevideocellarcollection":
				score += 15
			}
		}
		if mediaDurationSeconds(candidate.Duration) >= 40*60 {
			score += 10
		}
		seen[doc.Identifier] = true
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

func archiveMetadataStrings(raw any) []string {
	if text, ok := raw.(string); ok {
		return []string{text}
	}
	var out []string
	if values, ok := raw.([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func archiveNonMovieEdition(query, title string) bool {
	words := " " + strings.Join(titleWords(title), " ") + " "
	requested := " " + strings.Join(titleWords(query), " ") + " "
	for _, phrase := range []string{"dvd menu", "dvd menus", "disc menu", "disc menus", "collection", "compilation", "sample", "preview", "promo", "double feature", "triple feature"} {
		if strings.Contains(words, " "+phrase+" ") && !strings.Contains(requested, " "+phrase+" ") {
			return true
		}
	}
	return hasUnrequestedDerivative(query, title)
}

// Public Domain Movies has an observed browse catalog, not a public keyword
// API. Read its real title links and at most two advertised continuation pages;
// never guess slugs or claim that the catalog's shorts are full-length films.
// Source: https://publicdomainmovie.net/feature_movies (checked 2026-10-18).
func (s *Scout) queryPublicDomainFeatureCatalog(ctx context.Context, query string) ([]CandidateSource, error) {
	catalogCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	data, final, err := fetchFreeSourceDocument(catalogCtx, client, "https://publicdomainmovie.net/feature_movies")
	if err != nil {
		return nil, err
	}
	if !publicDomainCatalogPage(final) {
		return nil, fmt.Errorf("public movie catalog changed destination")
	}
	candidates, pages := publicDomainFeaturePage(data, final, query)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, page := range pages {
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			data, final, err := fetchFreeSourceDocument(catalogCtx, client, raw)
			if err != nil || !publicDomainCatalogPage(final) {
				return
			}
			found, _ := publicDomainFeaturePage(data, final, query)
			mu.Lock()
			candidates = append(candidates, found...)
			mu.Unlock()
		}(page)
	}
	wg.Wait()
	// Identical cards and repeated pagination results must not consume later
	// source-resolution or playback slots.
	out := make([]CandidateSource, 0, 3)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if !seen[candidate.URL] {
			out = append(out, candidate)
			seen[candidate.URL] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := relevanceScore(query, out[i]), relevanceScore(query, out[j])
		if left != right {
			return left > right
		}
		return out[i].URL < out[j].URL
	})
	if len(out) > 3 {
		out = out[:3]
	}
	return out, nil
}

func publicDomainCatalogPage(page *url.URL) bool {
	return page != nil && (page.Hostname() == "publicdomainmovie.net" || page.Hostname() == "www.publicdomainmovie.net") && page.Path == "/feature_movies"
}

func publicDomainFeaturePage(data []byte, page *url.URL, query string) ([]CandidateSource, []string) {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, nil
	}
	var candidates []CandidateSource
	var pages []string
	seen, seenPages := map[string]bool{}, map[string]bool{}
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, inCatalog bool) {
		if node.Type == html.ElementNode {
			if node.Data == "script" || node.Data == "style" || node.Data == "nav" || node.Data == "footer" {
				return
			}
			if featureNodeClass(node, "view-content") {
				inCatalog = true
			}
			if node.Data == "a" {
				var href string
				for _, attr := range node.Attr {
					if attr.Key == "href" {
						href = attr.Val
					}
				}
				ref, parseErr := url.Parse(href)
				if href != "" && parseErr == nil {
					target := page.ResolveReference(ref)
					if publicDomainCatalogPage(target) && target.Query().Get("page") != "" && !seenPages[target.String()] && len(pages) < 2 {
						if number, e := strconv.Atoi(target.Query().Get("page")); e == nil && number > 0 && number <= 2 {
							pages = append(pages, target.String())
							seenPages[target.String()] = true
						}
					}
					parts := strings.Split(strings.Trim(target.Path, "/"), "/")
					if inCatalog && (target.Hostname() == "publicdomainmovie.net" || target.Hostname() == "www.publicdomainmovie.net") && target.Scheme == "https" && target.User == nil && target.RawQuery == "" && len(parts) == 2 && parts[0] == "movie" && freeArchiveIdentifier.MatchString(parts[1]) {
						title := availabilityNodeText(node)
						candidate := CandidateSource{ID: resolvedFreeMovieID("pdm-catalog", target.String()), Title: title, URL: target.String(), Provider: "Public Domain Movies", SourceType: "Public Domain", AccessType: "free", ListingOnly: true, Description: "Movie found in the public catalog. Media identity, full runtime and playback are checked separately.", AccessNote: "Free catalog listing; availability is checked separately."}
						// The observed card stores release year in a nearby /year?v=
						// link. Use the enclosing details block, never the full page.
						for parent := node.Parent; parent != nil; parent = parent.Parent {
							if featureNodeClass(parent, "details") {
								candidate.ReleaseYear = publicDomainCardYear(parent)
								break
							}
							if parent.Data == "td" {
								break
							}
						}
						if title != "" && !archiveNonMovieEdition(query, title) && relevanceScore(query, candidate) > 0 && !seen[candidate.URL] {
							candidates = append(candidates, candidate)
							seen[candidate.URL] = true
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inCatalog)
		}
	}
	walk(doc, false)
	return candidates, pages
}

func featureNodeClass(node *html.Node, class string) bool {
	for _, attr := range node.Attr {
		if attr.Key == "class" && strings.Contains(" "+attr.Val+" ", " "+class+" ") {
			return true
		}
	}
	return false
}

func publicDomainCardYear(card *html.Node) int {
	year := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					if u, e := url.Parse(attr.Val); e == nil && u.Path == "/year" {
						value, _ := strconv.Atoi(u.Query().Get("v"))
						if value >= 1880 && value <= time.Now().Year()+1 {
							year = value
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(card)
	return year
}
