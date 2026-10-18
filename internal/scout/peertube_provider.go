package scout

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

const blenderMovieSearchEndpoint = "https://video.blender.org/api/v1/search/videos"

// queryBlenderMovies uses the public search contract of Blender's own PeerTube
// instance. It remains available when a global federation index is down, and
// needs no account or API key. Search results alone never establish free access
// or playback; the bounded video-metadata resolver supplies those observations.
// https://docs.joinpeertube.org/api-rest-reference.html#tag/Search/operation/searchVideos
// The actual endpoint and public result schema were checked 2026-10-18.
func (s *Scout) queryBlenderMovies(ctx context.Context, query string) ([]CandidateSource, error) {
	term, _ := movieQuery(query)
	if term == "" {
		return nil, errors.New("movie search requires keywords")
	}
	searchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	params := url.Values{"search": {term}, "count": {"6"}, "start": {"0"}, "nsfw": {"false"}, "isLocal": {"true"}}
	data, final, err := fetchFreeSourceDocument(searchCtx, s.availabilityHTTPClient, blenderMovieSearchEndpoint+"?"+params.Encode())
	if err != nil || final.Scheme != "https" || final.Host != "video.blender.org" || final.Path != "/api/v1/search/videos" {
		return nil, errors.New("official movie catalog unavailable")
	}
	var payload struct {
		Data []struct {
			UUID                  string `json:"uuid"`
			Name                  string `json:"name"`
			URL                   string `json:"url"`
			Description           string `json:"description"`
			Duration              int    `json:"duration"`
			IsLive                bool   `json:"isLive"`
			PasswordProtected     bool   `json:"passwordProtected"`
			OriginallyPublishedAt string `json:"originallyPublishedAt"`
			Privacy               struct {
				ID int `json:"id"`
			} `json:"privacy"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Data == nil {
		return nil, errors.New("invalid official movie catalog response")
	}
	var out []CandidateSource
	seen := map[string]bool{}
	for _, video := range payload.Data {
		if len(out) == 6 {
			break
		}
		if len(video.UUID) != 36 || !freePeerTubeID.MatchString(video.UUID) || video.Name == "" || video.Duration < 180 || video.IsLive || video.PasswordProtected || video.Privacy.ID != 1 || seen[video.UUID] {
			continue
		}
		page := "https://video.blender.org/videos/watch/" + video.UUID
		if video.URL != "" {
			u, safe := safeAvailabilityURL(video.URL)
			if !safe || u.Scheme != "https" || u.Host != "video.blender.org" || u.Path != "/videos/watch/"+video.UUID || u.RawQuery != "" {
				continue
			}
		}
		description := "Listing from Blender's official public video catalog. Movie access and playback are checked separately."
		if text := strings.TrimSpace(video.Description); text != "" {
			runes := []rune(text)
			if len(runes) > 300 {
				text = string(runes[:300]) + "…"
			}
			description += " " + text
		}
		source := CandidateSource{
			ID: "pt-blender-" + video.UUID, Title: normalizeBlenderMovieTitle(video.Name), URL: page,
			Provider: "PeerTube (video.blender.org)", SourceType: "Federated PeerTube",
			Description: description, Duration: (time.Duration(video.Duration) * time.Second).String(),
			AccessType: "unknown", ListingOnly: true, AccessNote: "A public catalog listing; the video metadata and actual playback have not been checked yet.",
		}
		if published, err := time.Parse(time.RFC3339, video.OriginallyPublishedAt); err == nil {
			source.ReleaseYear = published.Year()
		}
		if relevanceScore(query, source) == 0 {
			continue
		}
		seen[video.UUID] = true
		out = append(out, source)
	}
	return out, nil
}

// These are branding/encoding wrappers used on the official instance, not
// general words to erase from arbitrary movie titles or search results.
func normalizeBlenderMovieTitle(title string) string {
	title = strings.TrimSpace(title)
	for _, suffix := range []string{
		" - Third Open Movie by Blender Foundation", " - Blender VFX Open Movie",
		" - Official Blender Foundation Short Film", " - Blender animated cartoon",
		" - Funny 3D Animated Short", " - Blender Animated Short",
		". Official Blender Foundation release.",
	} {
		if strings.HasSuffix(strings.ToLower(title), strings.ToLower(suffix)) && len(title) > len(suffix) {
			title = strings.TrimSpace(title[:len(title)-len(suffix)])
			break
		}
	}
	if strings.HasSuffix(title, " 60fps 4K") {
		title = strings.TrimSpace(strings.TrimSuffix(title, " 60fps 4K"))
	}
	return title
}
