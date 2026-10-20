package scout

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// A bounded reading of a PDF with no library: enough of its structure to
// say it is one, how many pages it has and what its first pages say. The
// probe fetches the file's head and tail by range requests; the head holds
// the header, the linearisation dictionary when the file has one and the
// first pages' content, and the tail usually holds the page tree's root,
// which a writer appends last. Nothing here renders; that is the sandbox's
// job in a later slice.

const (
	// pdfStreamLimit bounds the bytes one content stream yields when
	// inflated, and pdfTextLimit the text read from all of them.
	pdfStreamLimit = 512 << 10
	pdfTextLimit   = 32000
	// pdfObjectLimit bounds the bytes of one page tree object read for its
	// count: a root lists every page in its /Kids before the /Count, eight
	// bytes a page.
	pdfObjectLimit = 256 << 10
)

var (
	pdfLinearized  = regexp.MustCompile(`/Linearized\b[^>]{0,200}?/N\s+(\d+)`)
	pdfPagesType   = regexp.MustCompile(`/Type\s*/Pages\b`)
	pdfCount       = regexp.MustCompile(`/Count\s+(\d+)`)
	pdfParent      = regexp.MustCompile(`/Parent\b`)
	pdfStreamStart = regexp.MustCompile(`stream\r?\n`)
	pdfTextShow    = regexp.MustCompile(`\(((?:\\.|[^\\)])*)\)\s*(?:Tj|')`)
	pdfTextArray   = regexp.MustCompile(`\[((?:\\.|[^\]])*)\]\s*TJ`)
	pdfArrayString = regexp.MustCompile(`\(((?:\\.|[^\\)])*)\)`)
	pdfEscape      = strings.NewReplacer(`\(`, "(", `\)`, ")", `\\`, `\`, `\n`, "\n", `\r`, "", `\t`, " ")
)

// pdfSummary is what the bounded reading proved.
type pdfSummary struct {
	// Pages is the page count read from the file: the linearisation
	// dictionary's /N or the page tree root's /Count. Zero when neither was
	// within the bytes read.
	Pages int
	// Linearized says the file is linearised, so its first page comes first.
	Linearized bool
	// Text is the text the first pages' content streams show, in order,
	// bounded by pdfTextLimit.
	Text string
}

// summarisePDF reads the head and tail of a PDF. It reports false when the
// head is not a PDF at all.
func summarisePDF(head, tail []byte) (pdfSummary, bool) {
	var summary pdfSummary
	if !isPDF(head) {
		return summary, false
	}
	if match := pdfLinearized.FindSubmatch(head); match != nil {
		summary.Linearized = true
		summary.Pages, _ = strconv.Atoi(string(match[1]))
	}
	if summary.Pages == 0 {
		summary.Pages = pdfRootCount(head)
	}
	if summary.Pages == 0 {
		summary.Pages = pdfRootCount(tail)
	}
	summary.Text = pdfText(head)
	return summary, true
}

// pdfRootCount finds the page tree's root, a /Pages dictionary with no
// /Parent, and reads its /Count. Several roots (an incremental update
// rewriting the tree) give the largest.
func pdfRootCount(data []byte) int {
	best := 0
	for _, loc := range pdfPagesType.FindAllIndex(data, -1) {
		start := bytes.LastIndex(data[:loc[0]], []byte("obj"))
		if start < 0 || loc[0]-start > 4096 {
			start = max(0, loc[0]-4096)
		}
		end := bytes.Index(data[loc[1]:], []byte("endobj"))
		if end < 0 || end > pdfObjectLimit {
			end = min(len(data)-loc[1], pdfObjectLimit)
		}
		object := data[start : loc[1]+end]
		if pdfParent.Match(object) {
			continue
		}
		if match := pdfCount.FindSubmatch(object); match != nil {
			if count, err := strconv.Atoi(string(match[1])); err == nil && count > best {
				best = count
			}
		}
	}
	return best
}

// pdfText reads the text-showing operators of every content stream in the
// bytes, inflating Flate-encoded streams, in the order the streams appear.
// Image streams and streams with other filters are skipped.
func pdfText(data []byte) string {
	var out strings.Builder
	for _, loc := range pdfStreamStart.FindAllIndex(data, -1) {
		if loc[0] >= 3 && string(data[loc[0]-3:loc[0]]) == "end" {
			// "endstream" ends a stream; it does not begin one.
			continue
		}
		dictStart := bytes.LastIndex(data[:loc[0]], []byte("<<"))
		if dictStart < 0 || loc[0]-dictStart > 2048 {
			continue
		}
		dict := data[dictStart:loc[0]]
		if bytes.Contains(dict, []byte("/Image")) || bytes.Contains(dict, []byte("/FontFile")) || bytes.Contains(dict, []byte("/XRef")) || bytes.Contains(dict, []byte("/ObjStm")) {
			continue
		}
		body := data[loc[1]:]
		if end := bytes.Index(body, []byte("endstream")); end >= 0 {
			body = body[:end]
		}
		var content []byte
		switch {
		case bytes.Contains(dict, []byte("/FlateDecode")):
			reader, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				continue
			}
			// A truncated stream at the end of the head still yields what it
			// held before the cut; the error is the cut.
			content, _ = io.ReadAll(io.LimitReader(reader, pdfStreamLimit))
			reader.Close()
		case bytes.Contains(dict, []byte("/Filter")):
			continue
		default:
			content = body
			if len(content) > pdfStreamLimit {
				content = content[:pdfStreamLimit]
			}
		}
		if !bytes.Contains(content, []byte("BT")) {
			continue
		}
		for _, match := range pdfTextArray.FindAllSubmatch(content, -1) {
			for _, part := range pdfArrayString.FindAllSubmatch(match[1], -1) {
				out.WriteString(pdfEscape.Replace(string(part[1])))
			}
			out.WriteByte(' ')
		}
		for _, match := range pdfTextShow.FindAllSubmatch(content, -1) {
			out.WriteString(pdfEscape.Replace(string(match[1])))
			out.WriteByte(' ')
		}
		if out.Len() >= pdfTextLimit {
			break
		}
	}
	text := out.String()
	if len(text) > pdfTextLimit {
		text = text[:pdfTextLimit]
	}
	return strings.Join(strings.Fields(text), " ")
}

// isPDF says the bytes begin with a PDF header, after a byte-order mark or
// whitespace that some writers put first.
func isPDF(head []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(head, "\xef\xbb\xbf \r\n\t"), []byte("%PDF-"))
}
