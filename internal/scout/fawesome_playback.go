package scout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var fawesomeMoviePath = regexp.MustCompile(`^/movies/([1-9][0-9]{0,11})/([a-z0-9]+(?:-[a-z0-9]+)*)/?$`)
var fawesomeNodeAssignment = regexp.MustCompile(`\bvar\s+nodeId\s*=\s*`)
var fawesomeMediaAssignment = regexp.MustCompile(`\bvar\s+dvvideo_url\s*=\s*`)
var fawesomeMovieAssignment = regexp.MustCompile(`\bwindow\.deeplinkMovieInfo\s*=\s*`)
var fawesomeISODuration = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?$`)
var fawesomeMainHLSPath = regexp.MustCompile(`^/files/[a-z0-9]+/vi/[a-f0-9]{2}/[a-f0-9]{2}/([1-9][0-9]{0,11})/s([1-9][0-9]{0,11})\.m3u8$`)

type fawesomeMovieInfo struct {
	NodeID      int64   `json:"node_id"`
	Title       string  `json:"title"`
	WebURL      string  `json:"web_url"`
	ItemType    string  `json:"item_type"`
	FeedType    string  `json:"feed_type"`
	ActionType  string  `json:"actiontype"`
	ReleaseDate string  `json:"release_date"`
	Runtime     float64 `json:"runtime"`
	DRM         *int    `json:"drm"`
	Ad          string  `json:"ad"`
	BlockItem   string  `json:"blockItem"`
	VideoURL    string  `json:"video_url"`
	VideoHLSURL string  `json:"video_hls_url"`
	VideoFLVURL string  `json:"video_flv_url"`
}

type fawesomeSchema struct {
	Type          string `json:"@type"`
	Name          string `json:"name"`
	Duration      string `json:"duration"`
	DatePublished string `json:"datePublished"`
	UploadDate    string `json:"uploadDate"`
	URL           string `json:"url"`
	EmbedURL      string `json:"embedUrl"`
}

func fawesomeMovieTarget(source CandidateSource) bool {
	u, safe := safeAvailabilityURL(source.URL)
	return safe && u.String() == source.URL && u.Scheme == "https" && (u.Host == "fawesome.tv" || u.Host == "www.fawesome.tv") && u.RawQuery == "" && u.Fragment == "" && fawesomeMoviePath.MatchString(u.Path) && source.Price == "" && (source.AccessType == "" || source.AccessType == "unknown" || source.AccessType == "free")
}

// Fawesome's public movie page binds nodeId and dvvideo_url to one
// window.deeplinkMovieInfo movie. Its own public player base64-decodes that
// string and XORs it with the literal below; this is URL representation, not
// authentication. Contracts observed 2026-10-18 on:
// https://fawesome.tv/movies/10540876/abduction
// https://fawesome.tv/movies/10829879/the-shape-of-shadows-2023
// https://fawesome.tv/home/new/v467/assets/js/iptv-main.js?ver=1236
// No private API, account, playback license or alternate country is used.
// A separate native sample must confirm full-feature runtime and real frames.
func inspectFawesomePlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	check := &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Method: "provider_playback_preflight", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL, Detail: "Fawesome's full movie asset could not be identified with protection enabled."}
	if !fawesomeMovieTarget(source) {
		return source, check
	}
	// Every preflight requires a current public-page binding; an earlier main
	// asset cannot survive a changed, blocked or inconsistent provider response.
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = false, false, ""
	data, final, err := fetchFreeSourceDocument(ctx, client, source.URL)
	if err != nil || final == nil {
		if ctx.Err() != nil {
			check.Reason, check.Detail = "timeout", "Fawesome's public movie-page check timed out; playback remains unconfirmed."
		}
		return source, check
	}
	page, _ := url.Parse(source.URL)
	if final.Hostname() != page.Hostname() || final.Path != page.Path || final.RawQuery != "" {
		return source, check
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return source, check
	}
	visible := strings.ToLower(availabilityNodeText(doc))
	if strings.Contains(visible, "this content is not available in your region") {
		check.Status, check.Reason, check.Detail = "not_started", "region_unavailable", "Fawesome reports this movie unavailable in the check network's country. No movie stream was requested."
		return source, check
	}
	info, encoded, schemas, ok := parseFawesomeMoviePage(doc)
	if !ok {
		return source, check
	}
	parts := fawesomeMoviePath.FindStringSubmatch(page.Path)
	if strconv.FormatInt(info.NodeID, 10) != parts[1] || info.WebURL != strings.TrimPrefix(page.Path, "/") || info.ItemType != "movie" || info.FeedType != "video" || info.ActionType != "video" {
		return source, check
	}
	if info.DRM == nil || *info.DRM != 0 {
		check.Status, check.Reason, check.Detail = "not_started", "drm_required", "Fawesome did not identify a DRM-free main movie asset; automatic playback did not proceed."
		return source, check
	}
	if info.BlockItem != "false" {
		check.Status, check.Reason, check.Detail = "unconfirmed", "content_unconfirmed", "Fawesome marks this movie blocked for the current guest request. The reason is unconfirmed and no movie stream was requested."
		return source, check
	}
	// The observed main-file guest contract says ad=no, while ad_breaks and
	// trailer_url are separate player inputs. Do not select those other assets,
	// replace an unavailable source, or accept an unclassified stitched stream.
	if info.Ad != "no" {
		return source, check
	}
	// These page/schema dates can describe a later catalog release. They
	// cannot establish or contradict the movie's independently supplied year.
	if !finiteFawesomeRuntime(info.Runtime) || !sameAdditionalFeatureTitle(source.Title, info.Title) || !matchingFreeMovieMetadata(source, info.Title, 0, info.Runtime) || !fawesomeSchemasMatch(schemas, info, page) {
		check.Detail = "Fawesome's movie title, node identity, full movie runtime or catalog dates did not match consistently."
		return source, check
	}
	stream, safe := decodeFawesomePublicMovieURL(encoded)
	// Some current movie pages also expose the identical main file in the
	// unencoded video_url/video_flv_url fields. Those must agree if supplied.
	if !safe || info.VideoURL != "" && info.VideoURL != stream || info.VideoFLVURL != "" && info.VideoFLVURL != stream {
		return source, check
	}
	// The normal first-party player prefers e.video_hls_url over the MP4 for
	// this DRM-free movie. It is a declared main source, separate from the
	// trailer field. Never generate an HLS path from the MP4, or fall back to a
	// different asset when a supplied main HLS URL fails the observed contract.
	if info.VideoHLSURL != "" {
		hls, valid := fawesomeDeclaredMainHLS(info.VideoHLSURL, info.NodeID)
		if !valid {
			return source, check
		}
		stream = hls
	}
	source.ID = resolvedFreeMovieID("fawesome", source.URL)
	source.Title, source.Duration = info.Title, strconv.FormatFloat(info.Runtime, 'f', -1, 64)
	source.Provider, source.SourceType = "Fawesome", "Provider free movie"
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "fawesome_movie_metadata", source.URL
	source.AccessNote = "Fawesome's public guest movie page identifies its DRM-free main movie source. A separate protected native sample checks playback; the service's normal player may request ads."
	if source.ReleaseYear == 0 {
		source.AccessNote += " The original release year is unconfirmed; a catalog date does not establish the film's edition."
	}
	source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, false, stream
	source.DirectStreamURL, source.KnownManifests = "", nil
	return source, nil
}

func finiteFawesomeRuntime(duration float64) bool {
	return duration >= 40*60 && duration <= 12*60*60 && !math.IsNaN(duration) && !math.IsInf(duration, 0)
}

func decodeFawesomePublicMovieURL(encoded string) (string, bool) {
	if len(encoded) == 0 || len(encoded) > 4096 {
		return "", false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", false
	}
	key := []byte("ft-iptv-website-#$#$")
	for i := range decoded {
		decoded[i] ^= key[i%len(key)]
	}
	raw := string(decoded)
	u, safe := safeAvailabilityURL(raw)
	// This adapter supports only the observed main-file CDN contract, never an
	// arbitrary decrypted URL, alternate host or generated storage-path variant.
	return raw, safe && u.String() == raw && u.Scheme == "https" && u.Host == "ftmain.cachefly.net" && strings.HasPrefix(u.Path, "/files/") && !strings.Contains(u.Path, "..") && strings.EqualFold(path.Ext(u.Path), ".mp4") && u.Fragment == ""
}

func fawesomeDeclaredMainHLS(raw string, nodeID int64) (string, bool) {
	u, safe := safeAvailabilityURL(raw)
	if !safe || u.String() != raw || u.Scheme != "https" || u.Host != "ftmain.cachefly.net" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	parts := fawesomeMainHLSPath.FindStringSubmatch(u.Path)
	id := strconv.FormatInt(nodeID, 10)
	return raw, len(parts) == 3 && parts[1] == id && parts[2] == id
}

func fawesomeResolvedPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	if page == nil || media == nil || evidence == nil || source.Provider != "Fawesome" || source.SourceType != "Provider free movie" || source.AccessBasis != "fawesome_movie_metadata" || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != media.String() || source.AccessEvidenceURL != source.URL || page.String() != source.URL || evidence.String() != source.URL || !fawesomeMovieTarget(source) || !finiteFawesomeRuntime(mediaDurationSeconds(source.Duration)) {
		return false
	}
	_, safe := safeAvailabilityURL(media.String())
	if strings.EqualFold(path.Ext(media.Path), ".m3u8") {
		parts := fawesomeMoviePath.FindStringSubmatch(page.Path)
		if len(parts) != 3 {
			return false
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return false
		}
		_, bound := fawesomeDeclaredMainHLS(media.String(), id)
		return bound
	}
	return safe && media.Scheme == "https" && media.Host == "ftmain.cachefly.net" && strings.HasPrefix(media.Path, "/files/") && !strings.Contains(media.Path, "..") && strings.EqualFold(path.Ext(media.Path), ".mp4") && media.Fragment == ""
}

func parseFawesomeMoviePage(doc *html.Node) (fawesomeMovieInfo, string, []fawesomeSchema, bool) {
	var info fawesomeMovieInfo
	var encoded string
	var schemas []fawesomeSchema
	bindings := 0
	assignments := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "script" {
			var text strings.Builder
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.TextNode {
					text.WriteString(child.Data)
				}
			}
			body := text.String()
			if streamingSiteAttribute(node, "type") == "application/ld+json" {
				var schema fawesomeSchema
				if json.Unmarshal([]byte(body), &schema) == nil && (schema.Type == "Movie" || schema.Type == "VideoObject") {
					schemas = append(schemas, schema)
				}
			} else {
				nodes, medias, movies := fawesomeNodeAssignment.FindAllStringIndex(body, -1), fawesomeMediaAssignment.FindAllStringIndex(body, -1), fawesomeMovieAssignment.FindAllStringIndex(body, -1)
				assignments += len(nodes) + len(medias) + len(movies)
				if len(nodes) == 1 && len(medias) == 1 && len(movies) == 1 {
					var nodeID, value string
					var movie fawesomeMovieInfo
					if json.NewDecoder(strings.NewReader(body[nodes[0][1]:])).Decode(&nodeID) == nil && json.NewDecoder(strings.NewReader(body[medias[0][1]:])).Decode(&value) == nil && json.NewDecoder(strings.NewReader(body[movies[0][1]:])).Decode(&movie) == nil && nodeID == strconv.FormatInt(movie.NodeID, 10) {
						bindings++
						info, encoded = movie, value
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return info, encoded, schemas, bindings == 1 && assignments == 3
}

func fawesomeSchemasMatch(schemas []fawesomeSchema, info fawesomeMovieInfo, page *url.URL) bool {
	if len(schemas) != 2 {
		return false
	}
	catalogDate, err := time.Parse("02 January 2006 - 03:04 pm", info.ReleaseDate)
	if err != nil {
		return false
	}
	var schemaDate time.Time
	seen := map[string]bool{}
	for _, schema := range schemas {
		if seen[schema.Type] || !sameAdditionalFeatureTitle(info.Title, schema.Name) {
			return false
		}
		seen[schema.Type] = true
		parts := fawesomeISODuration.FindStringSubmatch(schema.Duration)
		if len(parts) != 4 {
			return false
		}
		seconds := 0.0
		for i, scale := range []float64{3600, 60, 1} {
			value, _ := strconv.ParseFloat(parts[i+1], 64)
			seconds += value * scale
		}
		if math.Abs(seconds-info.Runtime) > 1 {
			return false
		}
		date, raw := schema.DatePublished, schema.URL
		if schema.Type == "VideoObject" {
			date, raw = schema.UploadDate, schema.EmbedURL
		}
		released, err := time.Parse(time.RFC3339Nano, date)
		if err != nil || released.Year() != catalogDate.Year() || released.Month() != catalogDate.Month() || released.Day() != catalogDate.Day() || raw != page.String() {
			return false
		}
		if !schemaDate.IsZero() && !released.Equal(schemaDate) {
			return false
		}
		schemaDate = released
	}
	return seen["Movie"] && seen["VideoObject"]
}
