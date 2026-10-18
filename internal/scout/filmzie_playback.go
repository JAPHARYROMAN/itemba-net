package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var filmzieVideoID = regexp.MustCompile(`^[a-f0-9]{24}$`)
var errFilmziePlaybackTimeout = errors.New("Filmzie playback preflight timed out")

type filmzieVideoFormat struct {
	JobStatus string `json:"jobStatus"`
	Approved  bool   `json:"approved"`
	LegacyURL string `json:"legacyUrl"`
}

type filmzieMainVideo struct {
	ID       string                        `json:"id"`
	Type     string                        `json:"type"`
	Duration float64                       `json:"duration"`
	Formats  map[string]filmzieVideoFormat `json:"formats"`
}

type filmziePageState struct {
	ContentPage struct {
		Content struct {
			ID         string             `json:"id"`
			Type       string             `json:"type"`
			Title      string             `json:"title"`
			Duration   float64            `json:"duration"`
			Released   string             `json:"released"`
			ShareLink  string             `json:"shareLink"`
			SefName    string             `json:"sefName"`
			ComingSoon bool               `json:"comingSoon"`
			Videos     []filmzieMainVideo `json:"videos"`
		} `json:"content"`
		Available *bool `json:"isAvailableInUserCountry"`
	} `json:"contentPage"`
}

func filmzieMovieTarget(source CandidateSource) bool {
	u, safe := safeAvailabilityURL(source.URL)
	if !safe || u.Scheme != "https" || source.AccessType != "free" || source.Price != "" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return filmzieHost(u.Hostname()) && len(parts) == 2 && parts[0] == "content" && freeArchiveIdentifier.MatchString(parts[1])
}

func filmzieHost(host string) bool {
	return host == "filmzie.com" || host == "www.filmzie.com"
}

// Filmzie's observed public content bundle loads the PAID_CONTENT video (a
// provider asset label, not a purchase claim), then GETs /api/v1/video/stream/ID
// and passes data.source.hlsV2 to Video.js. TRAILER is a separate video type.
// Primary contracts checked 2026-10-18:
// https://filmzie.com/content/imprint-2007
// https://filmzie.com/js/content-bda3e39a4bc8daf94c3e.js
// Only existing public guest responses are read. Country, account and DRM
// restrictions stop the check; no alternate origin, identity or token is used.
func inspectFilmziePlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	result := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL, Detail: "Filmzie's full movie stream could not be identified with protection enabled."}
	if !filmzieMovieTarget(source) {
		return source, result
	}
	checkCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	pageData, final, status, err := fetchFilmziePlaybackDocument(checkCtx, client, source.URL, availabilityPageLimit)
	if err != nil || status != http.StatusOK {
		result.Detail = "Filmzie's public movie page could not be checked with protection enabled."
		if errors.Is(err, errFilmziePlaybackTimeout) {
			result.Reason, result.Detail = "timeout", "Filmzie's protected movie-page check timed out; playback remains unconfirmed."
		}
		return source, result
	}
	requested, _ := url.Parse(source.URL)
	if final.Path != requested.Path {
		return source, result
	}
	state, ok := parseFilmziePlaybackState(pageData)
	if !ok {
		return source, result
	}
	content := state.ContentPage.Content
	share, safe := safeAvailabilityURL(content.ShareLink)
	if !safe || share.Scheme != "https" || !filmzieHost(share.Hostname()) || share.Path != final.Path || path.Base(final.Path) != content.SefName || content.Type != "MOVIE" || content.ComingSoon || !filmzieVideoID.MatchString(content.ID) {
		return source, result
	}
	year := 0
	if released, err := time.Parse(time.RFC3339Nano, content.Released); err == nil {
		year = released.Year()
	}
	var mains []filmzieMainVideo
	for _, video := range content.Videos {
		if video.Type == "PAID_CONTENT" {
			mains = append(mains, video)
		}
	}
	if len(mains) != 1 {
		result.Detail = "Filmzie did not identify one full movie asset; trailer playback is not verification."
		return source, result
	}
	main := mains[0]
	if !filmzieVideoID.MatchString(main.ID) || !finiteFilmzieRuntime(content.Duration) || !finiteFilmzieRuntime(main.Duration) || math.Abs(main.Duration-content.Duration) > math.Max(60, content.Duration*.05) || !matchingFreeMovieMetadata(source, content.Title, year, main.Duration) {
		result.Detail = "Filmzie's title, release year or full movie runtime did not match this source."
		return source, result
	}
	// Matching page metadata remains useful even if the guest playback request
	// is restricted. A listed feature runtime is separate from decoded playback.
	source.Title, source.ReleaseYear = content.Title, year
	source.Duration = strconv.FormatFloat(main.Duration, 'f', -1, 64)
	apiURL := "https://filmzie.com/api/v1/video/stream/" + main.ID
	result.EvidenceURL = apiURL
	data, apiFinal, status, err := fetchFilmziePlaybackDocument(checkCtx, client, apiURL, 128<<10)
	if err != nil || apiFinal == nil || apiFinal.Hostname() != "filmzie.com" || apiFinal.Path != "/api/v1/video/stream/"+main.ID {
		result.Detail = "Filmzie's guest playback response could not be checked with protection enabled."
		if errors.Is(err, errFilmziePlaybackTimeout) {
			result.Reason, result.Detail = "timeout", "Filmzie's protected guest playback check timed out; playback remains unconfirmed."
		}
		return source, result
	}
	if status == http.StatusUnauthorized {
		result.Status, result.Reason, result.Detail = "not_started", "sign_in_required", "Filmzie requires an account or authorization for this movie; automatic playback did not proceed."
		return source, result
	}
	var payload struct {
		Error json.RawMessage `json:"error"`
		Data  struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Source struct {
				HLSV2 json.RawMessage `json:"hlsV2"`
			} `json:"source"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil {
		result.Detail = "Filmzie did not return a recognized guest playback response."
		if status == http.StatusForbidden || status == http.StatusTooManyRequests {
			result.Detail = "Filmzie restricted the automated guest playback request; playback remains unconfirmed."
		}
		return source, result
	}
	var providerError string
	_ = json.Unmarshal(payload.Error, &providerError)
	switch providerError {
	case "not_available_in_your_country", "content_not_available_in_your_country":
		result.Status, result.Reason, result.Detail = "not_started", "region_unavailable", "Filmzie reports that this movie is unavailable in the check network's country. No movie stream was requested."
		return source, result
	}
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		result.Status, result.Reason, result.Detail = "unconfirmed", "content_unconfirmed", "Filmzie restricted the automated guest playback request; playback remains unconfirmed."
		return source, result
	}
	rawError := strings.TrimSpace(string(payload.Error))
	hasError := rawError != "" && rawError != "null" && rawError != `""`
	if hasError || status != http.StatusOK {
		result.Detail = "Filmzie did not authorize a public main movie stream for this request."
		return source, result
	}
	if state.ContentPage.Available != nil && !*state.ContentPage.Available {
		result.Status, result.Reason, result.Detail = "not_started", "region_unavailable", "Filmzie's movie page marks this movie unavailable in the check network's country. No movie stream was requested."
		return source, result
	}
	if payload.Data.Type == "TRAILER" {
		result.Status, result.Reason, result.Detail = "not_started", "trailer_only", "Filmzie returned a trailer; it cannot verify full movie playback."
		return source, result
	}
	if payload.Data.Type != "PAID_CONTENT" || payload.Data.ID != "" && payload.Data.ID != main.ID {
		return source, result
	}
	stream, drm := filmzieHLSResponseSource(payload.Data.Source.HLSV2)
	if drm {
		result.Status, result.Reason, result.Detail = "not_started", "drm_required", "Filmzie's source requires DRM; the protected automatic sample did not proceed."
		return source, result
	}
	// A nonempty legacy movie file is an explicitly supplied member of the
	// full movie asset. Never derive an HTTP URL from gs: paths or storage keys.
	legacy := main.Formats["MP4_WURL"]
	if legacy.Approved && legacy.JobStatus == "SUCCESS" {
		if media, safe := safeAvailabilityURL(legacy.LegacyURL); safe && media.Scheme == "https" && (strings.EqualFold(path.Ext(media.Path), ".mp4") || strings.EqualFold(path.Ext(media.Path), ".webm")) {
			source = boundFilmzieSource(source, content.Title, year, main.Duration, apiURL, media.String(), false)
			return source, nil
		}
	}
	if stream != "" {
		if media, safe := safeAvailabilityURL(stream); safe && media.Scheme == "https" {
			ssai := main.Formats["HLS_SSAI"]
			if ssai.Approved && ssai.JobStatus == "SUCCESS" {
				source = boundFilmzieSource(source, content.Title, year, main.Duration, apiURL, media.String(), true)
				result.Detail = "Filmzie supplied the main movie's stream, but its server-inserted ads cannot be distinguished from movie playback with the current protected check. Playback remains unconfirmed."
				return source, result
			}
		}
	}
	result.Detail = "Filmzie did not supply an attributable public full movie stream. Trailer or ad playback is not movie verification."
	return source, result
}

func finiteFilmzieRuntime(value float64) bool {
	return value >= 40*60 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func boundFilmzieSource(source CandidateSource, title string, year int, duration float64, evidence, stream string, ssai bool) CandidateSource {
	source.ID = resolvedFreeMovieID("filmzie", source.URL)
	source.Title, source.Provider, source.SourceType = title, "Filmzie", "Provider free movie"
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "filmzie_movie_metadata", evidence
	source.Duration, source.ReleaseYear = strconv.FormatFloat(duration, 'f', -1, 64), year
	source.AccessNote = "A Filmzie guest response identified the full movie asset. Availability and actual protected playback are checked separately; the service may insert ads."
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, ssai, stream
	source.DirectStreamURL = ""
	source.KnownManifests = nil
	return source
}

func filmzieResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	if page == nil || media == nil || evidence == nil || source.Provider != "Filmzie" || source.SourceType != "Provider free movie" || source.AccessBasis != "filmzie_movie_metadata" || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != media.String() || !filmzieMovieTarget(CandidateSource{URL: page.String(), AccessType: "free"}) || evidence.Scheme != "https" || evidence.Host != "filmzie.com" || evidence.RawQuery != "" {
		return false
	}
	id := strings.TrimPrefix(evidence.Path, "/api/v1/video/stream/")
	_, safe := safeAvailabilityURL(media.String())
	return safe && media.Scheme == "https" && filmzieVideoID.MatchString(id) && evidence.Path == "/api/v1/video/stream/"+id
}

func parseFilmziePlaybackState(data []byte) (filmziePageState, bool) {
	var state filmziePageState
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return state, false
	}
	count := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.TextNode {
					continue
				}
				for _, marker := range []string{"window['APP_STATE'] =", "window[\"APP_STATE\"] ="} {
					if index := strings.Index(child.Data, marker); index >= 0 {
						var parsed filmziePageState
						if json.NewDecoder(strings.NewReader(child.Data[index+len(marker):])).Decode(&parsed) == nil {
							state = parsed
							count++
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
	return state, count == 1
}

func filmzieHLSResponseSource(raw json.RawMessage) (string, bool) {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct, false
	}
	var source struct {
		Src        string                     `json:"src"`
		KeySystems map[string]json.RawMessage `json:"keySystems"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return "", false
	}
	return source.Src, len(source.KeySystems) > 0
}

func fetchFilmziePlaybackDocument(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, *url.URL, int, error) {
	initial, safe := safeAvailabilityURL(raw)
	if !safe || initial.Scheme != "https" || !filmzieHost(initial.Hostname()) {
		return nil, nil, 0, errors.New("unsupported Filmzie metadata destination")
	}
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guarded := *client
	if guarded.Transport == nil {
		guarded.Transport = newMovieAvailabilityClient().Transport
	}
	guarded.Jar = nil
	guarded.Timeout = 4 * time.Second
	priorRedirect := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		u, safe := safeAvailabilityURL(req.URL.String())
		if len(via) >= 3 || !safe || u.Scheme != "https" || !filmzieHost(u.Hostname()) || u.Path != initial.Path {
			return errors.New("Filmzie metadata redirect changed destination")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, initial.String(), nil)
	if err != nil {
		return nil, nil, 0, err
	}
	request.Header.Set("Accept", "application/json,text/html;q=0.9")
	request.Header.Set("User-Agent", "ITEMBA-NET/0.1 (public movie playback check)")
	response, err := guarded.Do(request)
	if err != nil {
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
			return nil, nil, 0, errFilmziePlaybackTimeout
		}
		return nil, nil, 0, errors.New("Filmzie metadata request unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit || response.Request == nil || response.Request.URL == nil {
		return nil, nil, response.StatusCode, errors.New("Filmzie metadata response exceeded bounds")
	}
	final, safe := safeAvailabilityURL(response.Request.URL.String())
	if !safe || final.Scheme != "https" || !filmzieHost(final.Hostname()) || final.Path != initial.Path {
		return nil, nil, response.StatusCode, errors.New("Filmzie metadata changed destination")
	}
	return data, final, response.StatusCode, nil
}
