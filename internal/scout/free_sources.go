package scout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const maxFreeSourceResolutions = 6

var (
	freeArchiveIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
	freePeerTubeID        = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[A-Za-z0-9]{20,24})$`)
)

// resolveFreeMovieSources enriches individual public catalog pages, never an
// arbitrary paid player's scripts. Metadata is fetched through the same guarded,
// direct connection used for availability and playback. Failures leave listings
// untouched, and metadata access is not itself playback confirmation.
//
// Provider contracts: https://archive.org/developers/md-read.html and
// https://docs.joinpeertube.org/api-rest-reference.html (checked 2026-10-18).
func (s *Scout) resolveFreeMovieSources(ctx context.Context, candidates []CandidateSource) ([]CandidateSource, ProviderStatus) {
	out := append([]CandidateSource(nil), candidates...)
	status := ProviderStatus{Name: "Free movie source resolution", Status: "empty"}
	resolveCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	targets := freeSourceResolutionTargets(out)
	var wg sync.WaitGroup
	var mu sync.Mutex
	resolved := 0
	slots := make(chan struct{}, 3)
	for _, index := range targets {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-resolveCtx.Done():
				return
			}
			itemCtx, itemCancel := context.WithTimeout(resolveCtx, 4*time.Second)
			defer itemCancel()
			if enriched, ok := resolveFreeMovieSource(itemCtx, client, out[index]); ok {
				out[index] = enriched
				mu.Lock()
				resolved++
				mu.Unlock()
			}
		}(index)
	}
	wg.Wait()
	status.Detail = fmt.Sprintf("Resolved %d of %d supported free movie pages to public catalog media; playback requires a separate sample. At most %d pages are resolved per search.", resolved, len(targets), maxFreeSourceResolutions)
	if resolved > 0 {
		status.Status = "ok"
	} else if len(targets) > 0 {
		status.Status = "unconfirmed"
	}
	return out, status
}

func freeSourceResolutionTargets(candidates []CandidateSource) []int {
	type target struct {
		index      int
		kind, site string
	}
	var eligible []target
	seen := map[string]bool{}
	for i, source := range candidates {
		kind, _, ok := freeResolutionTarget(source)
		if !ok || seen[source.URL] {
			continue
		}
		u, _ := url.Parse(source.URL)
		seen[source.URL] = true
		eligible = append(eligible, target{i, kind, kind + "|" + strings.ToLower(u.Hostname())})
	}
	var targets []int
	selected := map[int]bool{}
	kinds, sites := map[string]bool{}, map[string]bool{}
	// Reserve coverage for each supported catalog family, then distinct sites
	// (including federated instances), before filling the remaining slots.
	for pass := 0; pass < 3 && len(targets) < maxFreeSourceResolutions; pass++ {
		for _, item := range eligible {
			if selected[item.index] || pass == 0 && kinds[item.kind] || pass == 1 && sites[item.site] {
				continue
			}
			targets = append(targets, item.index)
			selected[item.index], kinds[item.kind], sites[item.site] = true, true, true
			if len(targets) == maxFreeSourceResolutions {
				break
			}
		}
	}
	return targets
}

func freeResolutionTarget(source CandidateSource) (kind, id string, ok bool) {
	if source.DirectStreamURL != "" || source.AccessType != "" && source.AccessType != "unknown" && source.AccessType != "free" || source.Price != "" {
		return "", "", false
	}
	u, safe := safeAvailabilityURL(source.URL)
	if !safe {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if host == "archive.org" && len(parts) == 2 && (parts[0] == "details" || parts[0] == "embed") && freeArchiveIdentifier.MatchString(parts[1]) {
		return "archive", parts[1], true
	}
	if (host == "publicdomainmovie.net" || host == "www.publicdomainmovie.net") && len(parts) == 2 && parts[0] == "movie" && freeArchiveIdentifier.MatchString(parts[1]) {
		return "public_domain_movies", "", true
	}
	if len(parts) == 2 && parts[0] == "w" && freePeerTubeID.MatchString(parts[1]) {
		return "peertube", parts[1], true
	}
	if len(parts) == 3 && parts[0] == "videos" && (parts[1] == "watch" || parts[1] == "embed") && freePeerTubeID.MatchString(parts[2]) {
		return "peertube", parts[2], true
	}
	if kind, supported := additionalFreeResolutionTarget(u); supported {
		return kind, "", true
	}
	return "", "", false
}

func resolveFreeMovieSource(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, bool) {
	kind, id, ok := freeResolutionTarget(source)
	if !ok {
		return source, false
	}
	switch kind {
	case "archive":
		return resolveFreeArchiveSource(ctx, client, source, id, false)
	case "peertube":
		return resolveFreePeerTubeSource(ctx, client, source, id)
	case "public_domain_movies":
		data, final, err := fetchFreeSourceDocument(ctx, client, source.URL)
		page, _ := url.Parse(source.URL)
		if err != nil || final.Hostname() != page.Hostname() || final.Path != page.Path {
			return source, false
		}
		id = publicDomainMovieArchiveItem(data, final)
		if id != "" {
			return resolveFreeArchiveSource(ctx, client, source, id, true)
		}
	case "arcoiris", "public_domain_movie_org":
		return resolveAdditionalFreeMovie(ctx, client, source)
	}
	return source, false
}

func fetchFreeSourceDocument(ctx context.Context, client *http.Client, raw string) ([]byte, *url.URL, error) {
	return fetchMovieDocumentWithinLimit(ctx, client, raw, availabilityPageLimit)
}

func fetchMovieDocumentWithinLimit(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, *url.URL, error) {
	u, ok := safeAvailabilityURL(raw)
	if !ok {
		return nil, nil, errors.New("unsupported public catalog URL")
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
		if len(via) >= 5 {
			return errors.New("catalog redirect limit reached")
		}
		if _, safe := safeAvailabilityURL(req.URL.String()); !safe {
			return errors.New("catalog redirect requires public HTTP destination")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json,text/html;q=0.9")
	request.Header.Set("User-Agent", "ITEMBA-NET/0.1 (public movie metadata)")
	response, err := guarded.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, errors.New("public catalog metadata unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit || response.Request == nil || response.Request.URL == nil {
		return nil, nil, errors.New("public catalog metadata exceeded bounds")
	}
	final, safe := safeAvailabilityURL(response.Request.URL.String())
	if !safe {
		return nil, nil, errors.New("public catalog metadata destination invalid")
	}
	return data, final, nil
}

func matchingFreeMovieMetadata(source CandidateSource, title string, year int, duration float64) bool {
	canonical, sourceYear := movieTitleDetails(source)
	if canonical == "" || title == "" || duration < 180 || hasUnrequestedDerivative(canonical, title) || sourceYear != 0 && year != 0 && sourceYear != year {
		return false
	}
	query := canonical
	if sourceYear != 0 {
		query += " " + strconv.Itoa(sourceYear)
	}
	if relevanceScore(query, CandidateSource{Title: title, ReleaseYear: year}) == 0 {
		return false
	}
	if expected := mediaDurationSeconds(source.Duration); expected > 0 && duration < expected*.8 {
		return false
	}
	return true
}

func scalarMetadataString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		if len(v) == 1 {
			return scalarMetadataString(v[0])
		}
	}
	return ""
}

func resolvedFreeMovieID(prefix, raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return prefix + "-resolved-" + hex.EncodeToString(sum[:8])
}

func resolveFreeArchiveSource(ctx context.Context, client *http.Client, source CandidateSource, identifier string, publicDomainSite bool) (CandidateSource, bool) {
	if !freeArchiveIdentifier.MatchString(identifier) {
		return source, false
	}
	metadataURL := "https://archive.org/metadata/" + url.PathEscape(identifier)
	data, final, err := fetchFreeSourceDocument(ctx, client, metadataURL)
	if err != nil || final.Hostname() != "archive.org" || final.Path != "/metadata/"+identifier {
		return source, false
	}
	var payload struct {
		IsDark   bool `json:"is_dark"`
		Metadata struct {
			Identifier string `json:"identifier"`
			Title      any    `json:"title"`
			MediaType  string `json:"mediatype"`
			Year       any    `json:"year"`
			Date       string `json:"date"`
			Runtime    any    `json:"runtime"`
			Restricted any    `json:"access-restricted-item"`
		} `json:"metadata"`
		Files []struct {
			Name    string `json:"name"`
			Format  string `json:"format"`
			Length  any    `json:"length"`
			Size    any    `json:"size"`
			Private any    `json:"private"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.IsDark || payload.Metadata.Identifier != identifier || payload.Metadata.MediaType != "movies" {
		return source, false
	}
	if restricted := scalarMetadataString(payload.Metadata.Restricted); strings.EqualFold(restricted, "true") || restricted == "1" || payload.Metadata.Restricted == true {
		return source, false
	}
	year := 0
	if match := movieYearPattern.FindString(scalarMetadataString(payload.Metadata.Year)); match != "" {
		year, _ = strconv.Atoi(match)
	} else if match := movieYearPattern.FindString(payload.Metadata.Date); match != "" {
		year, _ = strconv.Atoi(match)
	}
	title := scalarMetadataString(payload.Metadata.Title)
	if page, safe := safeAvailabilityURL(source.URL); safe {
		if kind, supported := additionalFreeResolutionTarget(page); supported && kind == "public_domain_movie_org" && !sameAdditionalFeatureTitle(source.Title, title) {
			return source, false
		}
	}
	type mediaFile struct {
		name           string
		duration, size float64
		mp4            bool
		browserFormat  bool
		browserHint    bool
	}
	var files []mediaFile
	for _, file := range payload.Files {
		lower := strings.ToLower(file.Name)
		ext := path.Ext(lower)
		if ext != ".mp4" && ext != ".webm" && ext != ".ogv" || file.Name == "" || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "\\") || path.Clean(file.Name) != file.Name || strings.HasPrefix(file.Name, "../") || file.Private == true || strings.EqualFold(scalarMetadataString(file.Private), "true") || scalarMetadataString(file.Private) == "1" {
			continue
		}
		if hasUnrequestedDerivative("movie", strings.ReplaceAll(strings.ReplaceAll(lower, "_", " "), "-", " ")) || strings.Contains(lower, "preview") || strings.Contains(lower, "sample") {
			continue
		}
		if archiveNonMovieFile(file.Name) || movieFileYearConflicts(file.Name, title, year) {
			continue
		}
		duration := mediaDurationSeconds(scalarMetadataString(file.Length))
		if duration == 0 {
			duration = mediaDurationSeconds(scalarMetadataString(payload.Metadata.Runtime))
		}
		if !matchingFreeMovieMetadata(source, title, year, duration) {
			continue
		}
		size, _ := strconv.ParseFloat(scalarMetadataString(file.Size), 64)
		browserHint := archiveStreamingFormatHint(file.Format)
		browserFormat := ext == ".webm" || browserHint
		files = append(files, mediaFile{file.Name, duration, size, ext == ".mp4", browserFormat, browserHint})
	}
	if len(files) == 0 {
		return source, false
	}
	// A page without a known runtime may contain both the feature and unlabeled
	// excerpts. Prefer complete editions before choosing a smaller encoding;
	// otherwise a low-bitrate clip could displace a full movie.
	longest := mediaDurationSeconds(scalarMetadataString(payload.Metadata.Runtime))
	for _, file := range files {
		if file.duration > longest {
			longest = file.duration
		}
	}
	fullFiles := files[:0]
	for _, file := range files {
		if file.duration >= longest*.8 {
			fullFiles = append(fullFiles, file)
		}
	}
	files = fullFiles
	if len(files) == 0 {
		return source, false
	}
	sort.SliceStable(files, func(i, j int) bool {
		// MP4 is a container: prefer declared streaming derivatives over an
		// unspecified MPEG4 encoding, then choose a lower bitrate. A native
		// sample still has to prove playback for the selected file.
		if files[i].browserFormat != files[j].browserFormat {
			return files[i].browserFormat
		}
		if files[i].mp4 != files[j].mp4 {
			return files[i].mp4
		}
		if files[i].size > 0 && files[j].size > 0 {
			return files[i].size/files[i].duration < files[j].size/files[j].duration
		}
		return files[i].duration > files[j].duration
	})
	best := files[0]
	// These hints describe declared encoding and average bitrate, not a
	// successful decoder result. Ignore absent, invalid or overflowing values.
	source.mediaBrowserFormat, source.mediaBitrate = best.browserHint, 0
	if best.size > 0 && best.duration > 0 && !math.IsNaN(best.size) && !math.IsInf(best.size, 0) && !math.IsNaN(best.duration) && !math.IsInf(best.duration, 0) {
		bitrate := best.size / best.duration * 8
		if bitrate > 0 && !math.IsNaN(bitrate) && !math.IsInf(bitrate, 0) {
			source.mediaBitrate = bitrate
		}
	}
	stream := &url.URL{Scheme: "https", Host: "archive.org", Path: "/download/" + identifier + "/" + best.name}
	source.DirectStreamURL, source.KnownManifests = stream.String(), []string{stream.String()}
	source.ID, source.Provider, source.SourceType = resolvedFreeMovieID("ia", source.URL), "Internet Archive", "Archive video"
	if publicDomainSite {
		source.ID, source.Provider, source.SourceType = resolvedFreeMovieID("pdm", source.URL), "Public Domain Movies", "Public Domain"
	}
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "archive_metadata", metadataURL
	source.AccessNote = "A public movie file was listed in this item's Archive metadata. Access and regional playback are checked separately; rights vary by item and country."
	source.Duration, source.ListingOnly, source.DestinationType = strconv.FormatFloat(best.duration, 'f', -1, 64), false, "watch"
	if source.ReleaseYear == 0 {
		source.ReleaseYear = year
	}
	return source, true
}

func archiveStreamingFormatHint(value string) bool {
	format := strings.ToLower(strings.TrimSpace(value))
	// Archive documents its 512kb MP4 derivative as baseline H.264 optimized
	// for streaming. This exact metadata label is a selection hint, not proof
	// that every historical file actually decodes in the current browser.
	// https://blog.archive.org/2010/04/26/better-mp4-h-264-derivatives-at-archive-org/
	return format == "512kb mpeg4" || strings.Contains(format, "webm") || strings.Contains(format, "h.264") || strings.Contains(format, "h264") || strings.Contains(format, "avc")
}

func publicDomainMovieArchiveItem(data []byte, page *url.URL) string {
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return ""
	}
	items := map[string]bool{}
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, download bool) {
		if node.Type == html.ElementNode {
			for _, attr := range node.Attr {
				if attr.Key == "class" && strings.Contains(" "+attr.Val+" ", " field-name-download ") {
					download = true
				}
			}
			if node.Data == "a" && download {
				for _, attr := range node.Attr {
					if attr.Key != "href" {
						continue
					}
					ref, err := url.Parse(attr.Val)
					if err != nil {
						continue
					}
					u := page.ResolveReference(ref)
					id := u.Query().Get("id")
					if u.Scheme == page.Scheme && u.Host == page.Host && u.Path == "/movie.php" && strings.EqualFold(u.Query().Get("type"), ".mp4") && freeArchiveIdentifier.MatchString(id) {
						items[id] = true
					}
				}
			}
		}
		if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style" || node.Data == "nav" || node.Data == "footer") {
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, download)
		}
	}
	walk(doc, false)
	if len(items) == 1 {
		for id := range items {
			return id
		}
	}
	return ""
}

func resolveFreePeerTubeSource(ctx context.Context, client *http.Client, source CandidateSource, id string) (CandidateSource, bool) {
	page, _ := safeAvailabilityURL(source.URL)
	metadataURL := page.Scheme + "://" + page.Host + "/api/v1/videos/" + url.PathEscape(id)
	data, final, err := fetchFreeSourceDocument(ctx, client, metadataURL)
	if err != nil || final.Host != page.Host || final.Path != "/api/v1/videos/"+id {
		return source, false
	}
	type mediaFile struct {
		FileURL         string `json:"fileUrl"`
		FileDownloadURL string `json:"fileDownloadUrl"`
		Size            int64  `json:"size"`
		Resolution      struct {
			ID int `json:"id"`
		} `json:"resolution"`
	}
	var payload struct {
		UUID              string `json:"uuid"`
		ShortUUID         string `json:"shortUUID"`
		Name              string `json:"name"`
		Duration          int    `json:"duration"`
		IsLive            bool   `json:"isLive"`
		PasswordProtected bool   `json:"passwordProtected"`
		Privacy           struct {
			ID int `json:"id"`
		} `json:"privacy"`
		Files              []mediaFile `json:"files"`
		StreamingPlaylists []struct {
			PlaylistURL string      `json:"playlistUrl"`
			Files       []mediaFile `json:"files"`
		} `json:"streamingPlaylists"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return source, false
	}
	matchedSource, metadataTitle := source, payload.Name
	if strings.EqualFold(page.Hostname(), "video.blender.org") {
		matchedSource.Title = normalizeBlenderMovieTitle(source.Title)
		metadataTitle = normalizeBlenderMovieTitle(metadataTitle)
	}
	if payload.UUID != id && payload.ShortUUID != id || payload.IsLive || payload.PasswordProtected || payload.Privacy.ID != 1 || !freePeerTubeID.MatchString(payload.UUID) || !matchingFreeMovieMetadata(matchedSource, metadataTitle, 0, float64(payload.Duration)) {
		return source, false
	}
	var files []mediaFile
	for _, file := range payload.Files {
		if u, safe := safeAvailabilityURL(file.FileURL); safe && freePublicMediaExtension(u.Path, false) && file.Resolution.ID > 0 {
			files = append(files, file)
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Resolution.ID < files[j].Resolution.ID
	})
	stream := ""
	if len(files) > 0 {
		stream = files[0].FileURL
	}
	if stream == "" {
		for _, playlist := range payload.StreamingPlaylists {
			if u, safe := safeAvailabilityURL(playlist.PlaylistURL); safe && strings.EqualFold(path.Ext(u.Path), ".m3u8") {
				stream = u.String()
				break
			}
		}
	}
	if stream == "" {
		return source, false
	}
	source.Title = matchedSource.Title
	source.ID, source.Provider, source.SourceType = resolvedFreeMovieID("pt", source.URL), "PeerTube ("+strings.ToLower(page.Hostname())+")", "Federated PeerTube"
	source.DirectStreamURL, source.KnownManifests = stream, []string{stream}
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "peertube_api", metadataURL
	source.AccessNote = "The hosting instance's public video API lists this movie and its media. A separate playback sample checks whether the media actually starts."
	source.Duration, source.ListingOnly, source.DestinationType = strconv.Itoa(payload.Duration), false, "watch"
	return source, true
}

func freePublicMediaExtension(rawPath string, hls bool) bool {
	switch strings.ToLower(path.Ext(rawPath)) {
	case ".mp4", ".webm", ".ogv":
		return true
	case ".m3u8":
		return hls
	}
	return false
}

// Metadata-backed cross-host PeerTube/CDN files are permitted only when the
// evidence is the video API on the exact page host and the source is resolved
// by the bounded catalog resolver. Public DNS is still checked on every relay.
func resolvedFreePlaybackPair(source CandidateSource, page, media *url.URL) bool {
	evidence, safe := safeAvailabilityURL(source.AccessEvidenceURL)
	if !safe {
		return false
	}
	if additionalResolvedPlaybackPair(source, page, media, evidence) {
		return true
	}
	if source.AccessBasis == "peertube_api" && source.SourceType == "Federated PeerTube" && strings.HasPrefix(source.ID, "pt-resolved-") && source.Provider == "PeerTube ("+strings.ToLower(page.Hostname())+")" {
		_, id, ok := freeResolutionTarget(CandidateSource{URL: source.URL, AccessType: "free"})
		return ok && evidence.Scheme == page.Scheme && evidence.Host == page.Host && evidence.Path == "/api/v1/videos/"+id && evidence.RawQuery == "" && freePublicMediaExtension(media.Path, true)
	}
	if source.AccessBasis != "archive_metadata" || evidence.Hostname() != "archive.org" || evidence.RawQuery != "" || media.Hostname() != "archive.org" || !freePublicMediaExtension(media.Path, false) {
		return false
	}
	id := strings.TrimPrefix(evidence.Path, "/metadata/")
	if !freeArchiveIdentifier.MatchString(id) || evidence.Path != "/metadata/"+id || !strings.HasPrefix(media.Path, "/download/"+id+"/") {
		return false
	}
	if source.Provider == "Internet Archive" && source.SourceType == "Archive video" && strings.HasPrefix(source.ID, "ia-resolved-") {
		return page.Hostname() == "archive.org" && (page.Path == "/details/"+id || page.Path == "/embed/"+id)
	}
	if source.Provider == "Public Domain Movies" && source.SourceType == "Public Domain" && strings.HasPrefix(source.ID, "pdm-resolved-") {
		kind, _, ok := freeResolutionTarget(CandidateSource{URL: source.URL, AccessType: "free"})
		return ok && kind == "public_domain_movies"
	}
	return false
}
