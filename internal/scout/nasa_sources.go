package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

const nasaMovieAPI = "https://plus.nasa.gov/wp-json/wp/v2/video"

type nasaMovieDocument struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Slug  string `json:"slug"`
	Link  string `json:"link"`
	Date  string `json:"date"`
	Title struct {
		Rendered string `json:"rendered"`
	} `json:"title"`
	Meta struct {
		VideoURL   string `json:"video-url"`
		Runtime    string `json:"runtime"`
		YouTubeURL string `json:"youtube-url"`
		PreviewURL string `json:"preview-url"`
	} `json:"meta"`
}

func nasaMovieTarget(source CandidateSource) bool {
	if source.Price != "" || source.AccessType != "" && source.AccessType != "free" && source.AccessType != "unknown" {
		return false
	}
	u, safe := safeAvailabilityURL(source.URL)
	if !safe || u.Scheme != "https" || u.Hostname() != "plus.nasa.gov" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && parts[0] == "video" && freeArchiveIdentifier.MatchString(parts[1])
}

func nasaMainMediaURL(raw string) (*url.URL, bool) {
	u, safe := safeAvailabilityURL(raw)
	return u, safe && u.Scheme == "https" && u.Host == "nasaplus.akamaized.net" && strings.HasPrefix(u.Path, "/output/") && strings.HasSuffix(u.Path, ".m3u8") && u.RawQuery == ""
}

// The public WordPress route is advertised by each NASA+ movie page's JSON
// alternate link. It supplies the main video-url separately from preview-url.
// Contract observed on Planetary Defenders (2025), 4508 seconds, 2026-10-18.
func (s *Scout) queryNASAMovieSites(ctx context.Context, query string) ([]CandidateSource, error) {
	title, _ := movieQuery(query)
	values := url.Values{"search": {title}, "per_page": {"20"}, "_fields": {"id,type,slug,link,date,title,meta"}}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	data, final, err := fetchMovieDocumentWithinLimit(ctx, client, nasaMovieAPI+"?"+values.Encode(), 256<<10)
	if err != nil {
		return nil, err
	}
	if final.Host != "plus.nasa.gov" || final.Path != "/wp-json/wp/v2/video" {
		return nil, nil
	}
	var documents []nasaMovieDocument
	if json.Unmarshal(data, &documents) != nil {
		return nil, errors.New("NASA+ returned an unreadable catalog")
	}
	var sources []CandidateSource
	for _, doc := range documents {
		candidate := CandidateSource{ID: "nasa-" + strconv.Itoa(doc.ID), Title: html.UnescapeString(doc.Title.Rendered), URL: doc.Link, Provider: "NASA+", SourceType: "Web Listing", Duration: doc.Meta.Runtime, AccessType: "unknown", ListingOnly: true, Region: "Unknown", Description: "NASA+ public film catalog. The matching main movie and protected playback are checked separately."}
		// Publication dates identify this catalog entry, not a commercial film's
		// original release. A later main-page check verifies documentary identity.
		if !nasaMovieTarget(candidate) || doc.ID <= 0 || doc.Type != "video" || knownShortMovie(candidate.Duration, 0) || mediaDurationSeconds(candidate.Duration) < 40*60 || relevanceScore(query, candidate) == 0 || hasUnrequestedDerivative(query, candidate.Title) {
			continue
		}
		sources = append(sources, candidate)
		if len(sources) == 6 {
			break
		}
	}
	return sources, nil
}

func inspectNASAPlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	check := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight", EvidenceURL: source.URL, Detail: "NASA+'s matching full documentary and main movie media could not be confirmed."}
	if !nasaMovieTarget(source) {
		return source, check
	}
	page, final, err := fetchFreeSourceDocument(ctx, client, source.URL)
	requested, _ := url.Parse(source.URL)
	if err != nil || final == nil || final.Host != requested.Host || final.Path != requested.Path {
		return source, check
	}
	apiURL, mainURL, title, documentary := parseNASAMoviePage(page, final)
	if !documentary || apiURL == "" || mainURL == "" || title == "" {
		return source, check
	}
	data, apiFinal, err := fetchMovieDocumentWithinLimit(ctx, client, apiURL, 128<<10)
	if err != nil || apiFinal == nil || apiFinal.String() != apiURL {
		return source, check
	}
	var doc nasaMovieDocument
	if json.Unmarshal(data, &doc) != nil || doc.ID <= 0 || apiURL != nasaMovieAPI+"/"+strconv.Itoa(doc.ID) || doc.Type != "video" || doc.Link != final.String() || doc.Slug != strings.TrimPrefix(strings.TrimSuffix(final.Path, "/"), "/video/") || doc.Meta.VideoURL != mainURL || doc.Meta.YouTubeURL != "" {
		return source, check
	}
	duration := mediaDurationSeconds(doc.Meta.Runtime)
	docTitle := html.UnescapeString(doc.Title.Rendered)
	if duration < 40*60 || !sameAdditionalFeatureTitle(title, docTitle) || !matchingFreeMovieMetadata(source, docTitle, 0, duration) {
		return source, check
	}
	if _, safe := nasaMainMediaURL(mainURL); !safe {
		return source, check
	}
	source.ID = resolvedFreeMovieID("nasa", source.URL)
	source.Title, source.Provider, source.SourceType = docTitle, "NASA+", "Provider free movie"
	source.Duration, source.AccessType, source.AccessBasis, source.AccessEvidenceURL = doc.Meta.Runtime, "free", "nasa_movie_metadata", apiURL
	source.AccessNote = "NASA+ publishes this full documentary for free without a subscription. A protected decoded sample is required to confirm a movie start."
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, false, mainURL
	source.DirectStreamURL, source.KnownManifests = "", nil
	return source, nil
}

func parseNASAMoviePage(data []byte, page *url.URL) (api, media, title string, documentary bool) {
	document, err := xhtml.Parse(bytes.NewReader(data))
	if err != nil {
		return
	}
	count := 0
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			if n.Data == "link" && attrs["rel"] == "alternate" && attrs["type"] == "application/json" {
				if u, safe := safeAvailabilityURL(attrs["href"]); safe && u.Scheme == "https" && u.Host == page.Host && strings.HasPrefix(u.Path, "/wp-json/wp/v2/video/") && moviePositiveID.MatchString(strings.TrimPrefix(u.Path, "/wp-json/wp/v2/video/")) && u.RawQuery == "" {
					api = u.String()
				}
			}
			if n.Data == "video" && attrs["id"] == "main-video" {
				count++
				if attrs["data-is-youtube"] == "false" && attrs["data-video-type"] == "application/x-mpegURL" {
					media, title = attrs["data-video-url"], attrs["title"]
					matched := false
					for child := n.FirstChild; child != nil; child = child.NextSibling {
						if child.Type == xhtml.ElementNode && child.Data == "source" {
							for _, a := range child.Attr {
								matched = matched || a.Key == "src" && a.Val == media
							}
						}
					}
					if !matched {
						media = ""
					}
				}
			}
			if n.Data == "script" && attrs["type"] == "application/ld+json" {
				var metadata struct {
					Headline       string `json:"headline"`
					URL            string `json:"url"`
					ArticleSection string `json:"articleSection"`
					Published      string `json:"datePublished"`
				}
				if n.FirstChild != nil && json.Unmarshal([]byte(n.FirstChild.Data), &metadata) == nil && metadata.URL == page.String() && strings.EqualFold(metadata.ArticleSection, "Documentaries") {
					if published, err := time.Parse(time.RFC3339, metadata.Published); err == nil && !published.After(time.Now()) {
						documentary = true
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	if count != 1 {
		media = ""
	}
	return
}

func nasaResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	if page == nil || media == nil || evidence == nil || source.AccessBasis != "nasa_movie_metadata" || source.Provider != "NASA+" || source.SourceType != "Provider free movie" || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != media.String() || !nasaMovieTarget(source) {
		return false
	}
	_, safe := nasaMainMediaURL(media.String())
	return safe && evidence.Scheme == "https" && evidence.Host == "plus.nasa.gov" && evidence.RawQuery == "" && strings.HasPrefix(evidence.Path, "/wp-json/wp/v2/video/") && moviePositiveID.MatchString(strings.TrimPrefix(evidence.Path, "/wp-json/wp/v2/video/"))
}
