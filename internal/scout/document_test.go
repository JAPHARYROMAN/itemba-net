package scout

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// documentSampleText composes a deterministic text of n words from a
// vocabulary, so the fixtures can hold the same text twice and a different
// one without quoting anyone.
func documentSampleText(vocabulary string, seed uint32, n int) string {
	words := strings.Fields(vocabulary)
	out := make([]string, 0, n)
	x := seed
	for i := 0; i < n; i++ {
		x = x*1664525 + 1013904223
		out = append(out, words[(x>>16)%uint32(len(words))])
	}
	return strings.Join(out, " ")
}

const (
	millWords  = "the river ran past the old mill where the miller kept a ledger of every sack that crossed the bridge and the ferryman counted the lanterns on the far bank while the geese slept under the willow and nobody asked why the bell rang twice at dusk"
	coastWords = "salt wind over shingle beach gulls wheel above tide pools keeper trims lamp before fog rolls in boats return late nets heavy with herring harbour master logs each arrival chalk cliff crumbles slowly into grey sea"
)

// pagesOf splits a text into lines of twelve words, one PDF page each.
func pagesOf(text string) []string {
	words := strings.Fields(text)
	var pages []string
	for len(words) > 0 {
		n := min(12, len(words))
		pages = append(pages, strings.Join(words[:n], " "))
		words = words[n:]
	}
	return pages
}

// rangeResponse answers a Range request over a file as the Archive's
// download hosts do: a prefix, a suffix, or the whole file when no range
// was asked for.
func rangeResponse(req *http.Request, contentType string, file []byte) *http.Response {
	total := len(file)
	header := req.Header.Get("Range")
	switch {
	case strings.HasPrefix(header, "bytes=-"):
		n, _ := strconv.Atoi(strings.TrimPrefix(header, "bytes=-"))
		n = min(n, total)
		response := searchFixtureResponse(206, contentType, string(file[total-n:]))
		response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", total-n, total-1, total))
		return response
	case strings.HasPrefix(header, "bytes=0-"):
		end, _ := strconv.Atoi(strings.TrimPrefix(header, "bytes=0-"))
		end = min(end, total-1)
		response := searchFixtureResponse(206, contentType, string(file[:end+1]))
		response.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", end, total))
		return response
	}
	response := searchFixtureResponse(200, contentType, string(file))
	response.ContentLength = int64(total)
	return response
}

// documentFixtureScout serves an Archive texts search with five items: a
// scanned book with a Text PDF and OCR text, another edition of the same
// text under another title with only its PDF, a different text under the
// book's own title and author, a transcription with only a text file, and
// an item whose metadata is dark, so it stays a listing.
func documentFixtureScout(t *testing.T) (*Scout, func() []string) {
	t.Helper()
	body := documentSampleText(millWords, 7, 900)
	edition := documentSampleText(coastWords, 3, 50) + " " + body
	other := documentSampleText(coastWords, 11, 900)
	files := map[string]struct {
		contentType string
		data        []byte
	}{
		"/0/items/alice-scan/alice-scan.pdf":            {"application/pdf", textPDF(pagesOf("scan text layer "+documentSampleText(coastWords, 5, 60)), true, false)},
		"/download/alice-scan/alice-scan_djvu.txt":      {"text/plain", []byte("ALICE\n\n" + body)},
		"/download/alice-edition/alice-edition.pdf":     {"application/pdf", textPDF(pagesOf(edition), true, false)},
		"/download/alice-other/alice-other.pdf":         {"application/pdf", textPDF(pagesOf(other), false, true)},
		"/download/alice-gutenberg/alice-gutenberg.txt": {"text/plain", []byte(body)},
	}
	metadata := map[string]string{
		"alice-scan":      `{"metadata":{"identifier":"alice-scan","mediatype":"texts","creator":["Carroll, Lewis, 1832-1898"],"year":"1865","imagecount":"3"},"files":[{"name":"alice-scan_bw.pdf","format":"Grayscale PDF"},{"name":"alice-scan.pdf","format":"Text PDF","size":"13276855"},{"name":"alice-scan_djvu.txt","format":"DjVuTXT"},{"name":"alice-scan.epub","format":"EPUB"}]}`,
		"alice-edition":   `{"metadata":{"identifier":"alice-edition","mediatype":"texts","creator":"Carroll, Lewis","date":"1920-01-01"},"files":[{"name":"alice-edition.pdf","format":"Text PDF"},{"name":"alice-edition.epub","format":"EPUB"}]}`,
		"alice-other":     `{"metadata":{"identifier":"alice-other","mediatype":"texts","creator":"Carroll, Lewis","imagecount":99},"files":[{"name":"alice-other.pdf","format":"Text PDF"}]}`,
		"alice-gutenberg": `{"metadata":{"identifier":"alice-gutenberg","mediatype":"texts","creator":"Lewis Carroll"},"files":[{"name":"alice-gutenberg.txt","format":"Text"},{"name":"alice-gutenberg.htm","format":"HTML"}]}`,
		"alice-dark":      `{"is_dark":true,"metadata":{"identifier":"alice-dark","mediatype":"texts"},"files":[{"name":"alice-dark.pdf","format":"Text PDF"}]}`,
	}
	var mu sync.Mutex
	var requests []string
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, req.URL.Host+req.URL.Path)
		mu.Unlock()
		switch {
		case req.URL.Host == "archive.org" && req.URL.Path == "/advancedsearch.php":
			if q := req.URL.Query().Get("q"); q != `title:("Alice in Wonderland") AND mediatype:texts` {
				t.Errorf("unexpected document query: %s", q)
			}
			return searchFixtureResponse(200, "application/json", `{"response":{"docs":[
				{"identifier":"alice-scan","title":"Alice in Wonderland","creator":["Carroll, Lewis, 1832-1898"],"year":"1865","imagecount":3,"collection":["americana"]},
				{"identifier":"alice-edition","title":"Adventures Under Ground","creator":"Carroll, Lewis","date":"1920-01-01T00:00:00Z"},
				{"identifier":"alice-other","title":"Alice in Wonderland","creator":"Carroll, Lewis","imagecount":99},
				{"identifier":"alice-gutenberg","title":"Alice in Wonderland","creator":"Lewis Carroll"},
				{"identifier":"alice-dark","title":"Alice in Wonderland annotated"}
			]}}`), nil
		case req.URL.Host == "archive.org" && strings.HasPrefix(req.URL.Path, "/metadata/"):
			if payload, ok := metadata[strings.TrimPrefix(req.URL.Path, "/metadata/")]; ok {
				return searchFixtureResponse(200, "application/json", payload), nil
			}
			return searchFixtureResponse(200, "application/json", `{}`), nil
		case req.URL.Host == "archive.org" && req.URL.Path == "/download/alice-scan/alice-scan.pdf":
			// The Archive answers a download with a redirect to the host
			// that holds the item.
			response := searchFixtureResponse(302, "text/plain", "")
			response.Header.Set("Location", "https://dn760105.eu.archive.org/0/items/alice-scan/alice-scan.pdf")
			return response, nil
		case req.URL.Host == "archive.org" && req.URL.Path == "/details/alice-dark":
			return searchFixtureResponse(200, "text/html; charset=utf-8", `<html><head><title>Alice in Wonderland annotated : Free Download, Borrow, and Streaming : Internet Archive</title></head><body><h1>Alice in Wonderland annotated</h1><p>An item page whose files are dark, kept here as a page for the probe to reach and read.</p></body></html>`), nil
		case req.URL.Host == "archive.org" || req.URL.Host == "dn760105.eu.archive.org":
			if file, ok := files[req.URL.Path]; ok {
				return rangeResponse(req, file.contentType, file.data), nil
			}
			return searchFixtureResponse(404, "text/plain", "no such file"), nil
		}
		return nil, fmt.Errorf("unexpected host for a document search: %s", req.URL)
	})
	s.UseDrivers(testDrivers(t))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func TestDocumentSearchParsesFilesAndMatchesTextsByFingerprint(t *testing.T) {
	s, requests := documentFixtureScout(t)
	report := s.SearchWithOptions(context.Background(), "Alice in Wonderland by Lewis Carroll", "ALL", SearchOptions{Kind: lens.TypeDocument, FullLengthOnly: true})
	if report.Kind != lens.TypeDocument || report.FullLengthOnly {
		t.Fatalf("a document report names its kind and ignores the movie length preference: %+v", report)
	}
	for _, request := range requests() {
		if !strings.HasPrefix(request, "archive.org/") && !strings.HasPrefix(request, "dn760105.eu.archive.org/") && request != "www.cloudflare.com/cdn-cgi/trace" {
			t.Fatalf("a document search reached %s; only the document drivers' hosts may be asked", request)
		}
	}
	if len(report.Sources) != 5 || report.VerifiedCount != 4 {
		var states []string
		for _, source := range report.Sources {
			state := source.ID + ":" + source.VerificationStatus
			if source.DocumentCheck != nil {
				state += ":" + source.DocumentCheck.Status + ":" + source.DocumentCheck.Detail
			}
			states = append(states, state)
		}
		t.Fatalf("five listings answer, four files parse: %d sources, %d verified: %v", len(report.Sources), report.VerifiedCount, states)
	}
	byID := map[string]VerifiedSource{}
	for _, source := range report.Sources {
		byID[source.ID] = source
	}
	scan := byID["ia-text-alice-scan"]
	if scan.Kind != lens.TypeDocument || scan.Creator != "Carroll, Lewis, 1832-1898" || scan.Pages != 3 || scan.DirectStreamURL != "https://archive.org/download/alice-scan/alice-scan.pdf" || scan.TextURL != "https://archive.org/download/alice-scan/alice-scan_djvu.txt" || scan.AccessType != "free" {
		t.Fatalf("the metadata names the Text PDF, not the grayscale one, and the OCR text beside it: %+v", scan.CandidateSource)
	}
	check := scan.DocumentCheck
	if check == nil || check.Status != "parsed" || check.Format != "pdf" || check.Pages != 6 || !check.PagesConfirmed || check.HTTPStatus != 206 || check.Bytes == 0 {
		t.Fatalf("the PDF parses through the redirect with its own page count, where the host said three: %+v", check)
	}
	if check.TextSource != "ocr_text" || check.FingerprintStatus != "computed" || !check.Fingerprint.Valid() || check.Fingerprint.Type != lens.FingerprintTextSimhash || !strings.Contains(check.Detail, "where the host stated 3") {
		t.Fatalf("the OCR text is fingerprinted in preference to the scan's text layer: %+v", check)
	}
	if !scan.Verified || scan.VerificationStatus != "media_reachable" || scan.StreamFormat != "PDF document" || scan.AvailabilityCheck == nil || scan.AvailabilityCheck.Status != "media_reachable" {
		t.Fatalf("a parsed file is reachable media: %+v", scan)
	}
	edition := byID["ia-text-alice-edition"]
	if edition.DocumentCheck == nil || edition.DocumentCheck.TextSource != "pdf" || edition.DocumentCheck.FingerprintStatus != "computed" || edition.DocumentCheck.Pages == 0 {
		t.Fatalf("a PDF without OCR text is fingerprinted from its own content streams: %+v", edition.DocumentCheck)
	}
	gutenberg := byID["ia-text-alice-gutenberg"]
	if scan.Identity == nil || scan.Identity.Method != lens.MethodTextFingerprint || scan.Identity.Verdict != lens.VerdictSame || scan.Identity.Effect != lens.EffectConfirmed || scan.Identity.PeerURL != gutenberg.URL || scan.Identity.Distance != 0 {
		t.Fatalf("the scan's OCR text and the transcription are the same text under the same tags: %+v", scan.Identity)
	}
	if edition.Identity == nil || edition.Identity.Verdict != lens.VerdictSame || edition.Identity.Effect != lens.EffectJoined || edition.Identity.Distance > lens.SameTextThreshold {
		t.Fatalf("the same text under another title is joined by its fingerprint: %+v", edition.Identity)
	}
	if !strings.Contains(edition.Identity.Detail, "Reads the same as") || !strings.Contains(edition.Identity.Detail, "different tags") || strings.Contains(edition.Identity.Detail, "ound") {
		t.Fatalf("the verdict speaks of text: %q", edition.Identity.Detail)
	}
	if edition.RelevanceScore != scan.RelevanceScore || edition.RelevanceScore < 90 {
		t.Fatalf("a joined edition takes the better relevance: %d vs %d", edition.RelevanceScore, scan.RelevanceScore)
	}
	other := byID["ia-text-alice-other"]
	if other.DocumentCheck == nil || !other.DocumentCheck.Linearized || other.DocumentCheck.Pages != 75 || other.DocumentCheck.Fingerprint == nil {
		t.Fatalf("a linearised PDF states its count first: %+v", other.DocumentCheck)
	}
	if other.Identity == nil || other.Identity.Verdict != lens.VerdictDifferent || other.Identity.Distance < 0.3 {
		t.Fatalf("a different text is told apart: %+v", other.Identity)
	}
	if gutenberg.DocumentCheck == nil || gutenberg.DocumentCheck.Status != "parsed" || gutenberg.DocumentCheck.Format != "txt" || gutenberg.DocumentCheck.TextSource != "text_file" || gutenberg.DocumentCheck.FingerprintStatus != "computed" {
		t.Fatalf("a transcription with only a text file is read as text: %+v", gutenberg.DocumentCheck)
	}
	if gutenberg.Identity == nil || gutenberg.Identity.Verdict != lens.VerdictSame || gutenberg.Identity.Effect == lens.EffectKeptApart {
		t.Fatalf("the same text as a text file is the same text: %+v", gutenberg.Identity)
	}
	dark := byID["ia-text-alice-dark"]
	if dark.DocumentCheck != nil || !dark.ListingOnly || dark.VerificationStatus != "page_reachable" || dark.AvailabilityCheck == nil || dark.AvailabilityCheck.Status != "page_reachable" {
		t.Fatalf("a dark item stays a listing and has its page checked: %+v %+v", dark.CandidateSource, dark.AvailabilityCheck)
	}
	if detail := strings.ToLower(dark.AvailabilityCheck.Detail); strings.Contains(detail, "movie") || strings.Contains(detail, "playback") {
		t.Fatalf("the page check speaks of a document: %q", dark.AvailabilityCheck.Detail)
	}
	if report.Sources[0].VerificationStatus != "media_reachable" || report.Sources[4].ID != "ia-text-alice-dark" {
		t.Fatalf("parsed files lead and the page-only listing trails: %v", []string{report.Sources[0].ID, report.Sources[1].ID, report.Sources[2].ID, report.Sources[3].ID, report.Sources[4].ID})
	}
	coverage := report.AvailabilityChecks
	if coverage == nil || coverage.CheckedCount != 5 || coverage.FingerprintCount != 4 || !strings.Contains(coverage.Detail, "fingerprinted") || strings.Contains(strings.ToLower(coverage.Detail), "movie") {
		t.Fatalf("the coverage counts the fingerprints in the document's words: %+v", coverage)
	}
	for _, provider := range report.Providers {
		if provider.Name != "Internet Archive texts" || provider.Status != "ok" {
			t.Fatalf("only the document driver reports, and it answered: %+v", report.Providers)
		}
	}
}

func TestDocumentProbeTellsFormatsApartAndKeepsTheSameTagsApart(t *testing.T) {
	pdf := textPDF(pagesOf(documentSampleText(millWords, 2, 200)), true, false)
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		switch strings.TrimPrefix(req.URL.Path, "/download/x") {
		case "/book.epub":
			return searchFixtureResponse(200, "application/epub+zip", "PK\x03\x04"+strings.Repeat("\x00", 64)), nil
		case "/page.html":
			return searchFixtureResponse(200, "text/html", "<html><body>not a document</body></html>"), nil
		case "/gone.pdf":
			return searchFixtureResponse(404, "text/plain", "gone"), nil
		case "/busy.pdf":
			return searchFixtureResponse(429, "text/plain", "slow down"), nil
		case "/short.txt":
			return searchFixtureResponse(200, "text/plain", "a few words only"), nil
		}
		return rangeResponse(req, "application/pdf", pdf), nil
	})}
	source := CandidateSource{Kind: lens.TypeDocument, URL: "https://archive.org/details/x", DirectStreamURL: "https://archive.org/download/x/book.epub"}
	if check := probeDocument(context.Background(), client, source); check.Status != "unsupported" || check.Format != "epub" || documentAvailability(check, "ALL", "TZ").Status != "media_reachable" {
		t.Fatalf("an EPUB is unsupported yet reachable: %+v", check)
	}
	source.DirectStreamURL = "https://archive.org/download/x/page.html"
	if check := probeDocument(context.Background(), client, source); check.Status != "undecodable" || documentAvailability(check, "ALL", "TZ").Status != "unconfirmed" {
		t.Fatalf("an HTML answer is undecodable: %+v", check)
	}
	source.DirectStreamURL = "https://archive.org/download/x/gone.pdf"
	if check := probeDocument(context.Background(), client, source); check.Status != "unreachable" || check.HTTPStatus != 404 || documentAvailability(check, "ALL", "TZ").Status != "not_found" {
		t.Fatalf("a missing file is not found: %+v", check)
	}
	source.DirectStreamURL = "https://archive.org/download/x/busy.pdf"
	if check := probeDocument(context.Background(), client, source); check.Status != "unreachable" || check.HTTPStatus != 429 || !strings.Contains(check.Detail, "rate-limited") || documentAvailability(check, "ALL", "TZ").Status != "access_restricted" {
		t.Fatalf("a rate limit is said: %+v", check)
	}
	source.DirectStreamURL = "https://archive.org/download/x/short.txt"
	if check := probeDocument(context.Background(), client, source); check.Status != "parsed" || check.Format != "txt" || check.FingerprintStatus != "too_short" || check.Fingerprint != nil {
		t.Fatalf("too little text parses without a fingerprint: %+v", check)
	}
	source.DirectStreamURL, source.Pages = "https://archive.org/download/x/book.pdf", 17
	check := probeDocument(context.Background(), client, source)
	if check.Status != "parsed" || check.Pages != 17 || !check.PagesConfirmed || check.TextSource != "pdf" || check.FingerprintStatus != "computed" {
		t.Fatalf("a PDF whose count agrees with the host's: %+v", check)
	}

	// Two documents under the same title and author that read differently
	// are kept apart by the search's own comparison.
	a := lens.TextFingerprint(documentSampleText(millWords, 2, 400))
	b := lens.TextFingerprint(documentSampleText(coastWords, 2, 400))
	results := []VerifiedSource{
		{CandidateSource: CandidateSource{Title: "Alice in Wonderland", Creator: "Carroll, Lewis", URL: "https://archive.org/details/a"}, DocumentCheck: &DocumentCheck{Status: "parsed", Fingerprint: &a}, RelevanceScore: 90},
		{CandidateSource: CandidateSource{Title: "Alice in Wonderland", Creator: "Lewis Carroll", URL: "https://archive.org/details/b"}, DocumentCheck: &DocumentCheck{Status: "parsed", Fingerprint: &b}, RelevanceScore: 90},
	}
	matchDocumentsByText(results)
	if results[0].Identity == nil || results[0].Identity.Verdict != lens.VerdictDifferent || results[0].Identity.Effect != lens.EffectKeptApart || !strings.Contains(results[0].Identity.Detail, "kept apart") {
		t.Fatalf("a different text under the same tags is kept apart: %+v", results[0].Identity)
	}
}

func TestFetchImageTailKeepsTheLastBytes(t *testing.T) {
	file := []byte(strings.Repeat("x", 5000) + "THE END")
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/whole.bin") {
			return searchFixtureResponse(200, "application/octet-stream", string(file)), nil
		}
		return rangeResponse(req, "application/octet-stream", file), nil
	})}
	data, status, total, err := fetchImageTail(context.Background(), client, "https://archive.org/download/x/ranged.bin", 100)
	if err != nil || status != 206 || total != int64(len(file)) || len(data) != 100 || !strings.HasSuffix(string(data), "THE END") {
		t.Fatalf("a suffix range gives the last bytes: %d %d %d %v", status, total, len(data), err)
	}
	data, status, _, err = fetchImageTail(context.Background(), client, "https://archive.org/download/x/whole.bin", 100)
	if err != nil || status != 200 || len(data) != 100 || !strings.HasSuffix(string(data), "THE END") {
		t.Fatalf("a host that ignores the range still yields the last bytes: %d %d %v", status, len(data), err)
	}
}
