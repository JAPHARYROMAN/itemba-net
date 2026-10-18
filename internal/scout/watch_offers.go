package scout

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// Plex also publishes guide pages for movies offered only by other services.
// A route or site-wide free-service statement cannot establish a free offer.
// Inspect only the matching movie's visible primary action before secondary
// sections. No scripts, trailer URLs, account state or private APIs are used.
func (s *Scout) confirmDirectWatchOffers(ctx context.Context, candidates []CandidateSource) ([]CandidateSource, ProviderStatus) {
	out := append([]CandidateSource(nil), candidates...)
	status := ProviderStatus{Name: "Direct movie offers", Status: "empty"}
	var targets []int
	seen := map[string]bool{}
	for index, candidate := range out {
		page, safe := safeAvailabilityURL(candidate.URL)
		if safe && plexMovieOfferPage(page) && candidate.DirectStreamURL == "" && candidate.Price == "" && (candidate.AccessType == "" || candidate.AccessType == "unknown") && !seen[candidate.URL] {
			targets = append(targets, index)
			seen[candidate.URL] = true
			if len(targets) == 3 {
				break
			}
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	var wg sync.WaitGroup
	for _, index := range targets {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			source := out[index]
			data, final, err := fetchMovieDocumentWithinLimit(checkCtx, client, source.URL, 1<<20)
			original, _ := url.Parse(source.URL)
			if err != nil || final.Host != original.Host || final.Path != original.Path || final.RawQuery != original.RawQuery || !plexPrimaryFreeOffer(data, source) {
				return
			}
			source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "page_text", source.URL
			source.AccessNote = "The matching movie page shows a primary Watch for Free action. This connection's offer may differ by country; playback has not been confirmed."
			source.Provider = "Plex"
			out[index] = source
		}(index)
	}
	wg.Wait()
	confirmed := 0
	for _, index := range targets {
		if out[index].AccessBasis == "page_text" {
			confirmed++
		}
	}
	status.Detail = fmt.Sprintf("%d explicit free movie offers from %d bounded primary-page checks; guide-only pages do not count as watch sources. Playback is checked separately.", confirmed, len(targets))
	if len(targets) > 0 {
		status.Status = "unconfirmed"
	}
	if confirmed > 0 {
		status.Status = "ok"
	}
	return out, status
}

func plexMovieOfferPage(page *url.URL) bool {
	if page == nil || page.Scheme != "https" || page.Hostname() != "watch.plex.tv" || page.RawQuery != "" {
		return false
	}
	parts := strings.Split(strings.Trim(page.Path, "/"), "/")
	if len(parts) == 3 && len(parts[0]) == 5 && parts[0][2] == '-' {
		parts = parts[1:]
	}
	return len(parts) == 2 && parts[0] == "movie" && parts[1] != "" && !movieGenericRoute(parts[1])
}

func plexPrimaryFreeOffer(data []byte, source CandidateSource) bool {
	page, safe := safeAvailabilityURL(source.URL)
	if !safe || !plexMovieOfferPage(page) {
		return false
	}
	parsed := parseAvailabilityPage(string(data))
	if parsed.challenge || parsed.signIn || availabilitySoftNotFound(source, parsed) || !availabilityMovieMatches(source, parsed, page.Hostname()) {
		return false
	}
	for _, block := range parsed.blocks {
		if availabilityRegionBlock(block, "") {
			return false
		}
	}
	document, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return false
	}
	matchedHeading, secondary, free := false, false, false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if secondary || availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode {
			if matchedHeading && (node.Data == "h2" || node.Data == "h3") {
				secondary = true
				return
			}
			if node.Data == "h1" {
				label := availabilityNodeText(node)
				matchedHeading = sameAdditionalFeatureTitle(source.Title, label)
			}
			if matchedHeading && (node.Data == "button" || node.Data == "a") {
				label := strings.ToLower(strings.Join(titleWords(availabilityNodeText(node)), " "))
				if label == "watch for free" || label == "watch free" {
					allowed := node.Data == "button"
					if node.Data == "a" {
						ref, err := url.Parse(streamingSiteAttribute(node, "href"))
						if err == nil {
							target := page.ResolveReference(ref)
							allowed = target.Scheme == "https" && target.Host == page.Host && target.Path == "/watch/movie" && !strings.Contains(strings.ToLower(target.RawQuery), "extras")
						}
					}
					free = free || allowed
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return matchedHeading && free
}
