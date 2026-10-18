package scout

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	creatorSederPage   = "https://sedermasochism.com/download/"
	creatorSederPlayer = "https://www.catsuka.com/player/Seder_Masochism"
	creatorSederMedia  = "https://www.catsuka.com/videos/files/Seder_Masochism.mp4"
)

// The filmmaker's public download page explicitly links this publisher's full
// movie player. The same creator identifies the film as a 2018, 78-minute
// feature. Neither a generic Catsuka upload nor a guessed media URL qualifies.
// Primary sources checked 2026-10-18:
// https://sedermasochism.com/download/
// https://sedermasochism.com/ (2018, 78-minute feature and public release)
// https://www.catsuka.com/player/Seder_Masochism
func creatorMovieTarget(source CandidateSource) bool {
	if source.Price != "" || source.AccessType != "" && source.AccessType != "free" && source.AccessType != "unknown" {
		return false
	}
	u, safe := safeAvailabilityURL(source.URL)
	if !safe || u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host, path := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), strings.TrimRight(u.Path, "/")
	return host == "sedermasochism.com" && (path == "" || path == "/download") || host == "catsuka.com" && path == "/player/Seder_Masochism"
}

func creatorMovieCandidates(query string) []CandidateSource {
	source := CandidateSource{
		ID: "creator-seder-masochism", Title: "Seder-Masochism", URL: creatorSederPage,
		Provider: "Nina Paley / Catsuka", SourceType: "Creator movie listing", AccessType: "free",
		ReleaseYear: 2018, Duration: "78:00", ListingOnly: true, DestinationType: "watch", Region: "Unknown",
		AccessBasis: "creator_catalog", AccessEvidenceURL: creatorSederPage,
		AccessNote:  "The filmmaker's official site links the free full feature. Actual playback is checked separately.",
		Description: "Nina Paley's independently made animated musical feature. The official creator page links its published full movie player.",
	}
	if relevanceScore(query, source) > 0 {
		return []CandidateSource{source}
	}
	return nil
}

func inspectCreatorPlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	check := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: creatorSederPage, Detail: "The creator's published full movie could not be identified with protection enabled."}
	if !creatorMovieTarget(source) {
		return source, check
	}
	if source.Title != "" && (relevanceScore("Seder-Masochism 2018", source) == 0 || source.ReleaseYear != 0 && source.ReleaseYear != 2018) {
		check.Detail = "The creator's identified feature does not match this movie listing. No media was requested."
		return source, check
	}
	checkCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	creator, final, err := fetchCreatorPlaybackDocument(checkCtx, client, creatorSederPage)
	if err != nil || !creatorURLMatches(final, creatorSederPage) || !creatorAuthorizesSederPlayer(creator) {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			check.Reason, check.Detail = "timeout", "The creator's protected movie-page check timed out; playback remains unconfirmed."
		}
		return source, check
	}
	player, final, err := fetchCreatorPlaybackDocument(checkCtx, client, creatorSederPlayer)
	if err != nil || !creatorURLMatches(final, creatorSederPlayer) {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			check.Reason, check.Detail = "timeout", "The publisher's protected full movie check timed out; playback remains unconfirmed."
		}
		return source, check
	}
	media, thumbnail, ok := creatorSederPlayerMedia(player)
	if !ok {
		check.Detail = "The creator-authorized publisher did not identify its full movie video. Trailers and unrelated uploads do not qualify."
		return source, check
	}
	source.ID = resolvedFreeMovieID("creator", creatorSederPlayer)
	source.Title, source.ReleaseYear, source.Duration = "Seder-Masochism", 2018, "78:00"
	source.URL, source.Provider, source.SourceType = creatorSederPlayer, "Nina Paley / Catsuka", "Provider free movie"
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "creator_movie_metadata", creatorSederPage
	source.Description = "Nina Paley's 2018 animated feature, offered through the full movie player linked by the filmmaker's official website."
	source.AccessNote = "The filmmaker explicitly links this free full feature. A protected native video sample is required to confirm playback."
	source.DestinationType, source.Thumbnail = "watch", thumbnail
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, false, media
	source.DirectStreamURL, source.KnownManifests = "", nil
	return source, nil
}

func creatorURLMatches(u *url.URL, expected string) bool {
	want, err := url.Parse(expected)
	return err == nil && u != nil && u.Scheme == "https" && strings.EqualFold(strings.TrimPrefix(u.Hostname(), "www."), strings.TrimPrefix(want.Hostname(), "www.")) && strings.TrimRight(u.Path, "/") == strings.TrimRight(want.Path, "/") && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func fetchCreatorPlaybackDocument(ctx context.Context, client *http.Client, raw string) ([]byte, *url.URL, error) {
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guarded := *client
	priorRedirect := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !creatorURLMatches(req.URL, raw) {
			return errors.New("creator movie metadata changed destination")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	return fetchMovieDocumentWithinLimit(ctx, &guarded, raw, availabilityPageLimit)
}

func creatorAuthorizesSederPlayer(data []byte) bool {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return false
	}
	authorized := false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					u, safe := safeAvailabilityURL(attr.Val)
					authorized = authorized || safe && creatorURLMatches(u, creatorSederPlayer)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return authorized
}

func creatorSederPlayerMedia(data []byte) (string, string, bool) {
	parsed := parseAvailabilityPage(string(data))
	title := strings.ToLower(parsed.title)
	if parsed.challenge || parsed.signIn || !strings.Contains(title, "seder-masochism") || !strings.Contains(title, "nina paley") || hasUnrequestedDerivative("Seder-Masochism", parsed.title) {
		return "", "", false
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", "", false
	}
	count, sources := 0, 0
	media, thumbnail := "", ""
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode && node.Data == "video" {
			count++
			correctID := false
			for _, attr := range node.Attr {
				correctID = correctID || attr.Key == "id" && attr.Val == "div_video"
				if attr.Key == "poster" {
					if u, safe := safeAvailabilityURL(attr.Val); safe && u.Hostname() == "www.catsuka.com" {
						thumbnail = u.String()
					}
				}
			}
			if !correctID {
				return
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.ElementNode || child.Data != "source" {
					continue
				}
				for _, attr := range child.Attr {
					if attr.Key == "src" {
						sources++
						if u, safe := safeAvailabilityURL(attr.Val); safe && u.String() == creatorSederMedia {
							media = u.String()
						}
					}
				}
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return media, thumbnail, count == 1 && sources == 1 && media != ""
}

func creatorResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	return source.Provider == "Nina Paley / Catsuka" && source.SourceType == "Provider free movie" && source.AccessBasis == "creator_movie_metadata" && source.providerContentBound && !source.providerSSAI && source.providerStreamURL == creatorSederMedia && media != nil && media.String() == creatorSederMedia && creatorURLMatches(page, creatorSederPlayer) && creatorURLMatches(evidence, creatorSederPage)
}
