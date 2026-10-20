package scout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // the probe decodes GIF
	_ "image/jpeg" // the probe decodes JPEG
	_ "image/png"  // the probe decodes PNG
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
)

// The picture search is the second Phase 3 domain. A query of kind image
// fans out to the image drivers, keeps everything the hosts returned ranked
// by how well the file's name answers the query, and probes each file
// without a browser: the first megabyte of the original decodes the header
// for the dimensions and format, and the thumbnail (or a small original)
// decodes whole for the perceptual hash that the fingerprint library
// compares with everything it has seen. A decoded file is identified on the
// ladder; no sandbox render runs yet, so no picture is rendered.

const (
	// maxImageCandidates caps the listings one search probes.
	maxImageCandidates = 24
	// maxImagePageChecks bounds the page checks spent on listings without a
	// file.
	maxImagePageChecks = 6
	// imageHeaderLimit is how much of the original is fetched for the
	// header: a megabyte holds any JPEG header, EXIF and ICC profile included.
	imageHeaderLimit = 1 << 20
	// imageHashLimit bounds the file decoded whole for the hash.
	imageHashLimit = 4 << 20
	// maxImageProbes is how many files one search fetches: the best
	// listings by relevance, two requests each. Wikimedia's upload host
	// answers 429 to a burst of a couple of dozen requests from one client.
	maxImageProbes = 8
	// imageProbeSlots is how many files are probed at once.
	imageProbeSlots = 2
)

// imageRequestGap is the least time between two of the probe's requests,
// whatever the host, so a search never bursts; a variable so tests can
// shorten it.
var imageRequestGap = 250 * time.Millisecond

// imagePacer spaces the probe's requests across a process.
var imagePacer struct {
	mu   sync.Mutex
	last time.Time
}

// paceImageRequest waits until imageRequestGap has passed since the last
// request the probe sent, or the context ends.
func paceImageRequest(ctx context.Context) {
	imagePacer.mu.Lock()
	wait := time.Until(imagePacer.last.Add(imageRequestGap))
	if wait < 0 {
		wait = 0
	}
	imagePacer.last = time.Now().Add(wait)
	imagePacer.mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
}

var imageContentRange = regexp.MustCompile(`^bytes [0-9]+-[0-9]+/([0-9]+)$`)

// imageWording turns the movie page check's wording into the picture's.
var imageWording = strings.NewReplacer(
	"Movie-page access and playback", "Access",
	"public HTTP or HTTPS movie page", "public HTTP or HTTPS page",
	"Movie licensing, sign-in, payment, and playback remain unconfirmed", "The file itself was not fetched",
	"requested movie", "requested picture",
	"movie HTML page", "HTML page",
	"movie-page response", "page response",
	"movie access", "access",
	"movie link", "link",
	"movie page", "page",
	"Playback is unconfirmed", "The file was not fetched",
	"playback is unconfirmed", "the file was not fetched",
	"playback remain unconfirmed", "the file was not fetched",
	"The movie", "The picture",
	"movie", "picture",
)

// ImageCheck records what a bounded fetch and decode of a picture's file
// proved. Status is decoded (the header decoded, with dimensions and
// format), undecodable (the file answered but is not a picture Go
// decodes), unsupported (a picture in a format the probe cannot decode
// yet), unreachable or not_checked. FingerprintStatus says how the hash
// went: computed, too_large, undecodable or unavailable.
type ImageCheck struct {
	Status            string            `json:"status"`
	Method            string            `json:"method"`
	CheckedAt         string            `json:"checkedAt"`
	Detail            string            `json:"detail"`
	Width             int               `json:"width,omitempty"`
	Height            int               `json:"height,omitempty"`
	Format            string            `json:"format,omitempty"`
	Bytes             int64             `json:"bytes,omitempty"`
	HTTPStatus        int               `json:"httpStatus,omitempty"`
	EvidenceURL       string            `json:"evidenceUrl,omitempty"`
	Fingerprint       *lens.Fingerprint `json:"fingerprint,omitempty"`
	FingerprintStatus string            `json:"fingerprintStatus,omitempty"`
}

// searchImage answers a picture query. The region is kept for the report
// and the check location; image drivers have no storefront markets.
func (s *Scout) searchImage(ctx context.Context, query, region string, start time.Time) SearchReport {
	q := lens.ParseAs(query, lens.TypeImage)
	report := SearchReport{Query: query, Kind: lens.TypeImage, Region: region, Sources: []VerifiedSource{}, VerifiedSources: []VerifiedSource{}, PrunedSources: []VerifiedSource{}, Providers: []ProviderStatus{}}
	if q.TitleKey == "" {
		report.Providers = append(report.Providers, ProviderStatus{Name: "Picture search", Status: "empty", Detail: "Enter what the picture shows, with the artist if known; picture pages are not inspected by URL yet."})
		report.DurationMs = time.Since(start).Milliseconds()
		return report
	}

	type checkLocation struct{ country, detail string }
	locationResult := make(chan checkLocation, 1)
	go func() {
		country, detail := s.availabilityCheckCountry(ctx)
		locationResult <- checkLocation{country, detail}
	}()

	candidates, providers := s.runDriverSearches(ctx, lens.TypeImage, region, func(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, market string) ([]CandidateSource, ProviderStatus) {
		return s.queryImageDriver(ctx, fetcher, d, q, market)
	})
	if len(providers) == 0 {
		providers = []ProviderStatus{{Name: "Picture catalogs", Status: "not_configured", Detail: "No image driver is loaded."}}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	candidates = rankImageCandidates(q, candidates)

	location := checkLocation{}
	select {
	case location = <-locationResult:
	case <-ctx.Done():
		location.detail = "Check location could not be confirmed before the search deadline."
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	pageChecks, probes := map[int]bool{}, map[int]bool{}
	for i, candidate := range candidates {
		if candidate.DirectStreamURL == "" {
			if len(pageChecks) < maxImagePageChecks {
				pageChecks[i] = true
			}
		} else if len(probes) < maxImageProbes {
			probes[i] = true
		}
	}

	results := make([]VerifiedSource, len(candidates))
	var wg sync.WaitGroup
	slots := make(chan struct{}, imageProbeSlots)
	for i, candidate := range candidates {
		wg.Add(1)
		go func(idx int, c CandidateSource) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			checked := time.Now()
			result := VerifiedSource{CandidateSource: c, DetectedManifests: []string{}, VerificationStatus: "listing", VerificationMode: "Provider listing", CheckedAt: checked.UTC().Format(time.RFC3339)}
			if c.DirectStreamURL != "" && !probes[idx] {
				result.ImageCheck = &ImageCheck{Status: "not_checked", Method: "image_decode", CheckedAt: result.CheckedAt, EvidenceURL: c.DirectStreamURL, Detail: fmt.Sprintf("Not probed within this search's %d-file budget. Open the page or the viewer to see it.", maxImageProbes)}
				result.AvailabilityCheck = uncheckedMovieAvailability(region, location.country, result.ImageCheck.Detail)
			} else if c.DirectStreamURL != "" {
				check := probeImage(ctx, client, c)
				result.ImageCheck = &check
				result.VerificationMode = "Image decode"
				result.VerifyDurationMs = time.Since(checked).Milliseconds()
				if check.Status == "decoded" {
					result.Verified = true
					result.VerificationStatus = "media_reachable"
					result.DetectedManifests = []string{c.DirectStreamURL}
					result.StreamFormat = strings.ToUpper(check.Format) + " image"
					result.CleanlinessScore = 100
				}
				result.AvailabilityCheck = imageAvailability(check, region, location.country)
			} else if pageChecks[idx] && ctx.Err() == nil {
				check := checkMoviePageAvailability(ctx, client, c, region, location.country)
				check.Detail = imageWording.Replace(check.Detail)
				result.AvailabilityCheck = &check
				result.VerifyDurationMs = time.Since(checked).Milliseconds()
				if check.Status == "page_reachable" {
					result.VerificationStatus = "page_reachable"
				}
			} else {
				result.AvailabilityCheck = uncheckedMovieAvailability(region, location.country, "Not checked within this search's bounded page-check budget. Open the page to see the picture.")
			}
			result.RelevanceScore = int(imageRelevance(q, c)*100 + 0.5)
			results[idx] = result
		}(i, candidate)
	}
	wg.Wait()
	matchImagesBySight(results)

	sources := make([]VerifiedSource, 0, len(results))
	verified := []VerifiedSource{}
	fingerprints := 0
	for _, result := range results {
		if result.Verified {
			verified = append(verified, result)
		}
		if result.ImageCheck != nil && result.ImageCheck.Fingerprint.Valid() {
			fingerprints++
		}
		sources = append(sources, result)
	}
	// What a check proved, then relevance, then the hosts' own order.
	sort.SliceStable(sources, func(i, j int) bool {
		if a, b := imageStatusRank(sources[i]), imageStatusRank(sources[j]); a != b {
			return a > b
		}
		return sources[i].RelevanceScore > sources[j].RelevanceScore
	})
	report.Sources, report.VerifiedSources = sources, verified
	report.TotalCandidates, report.ListedCount, report.VerifiedCount = len(candidates), len(sources), len(verified)
	report.Providers = providers
	coverage := movieAvailabilityCoverage(region, location.country, location.detail, sources)
	coverage.FingerprintCount = fingerprints
	detail := "Each picture's file is fetched for its header, which gives the dimensions and format, and its thumbnail is decoded for a perceptual hash the index compares with everything it has seen. No sandbox render runs yet, so no picture is rendered on the ladder. "
	if location.country == "" {
		detail += "The check server's country could not be confirmed. "
		if location.detail != "" {
			detail += location.detail
		}
	}
	coverage.Detail = strings.TrimSpace(detail)
	report.AvailabilityChecks = coverage
	report.DurationMs = time.Since(start).Milliseconds()
	return report
}

// queryImageDriver runs one image driver's search for the picture's title
// and keeps everything it returned, ranked by relevance.
func (s *Scout) queryImageDriver(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, q lens.ObjectQuery, market string) ([]CandidateSource, ProviderStatus) {
	status := ProviderStatus{Name: d.StatusName(market), Status: "ok"}
	listings, err := d.Search(ctx, fetcher, q.Title, market)
	if err != nil {
		status.Status, status.Detail = "error", "Provider unavailable; results may be incomplete."
		return nil, status
	}
	candidates := make([]CandidateSource, 0, len(listings))
	for _, listing := range listings {
		candidate := candidateFromListing(d, listing)
		if candidate.Kind == "" {
			candidate.Kind = lens.TypeImage
		}
		candidates = append(candidates, candidate)
	}
	if d.Spec.Keep > 0 && len(candidates) > d.Spec.Keep {
		candidates = rankImageCandidates(q, candidates)[:d.Spec.Keep]
	}
	status.Detail = fmt.Sprintf("%d pictures returned; the file of each is probed", len(candidates))
	if len(candidates) == 0 {
		status.Status = "empty"
	}
	return candidates, status
}

// imageIdentityOf reads a candidate's tags into a picture identity, with
// the dimensions the host stated.
func imageIdentityOf(c CandidateSource) lens.ImageIdentity {
	width, height := parseResolution(c.Resolution)
	return lens.ImageListing(c.Title, c.Creator, c.ReleaseYear, width, height)
}

// parseResolution reads "WxH" as the host lists it; anything else is zero.
func parseResolution(value string) (int, int) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0
	}
	width, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0, 0
	}
	return width, height
}

// imageRelevance scores a candidate against the query by its tags.
func imageRelevance(q lens.ObjectQuery, c CandidateSource) float64 {
	return lens.ImageRelevance(q, imageIdentityOf(c))
}

// rankImageCandidates orders candidates by relevance, keeping the hosts'
// own order among equals, drops repeated URLs and keeps the cap.
func rankImageCandidates(q lens.ObjectQuery, candidates []CandidateSource) []CandidateSource {
	sort.SliceStable(candidates, func(i, j int) bool {
		return imageRelevance(q, candidates[i]) > imageRelevance(q, candidates[j])
	})
	seen := map[string]bool{}
	out := make([]CandidateSource, 0, len(candidates))
	for _, c := range candidates {
		key := strings.TrimRight(c.URL, "/")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) == maxImageCandidates {
			break
		}
	}
	return out
}

// imageStatusRank orders sources by what their checks proved: a decoded
// file, then a reachable page.
func imageStatusRank(source VerifiedSource) int {
	switch source.VerificationStatus {
	case "media_reachable":
		return 2
	case "page_reachable":
		return 1
	}
	return 0
}

// errImageTooLarge says a file exceeded the bytes the probe would decode.
var errImageTooLarge = errors.New("the picture exceeds the probe's byte budget")

// fetchImageBytes fetches a file through the guarded client: the first
// limit bytes by a range request, or, when whole is asked, the whole file
// up to limit bytes and an error beyond it. It reports the HTTP status and
// the file's full length when the server states it.
func fetchImageBytes(ctx context.Context, client *http.Client, raw string, limit int64, whole bool) ([]byte, int, int64, error) {
	target, ok := safeAvailabilityURL(raw)
	if !ok {
		return nil, 0, 0, errors.New("the file is not on a public web host")
	}
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guarded := *client
	if guarded.Transport == nil {
		guarded.Transport = newMovieAvailabilityClient().Transport
	}
	guarded.Jar = nil
	guarded.Timeout = 8 * time.Second
	priorRedirect := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("picture redirect limit reached")
		}
		if _, safe := safeAvailabilityURL(req.URL.String()); !safe {
			return errors.New("picture redirect requires a public destination")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	paceImageRequest(ctx)
	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, 0, 0, err
	}
	request.Header.Set("User-Agent", "ITEMBA-NET/2 (+https://github.com/JAPHARYROMAN/itemba-net; personal research crawler; picture probe)")
	request.Header.Set("Accept", "image/*")
	if !whole {
		request.Header.Set("Range", "bytes=0-"+strconv.FormatInt(limit-1, 10))
	}
	response, err := guarded.Do(request)
	if err != nil {
		return nil, 0, 0, errors.New("the file could not be fetched")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return nil, response.StatusCode, 0, fmt.Errorf("the file answered HTTP %d", response.StatusCode)
	}
	total := int64(0)
	if match := imageContentRange.FindStringSubmatch(response.Header.Get("Content-Range")); match != nil {
		total, _ = strconv.ParseInt(match[1], 10, 64)
	} else if response.ContentLength > 0 {
		total = response.ContentLength
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, response.StatusCode, total, errors.New("the file could not be read")
	}
	if int64(len(data)) > limit {
		if whole {
			return nil, response.StatusCode, total, errImageTooLarge
		}
		data = data[:limit]
	}
	return data, response.StatusCode, total, nil
}

// probeImage fetches a picture's file for its header and decodes the
// thumbnail, or a small original, for its hash.
func probeImage(ctx context.Context, client *http.Client, source CandidateSource) ImageCheck {
	check := ImageCheck{Status: "unreachable", Method: "image_decode", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.DirectStreamURL, Detail: "The picture's file could not be fetched from the check location."}
	head, status, total, err := fetchImageBytes(ctx, client, source.DirectStreamURL, imageHeaderLimit, false)
	check.HTTPStatus, check.Bytes = status, total
	if err != nil {
		check.Detail = "The picture's file could not be fetched from the check location: " + err.Error() + "."
		if status == http.StatusTooManyRequests {
			check.Detail = "The host rate-limited the probe (HTTP 429), so the file was not fetched this time; it can still be opened in the viewer."
		}
		return check
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(head))
	if err != nil {
		sniffed := strings.ToLower(http.DetectContentType(head))
		if strings.HasPrefix(sniffed, "image/") {
			check.Status = "unsupported"
			check.Format = strings.TrimPrefix(sniffed, "image/")
			check.Detail = fmt.Sprintf("The file answered as a %s picture, which the probe cannot decode yet; it can still be opened in the viewer.", check.Format)
			return check
		}
		check.Status = "undecodable"
		check.Detail = "The file answered but is not a picture the probe decodes (JPEG, PNG or GIF)."
		return check
	}
	check.Status, check.Width, check.Height, check.Format = "decoded", config.Width, config.Height, format
	check.Detail = fmt.Sprintf("The file's header decoded as a %d×%d %s picture from the check location.", config.Width, config.Height, strings.ToUpper(format))
	check.Fingerprint, check.FingerprintStatus = hashImage(ctx, client, source, head, total)
	return check
}

// hashImage decodes the thumbnail, or the original when it is small enough
// and there is no thumbnail, and hashes it. The first megabyte already
// fetched serves when it is the whole original.
func hashImage(ctx context.Context, client *http.Client, source CandidateSource, head []byte, total int64) (*lens.Fingerprint, string) {
	var data []byte
	switch {
	case source.Thumbnail != "" && source.Thumbnail != source.DirectStreamURL:
		fetched, _, _, err := fetchImageBytes(ctx, client, source.Thumbnail, imageHashLimit, true)
		if err != nil {
			if errors.Is(err, errImageTooLarge) {
				return nil, "too_large"
			}
			return nil, "unavailable"
		}
		data = fetched
	case total > 0 && total <= int64(len(head)):
		data = head
	case total > imageHashLimit:
		return nil, "too_large"
	default:
		fetched, _, _, err := fetchImageBytes(ctx, client, source.DirectStreamURL, imageHashLimit, true)
		if err != nil {
			if errors.Is(err, errImageTooLarge) {
				return nil, "too_large"
			}
			return nil, "unavailable"
		}
		data = fetched
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "undecodable"
	}
	fp := lens.ImageFingerprint(img)
	if !fp.Valid() {
		return nil, "undecodable"
	}
	return &fp, "computed"
}

// imageAvailability reports a file probe in the picture's words.
func imageAvailability(check ImageCheck, region, country string) *AvailabilityCheck {
	out := uncheckedMovieAvailability(region, country, check.Detail)
	out.Method, out.CheckedAt, out.EvidenceURL, out.HTTPStatus = "http_media_probe", check.CheckedAt, check.EvidenceURL, check.HTTPStatus
	switch check.Status {
	case "decoded", "unsupported":
		out.Status = "media_reachable"
	case "undecodable":
		out.Status = "unconfirmed"
	default:
		switch check.HTTPStatus {
		case http.StatusNotFound, http.StatusGone:
			out.Status = "not_found"
		case http.StatusUnauthorized:
			out.Status = "sign_in_required"
		case http.StatusForbidden, http.StatusTooManyRequests:
			out.Status = "access_restricted"
		case 0:
			out.Status = "error"
		}
	}
	return out
}

// matchImagesBySight compares the hashes this search computed and says on
// each what the nearest other looked like: the same picture under different
// tags joins it, and the source takes the better relevance of the two; a
// different picture under the same tags is kept apart; the index makes the
// same comparison against everything it has seen before.
func matchImagesBySight(results []VerifiedSource) {
	type seen struct {
		index    int
		identity lens.ImageIdentity
	}
	var hashed []seen
	for i := range results {
		check := results[i].ImageCheck
		if check == nil || !check.Fingerprint.Valid() {
			continue
		}
		identity := imageIdentityOf(results[i].CandidateSource)
		identity.Width, identity.Height, identity.Fingerprint = check.Width, check.Height, check.Fingerprint
		hashed = append(hashed, seen{i, identity})
	}
	for _, a := range hashed {
		var best *lens.IdentityMatch
		relevance := results[a.index].RelevanceScore
		for _, b := range hashed {
			if a.index == b.index {
				continue
			}
			c := lens.CompareFingerprints(*a.identity.Fingerprint, *b.identity.Fingerprint)
			if !c.Known || best != nil && c.Distance >= best.Distance {
				continue
			}
			byTags, otherByTags := a.identity, b.identity
			byTags.Fingerprint, otherByTags.Fingerprint = nil, nil
			tags := lens.MatchImages(byTags, otherByTags)
			match := &lens.IdentityMatch{Method: lens.MethodPerceptualHash, PeerURL: results[b.index].URL, PeerTitle: results[b.index].Title, Distance: math.Round(c.Distance*1000) / 1000}
			switch {
			case c.Same() && tags.Same:
				match.Verdict, match.Effect = lens.VerdictSame, lens.EffectConfirmed
			case c.Same():
				match.Verdict, match.Effect = lens.VerdictSame, lens.EffectJoined
				relevance = max(relevance, results[b.index].RelevanceScore)
			case tags.Same:
				match.Verdict, match.Effect = lens.VerdictDifferent, lens.EffectKeptApart
			default:
				match.Verdict = lens.VerdictDifferent
			}
			match.Detail = lens.IdentityDetail(*match)
			best = match
		}
		if best != nil {
			results[a.index].Identity = best
			results[a.index].RelevanceScore = relevance
		}
	}
}
