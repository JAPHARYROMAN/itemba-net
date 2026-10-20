package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
)

// The document search is the third Phase 3 domain. A query of kind document
// fans out to the document drivers, keeps everything the hosts returned
// ranked by how well the title answers the query, reads the Archive items'
// metadata for their files, and probes each document without a browser:
// the PDF's head and tail by range requests for the format and a confirmed
// page count, and its text, from the OCR text beside it when the item has
// one and from the PDF's own content streams otherwise, for the text
// fingerprint that the fingerprint library compares with everything it has
// read. A parsed file is identified on the ladder; no sandbox render runs
// yet, so no document is rendered.

const (
	// maxDocumentCandidates caps the listings one search considers.
	maxDocumentCandidates = 24
	// maxDocumentProbes is how many files one search fetches: the best
	// listings by relevance, two or three requests each.
	maxDocumentProbes = 6
	// maxArchiveTextMetadata bounds the metadata requests one search spends.
	maxArchiveTextMetadata = 8
	// maxDocumentPageChecks bounds the page checks spent on listings
	// without a file.
	maxDocumentPageChecks = 6
	// documentHeadLimit and documentTailLimit are how much of a PDF is
	// fetched from each end: the head holds the header, a linearisation
	// dictionary and the first pages; the tail holds the page tree's root.
	documentHeadLimit = 512 << 10
	documentTailLimit = 256 << 10
	// documentTextLimit bounds the text file read for the fingerprint: room
	// for a long front matter before the body the fingerprint covers.
	documentTextLimit = 256 << 10
	// documentProbeSlots is how many files are probed at once.
	documentProbeSlots = 2
)

// documentWording turns the movie page check's wording into the document's.
var documentWording = strings.NewReplacer(
	"Movie-page access and playback", "Access",
	"public HTTP or HTTPS movie page", "public HTTP or HTTPS page",
	"Movie licensing, sign-in, payment, and playback remain unconfirmed", "The file itself was not fetched",
	"requested movie", "requested document",
	"movie HTML page", "HTML page",
	"movie-page response", "page response",
	"movie access", "access",
	"movie link", "link",
	"movie page", "page",
	"Playback is unconfirmed", "The file was not fetched",
	"playback is unconfirmed", "the file was not fetched",
	"playback remain unconfirmed", "the file was not fetched",
	"The movie", "The document",
	"movie", "document",
)

// DocumentCheck records what a bounded fetch and parse of a document's file
// proved. Status is parsed (a PDF whose structure was read, or a text file
// that was read), undecodable (the file answered but is neither),
// unsupported (a document format the probe cannot read yet, such as EPUB
// or DjVu), unreachable or not_checked. Pages is the page count read from
// the file when PagesConfirmed, else the count the host stated.
// FingerprintStatus says how the text fingerprint went: computed,
// too_short (too few words), unreadable (words enough, but no run of them
// reads as prose, as with a scan whose OCR is noise throughout), partial
// (the text read is the beginning of a longer one and its body is shorter
// than a fingerprint covers, so a fingerprint of it would be compared
// unfairly with whole texts) or none. BodySkipped is how many words of
// front matter the fingerprint skipped and BodyWords how many it covers.
type DocumentCheck struct {
	Status            string            `json:"status"`
	Method            string            `json:"method"`
	CheckedAt         string            `json:"checkedAt"`
	Detail            string            `json:"detail"`
	Format            string            `json:"format,omitempty"`
	Pages             int               `json:"pages,omitempty"`
	PagesConfirmed    bool              `json:"pagesConfirmed,omitempty"`
	Linearized        bool              `json:"linearized,omitempty"`
	Bytes             int64             `json:"bytes,omitempty"`
	HTTPStatus        int               `json:"httpStatus,omitempty"`
	EvidenceURL       string            `json:"evidenceUrl,omitempty"`
	TextChars         int               `json:"textChars,omitempty"`
	TextSource        string            `json:"textSource,omitempty"`
	BodySkipped       int               `json:"bodySkipped,omitempty"`
	BodyWords         int               `json:"bodyWords,omitempty"`
	Fingerprint       *lens.Fingerprint `json:"fingerprint,omitempty"`
	FingerprintStatus string            `json:"fingerprintStatus,omitempty"`
}

// searchDocument answers a document query.
func (s *Scout) searchDocument(ctx context.Context, query, region string, start time.Time) SearchReport {
	q := lens.ParseAs(query, lens.TypeDocument)
	report := SearchReport{Query: query, Kind: lens.TypeDocument, Region: region, Sources: []VerifiedSource{}, VerifiedSources: []VerifiedSource{}, PrunedSources: []VerifiedSource{}, Providers: []ProviderStatus{}}
	if q.TitleKey == "" {
		report.Providers = append(report.Providers, ProviderStatus{Name: "Document search", Status: "empty", Detail: "Enter a document's title, with its author if known; document pages are not inspected by URL yet."})
		report.DurationMs = time.Since(start).Milliseconds()
		return report
	}

	type checkLocation struct{ country, detail string }
	locationResult := make(chan checkLocation, 1)
	go func() {
		country, detail := s.availabilityCheckCountry(ctx)
		locationResult <- checkLocation{country, detail}
	}()

	candidates, providers := s.runDriverSearches(ctx, lens.TypeDocument, region, func(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, market string) ([]CandidateSource, ProviderStatus) {
		return s.queryDocumentDriver(ctx, fetcher, d, q, market)
	})
	if len(providers) == 0 {
		providers = []ProviderStatus{{Name: "Document catalogs", Status: "not_configured", Detail: "No document driver is loaded."}}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	candidates = rankDocumentCandidates(q, candidates)
	candidates = s.resolveArchiveTexts(ctx, candidates)

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
			if len(pageChecks) < maxDocumentPageChecks {
				pageChecks[i] = true
			}
		} else if len(probes) < maxDocumentProbes {
			probes[i] = true
		}
	}

	results := make([]VerifiedSource, len(candidates))
	var wg sync.WaitGroup
	slots := make(chan struct{}, documentProbeSlots)
	for i, candidate := range candidates {
		wg.Add(1)
		go func(idx int, c CandidateSource) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			checked := time.Now()
			result := VerifiedSource{CandidateSource: c, DetectedManifests: []string{}, VerificationStatus: "listing", VerificationMode: "Provider listing", CheckedAt: checked.UTC().Format(time.RFC3339)}
			if c.DirectStreamURL != "" && !probes[idx] {
				result.DocumentCheck = &DocumentCheck{Status: "not_checked", Method: "document_parse", CheckedAt: result.CheckedAt, EvidenceURL: c.DirectStreamURL, Pages: c.Pages, Detail: fmt.Sprintf("Not probed within this search's %d-file budget. Open the page or the reader to see it.", maxDocumentProbes)}
				result.AvailabilityCheck = uncheckedMovieAvailability(region, location.country, result.DocumentCheck.Detail)
			} else if c.DirectStreamURL != "" {
				check := probeDocument(ctx, client, c)
				result.DocumentCheck = &check
				result.VerificationMode = "Document parse"
				result.VerifyDurationMs = time.Since(checked).Milliseconds()
				if check.Status == "parsed" {
					result.Verified = true
					result.VerificationStatus = "media_reachable"
					result.DetectedManifests = []string{c.DirectStreamURL}
					result.StreamFormat = strings.ToUpper(check.Format) + " document"
					result.CleanlinessScore = 100
				}
				result.AvailabilityCheck = documentAvailability(check, region, location.country)
			} else if pageChecks[idx] && ctx.Err() == nil {
				check := checkMoviePageAvailability(ctx, client, c, region, location.country)
				check.Detail = documentWording.Replace(check.Detail)
				result.AvailabilityCheck = &check
				result.VerifyDurationMs = time.Since(checked).Milliseconds()
				if check.Status == "page_reachable" {
					result.VerificationStatus = "page_reachable"
				}
			} else {
				result.AvailabilityCheck = uncheckedMovieAvailability(region, location.country, "Not checked within this search's bounded page-check budget. Open the page to see the document.")
			}
			result.RelevanceScore = int(documentRelevance(q, c)*100 + 0.5)
			results[idx] = result
		}(i, candidate)
	}
	wg.Wait()
	matchDocumentsByText(results)
	// Up to two parsed PDFs are framed and drawn in the isolated sandbox,
	// which is what makes a document rendered on the ladder.
	rendersAttempted, rendersDone := s.renderSources(ctx, results, "document", region, location.country, renderableDocument)

	sources := make([]VerifiedSource, 0, len(results))
	verified := []VerifiedSource{}
	fingerprints := 0
	for _, result := range results {
		if result.Verified {
			verified = append(verified, result)
		}
		if result.DocumentCheck != nil && result.DocumentCheck.Fingerprint.Valid() {
			fingerprints++
		}
		sources = append(sources, result)
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if a, b := renderStatusRank(sources[i]), renderStatusRank(sources[j]); a != b {
			return a > b
		}
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
	coverage.RenderCheckedCount, coverage.RenderedCount = rendersAttempted, rendersDone
	detail := "Each document's PDF is fetched at both ends for its format and page count, and its text, from the OCR text beside it or from the PDF's own content, is fingerprinted for the index to compare with everything it has read. Up to two parsed PDFs are framed and drawn in the isolated sandbox, which makes them rendered on the ladder. "
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

// queryDocumentDriver runs one document driver's search for the title and
// keeps everything it returned, ranked by relevance.
func (s *Scout) queryDocumentDriver(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, q lens.ObjectQuery, market string) ([]CandidateSource, ProviderStatus) {
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
			candidate.Kind = lens.TypeDocument
		}
		candidates = append(candidates, candidate)
	}
	if d.Spec.Keep > 0 && len(candidates) > d.Spec.Keep {
		candidates = rankDocumentCandidates(q, candidates)[:d.Spec.Keep]
	}
	status.Detail = fmt.Sprintf("%d documents returned; the file of each is probed", len(candidates))
	if len(candidates) == 0 {
		status.Status = "empty"
	}
	return candidates, status
}

// documentIdentityOf reads a candidate's tags into a document identity.
func documentIdentityOf(c CandidateSource) lens.DocumentIdentity {
	return lens.DocumentListing(c.Title, c.Creator, c.ReleaseYear, c.Pages)
}

// documentRelevance scores a candidate against the query by its tags.
func documentRelevance(q lens.ObjectQuery, c CandidateSource) float64 {
	return lens.DocumentRelevance(q, documentIdentityOf(c))
}

// rankDocumentCandidates orders candidates by relevance, keeping the hosts'
// own order among equals, drops repeated URLs and keeps the cap.
func rankDocumentCandidates(q lens.ObjectQuery, candidates []CandidateSource) []CandidateSource {
	sort.SliceStable(candidates, func(i, j int) bool {
		return documentRelevance(q, candidates[i]) > documentRelevance(q, candidates[j])
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
		if len(out) == maxDocumentCandidates {
			break
		}
	}
	return out
}

// resolveArchiveTexts reads the Archive item metadata of the first kept
// Archive text listings, in parallel and under the request budget, so a
// listing names its PDF, the OCR text beside it and its page count.
func (s *Scout) resolveArchiveTexts(ctx context.Context, candidates []CandidateSource) []CandidateSource {
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	var wg sync.WaitGroup
	budget := 0
	for i := range candidates {
		if candidates[i].SourceType != "Archive text" || budget == maxArchiveTextMetadata {
			continue
		}
		page, err := url.Parse(candidates[i].URL)
		if err != nil || page.Hostname() != "archive.org" || !strings.HasPrefix(page.Path, "/details/") {
			continue
		}
		budget++
		wg.Add(1)
		go func(index int, identifier string) {
			defer wg.Done()
			if resolved, ok := resolveArchiveTextSource(ctx, client, candidates[index], identifier); ok {
				candidates[index] = resolved
			}
		}(i, strings.TrimPrefix(page.Path, "/details/"))
	}
	wg.Wait()
	return candidates
}

// resolveArchiveTextSource reads one item's public metadata. The item must
// be a text, public and not dark. The listing then carries its Text PDF
// (the first PDF that is not a grayscale or black-and-white derivative) as
// its file, or its plain text when it has no PDF, the OCR text beside the
// PDF as its text, the author when the listing had none, and the page
// count the item states. An item with neither stays a listing.
func resolveArchiveTextSource(ctx context.Context, client *http.Client, source CandidateSource, identifier string) (CandidateSource, bool) {
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
			MediaType  string `json:"mediatype"`
			Creator    any    `json:"creator"`
			Year       any    `json:"year"`
			Date       string `json:"date"`
			ImageCount any    `json:"imagecount"`
			Restricted any    `json:"access-restricted-item"`
		} `json:"metadata"`
		Files []struct {
			Name    string `json:"name"`
			Format  string `json:"format"`
			Size    any    `json:"size"`
			Private any    `json:"private"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.IsDark || payload.Metadata.Identifier != identifier || payload.Metadata.MediaType != "texts" {
		return source, false
	}
	if restricted := scalarMetadataString(payload.Metadata.Restricted); strings.EqualFold(restricted, "true") || restricted == "1" || payload.Metadata.Restricted == true {
		return source, false
	}
	safeName := func(name string) bool {
		return name != "" && !strings.HasPrefix(name, "/") && !strings.Contains(name, "\\") && path.Clean(name) == name && !strings.HasPrefix(name, "../")
	}
	// The Text PDF is the item's own file; a plain PDF stands in for it;
	// the grayscale, black-and-white and text-only derivatives come last.
	var pdfName, textName, ocrName string
	pdfRank := 0
	for _, file := range payload.Files {
		if !safeName(file.Name) || file.Private == true || strings.EqualFold(scalarMetadataString(file.Private), "true") || scalarMetadataString(file.Private) == "1" {
			continue
		}
		lower := strings.ToLower(file.Name)
		switch {
		case strings.HasSuffix(lower, ".pdf"):
			rank := 2
			switch {
			case file.Format == "Text PDF":
				rank = 3
			case strings.HasSuffix(lower, "_bw.pdf") || strings.HasSuffix(lower, "_text.pdf") || strings.HasSuffix(lower, "_jp2.pdf"):
				rank = 1
			}
			if rank > pdfRank {
				pdfName, pdfRank = file.Name, rank
			}
		case strings.HasSuffix(lower, "_djvu.txt"):
			ocrName = file.Name
		case strings.HasSuffix(lower, ".txt") && textName == "":
			textName = file.Name
		}
	}
	if pdfName == "" && textName == "" && ocrName == "" {
		return source, false
	}
	download := func(name string) string {
		u := &url.URL{Scheme: "https", Host: "archive.org", Path: "/download/" + identifier + "/" + name}
		return u.String()
	}
	switch {
	case pdfName != "":
		source.DirectStreamURL = download(pdfName)
	case ocrName != "":
		source.DirectStreamURL = download(ocrName)
	default:
		source.DirectStreamURL = download(textName)
	}
	switch {
	case ocrName != "":
		source.TextURL = download(ocrName)
	case textName != "":
		source.TextURL = download(textName)
	}
	source.KnownManifests = []string{source.DirectStreamURL}
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "archive_metadata", metadataURL
	source.AccessNote = "The item's public files are listed in its Archive metadata. Rights vary by item."
	source.ListingOnly = false
	if source.Creator == "" {
		source.Creator = scalarMetadataString(payload.Metadata.Creator)
	}
	if source.Pages == 0 {
		if pages, err := strconv.Atoi(scalarMetadataString(payload.Metadata.ImageCount)); err == nil && pages > 0 {
			source.Pages = pages
		}
	}
	if source.ReleaseYear == 0 {
		if match := movieYearPattern.FindString(scalarMetadataString(payload.Metadata.Year)); match != "" {
			source.ReleaseYear, _ = strconv.Atoi(match)
		} else if match := movieYearPattern.FindString(payload.Metadata.Date); match != "" {
			source.ReleaseYear, _ = strconv.Atoi(match)
		}
	}
	return source, true
}

// probeDocument fetches a document's file and reads it: a PDF's head and
// tail for the format and page count, then its text for the fingerprint.
func probeDocument(ctx context.Context, client *http.Client, source CandidateSource) DocumentCheck {
	check := DocumentCheck{Status: "unreachable", Method: "document_parse", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.DirectStreamURL, Pages: source.Pages, Detail: "The document's file could not be fetched from the check location."}
	ext := strings.ToLower(path.Ext(mustPath(source.DirectStreamURL)))
	head, status, total, err := fetchImageBytes(ctx, client, source.DirectStreamURL, documentHeadLimit, false)
	check.HTTPStatus, check.Bytes = status, total
	if err != nil {
		check.Detail = "The document's file could not be fetched from the check location: " + err.Error() + "."
		if status == http.StatusTooManyRequests {
			check.Detail = "The host rate-limited the probe (HTTP 429), so the file was not fetched this time; it can still be opened in the reader."
		}
		return check
	}
	var text, textSource string
	// The text is whole when the file ended within the bytes read.
	whole := int64(len(head)) < documentHeadLimit || (total > 0 && total <= documentHeadLimit)
	switch {
	case isPDF(head):
		var tail []byte
		if total > int64(len(head)) {
			tail, _, _, _ = fetchImageTail(ctx, client, source.DirectStreamURL, documentTailLimit)
		}
		summary, ok := summarisePDF(head, tail)
		if !ok {
			check.Status, check.Detail = "undecodable", "The file answered but its structure is not a PDF's."
			return check
		}
		check.Status, check.Format, check.Linearized = "parsed", "pdf", summary.Linearized
		if summary.Pages > 0 {
			check.Pages, check.PagesConfirmed = summary.Pages, true
		}
		switch {
		case check.PagesConfirmed && source.Pages > 0 && source.Pages != summary.Pages:
			check.Detail = fmt.Sprintf("The file is a PDF of %d pages by its own page tree, where the host stated %d.", summary.Pages, source.Pages)
		case check.PagesConfirmed:
			check.Detail = fmt.Sprintf("The file is a PDF of %d pages by its own page tree.", summary.Pages)
		case source.Pages > 0:
			check.Detail = fmt.Sprintf("The file is a PDF; its page tree was not within the bytes read, so the %d pages are the host's count.", source.Pages)
		default:
			check.Detail = "The file is a PDF; its page tree was not within the bytes read and the host stated no count."
		}
		text, textSource = summary.Text, "pdf"
	case ext == ".txt" || strings.HasPrefix(http.DetectContentType(head), "text/plain"):
		check.Status, check.Format = "parsed", "txt"
		check.Detail = "The file is plain text; it reads in the protected browser, not the reader."
		text, textSource = string(head), "text_file"
	case ext == ".epub" || ext == ".djvu" || ext == ".mobi":
		check.Status, check.Format = "unsupported", strings.TrimPrefix(ext, ".")
		check.Detail = fmt.Sprintf("The file answered as %s, which the probe cannot read yet; the page opens in the protected browser.", strings.ToUpper(check.Format))
	default:
		check.Status, check.Detail = "undecodable", "The file answered but is neither a PDF nor plain text."
		return check
	}
	// The OCR text beside a scanned PDF reads far better than the PDF's own
	// text layer, so it is preferred for the fingerprint when the item has
	// one.
	if source.TextURL != "" && source.TextURL != source.DirectStreamURL {
		if ocr, _, ocrTotal, err := fetchImageBytes(ctx, client, source.TextURL, documentTextLimit, false); err == nil && len(ocr) > 0 {
			text, textSource = string(ocr), "ocr_text"
			whole = int64(len(ocr)) < documentTextLimit || (ocrTotal > 0 && ocrTotal <= documentTextLimit)
		}
	}
	check.TextChars, check.TextSource = len(text), textSource
	if check.Status != "parsed" {
		return check
	}
	words := lens.TextWords(text)
	if len(words) < lens.MinFingerprintWords {
		check.FingerprintStatus = "too_short"
		return check
	}
	body, skipped := lens.TextBody(text)
	check.BodySkipped, check.BodyWords = skipped, len(body)
	if len(body) < lens.MinFingerprintWords {
		check.FingerprintStatus = "unreadable"
		return check
	}
	if len(body) < lens.TextFingerprintWords && !whole {
		// A scan's text layer from the first pages, or an OCR file cut by
		// the read limit: a fingerprint of a beginning would read as a
		// different text beside a fingerprint of the whole.
		check.FingerprintStatus = "partial"
		return check
	}
	fp := lens.TextFingerprint(text)
	if !fp.Valid() {
		check.FingerprintStatus = "unreadable"
		return check
	}
	check.Fingerprint, check.FingerprintStatus = &fp, "computed"
	return check
}

// mustPath is a URL's path, or the URL itself when it does not parse.
func mustPath(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return raw
}

// fetchImageTail fetches the last limit bytes of a file by a suffix range
// request; a host that ignores the range answers the whole file, of which
// the last limit bytes are kept.
func fetchImageTail(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, int, int64, error) {
	target, ok := safeAvailabilityURL(raw)
	if !ok {
		return nil, 0, 0, fmt.Errorf("the file is not on a public web host")
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
	paceProbeRequest(ctx)
	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, 0, 0, err
	}
	request.Header.Set("User-Agent", "ITEMBA-NET/2 (+https://github.com/JAPHARYROMAN/itemba-net; personal research crawler; document probe)")
	request.Header.Set("Range", "bytes=-"+strconv.FormatInt(limit, 10))
	response, err := guarded.Do(request)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("the file could not be fetched")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return nil, response.StatusCode, 0, fmt.Errorf("the file answered HTTP %d", response.StatusCode)
	}
	if response.StatusCode == http.StatusPartialContent {
		total := int64(0)
		if match := imageContentRange.FindStringSubmatch(response.Header.Get("Content-Range")); match != nil {
			total, _ = strconv.ParseInt(match[1], 10, 64)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil {
			return nil, response.StatusCode, total, fmt.Errorf("the file could not be read")
		}
		return data, response.StatusCode, total, nil
	}
	// The whole file: keep a window of the last limit bytes as it streams.
	window := make([]byte, 0, limit)
	buffer := make([]byte, 64<<10)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			window = append(window, buffer[:n]...)
			if int64(len(window)) > limit {
				window = window[int64(len(window))-limit:]
			}
		}
		if err != nil {
			break
		}
	}
	return window, response.StatusCode, response.ContentLength, nil
}

// documentAvailability reports a file probe in the document's words.
func documentAvailability(check DocumentCheck, region, country string) *AvailabilityCheck {
	out := uncheckedMovieAvailability(region, country, check.Detail)
	out.Method, out.CheckedAt, out.EvidenceURL, out.HTTPStatus = "http_media_probe", check.CheckedAt, check.EvidenceURL, check.HTTPStatus
	switch check.Status {
	case "parsed", "unsupported":
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

// matchDocumentsByText compares the fingerprints this search computed and
// says on each what the nearest other read like: the same text under
// different tags joins it, and the source takes the better relevance of
// the two; a different text under the same tags is kept apart; the index
// makes the same comparison against everything it has read before.
func matchDocumentsByText(results []VerifiedSource) {
	type read struct {
		index    int
		identity lens.DocumentIdentity
	}
	var fingerprinted []read
	for i := range results {
		check := results[i].DocumentCheck
		if check == nil || !check.Fingerprint.Valid() {
			continue
		}
		identity := documentIdentityOf(results[i].CandidateSource)
		identity.Pages, identity.Fingerprint = check.Pages, check.Fingerprint
		fingerprinted = append(fingerprinted, read{i, identity})
	}
	for _, a := range fingerprinted {
		var best *lens.IdentityMatch
		relevance := results[a.index].RelevanceScore
		for _, b := range fingerprinted {
			if a.index == b.index {
				continue
			}
			c := lens.CompareFingerprints(*a.identity.Fingerprint, *b.identity.Fingerprint)
			if !c.Known || best != nil && c.Distance >= best.Distance {
				continue
			}
			byTags, otherByTags := a.identity, b.identity
			byTags.Fingerprint, otherByTags.Fingerprint = nil, nil
			tags := lens.MatchDocuments(byTags, otherByTags)
			match := &lens.IdentityMatch{Method: lens.MethodTextFingerprint, PeerURL: results[b.index].URL, PeerTitle: results[b.index].Title, Distance: math.Round(c.Distance*1000) / 1000}
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
