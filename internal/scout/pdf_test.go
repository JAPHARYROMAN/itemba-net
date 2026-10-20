package scout

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"
)

// buildPDF writes a small PDF by hand: a catalog, one page object and one
// content stream per entry of contents (raw content operators), and the
// page tree's root last, as writers append it. Flate compresses the
// streams; linearized adds the dictionary a linearised file begins with.
// The cross-reference table is omitted: nothing here reads it.
func buildPDF(contents []string, flate, linearized bool) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	if linearized {
		fmt.Fprintf(&b, "1 0 obj\n<< /Linearized 1 /L 99999 /H [ 0 0 ] /O 10 /E 0 /N %d /T 0 >>\nendobj\n", len(contents))
	}
	b.WriteString("2 0 obj\n<< /Type /Catalog /Pages 3 0 R >>\nendobj\n")
	b.WriteString("4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	for i, content := range contents {
		num := 10 + 2*i
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Page /Parent 3 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 4 0 R >> >> >>\nendobj\n", num, num+1)
		data, filter := []byte(content), ""
		if flate {
			var z bytes.Buffer
			w := zlib.NewWriter(&z)
			w.Write(data)
			w.Close()
			data, filter = z.Bytes(), " /Filter /FlateDecode"
		}
		fmt.Fprintf(&b, "%d 0 obj\n<< /Length %d%s >>\nstream\n", num+1, len(data), filter)
		b.Write(data)
		b.WriteString("\nendstream\nendobj\n")
	}
	b.WriteString("3 0 obj\n<< /Type /Pages /Kids [")
	for i := range contents {
		fmt.Fprintf(&b, " %d 0 R", 10+2*i)
	}
	fmt.Fprintf(&b, " ] /Count %d >>\nendobj\n", len(contents))
	b.WriteString("trailer\n<< /Root 2 0 R >>\n%%EOF\n")
	return b.Bytes()
}

// textPDF is buildPDF with one line of text shown on each page.
func textPDF(pages []string, flate, linearized bool) []byte {
	contents := make([]string, 0, len(pages))
	for _, page := range pages {
		contents = append(contents, fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", page))
	}
	return buildPDF(contents, flate, linearized)
}

func TestPDFSummaryReadsPagesAndTextFromBothEnds(t *testing.T) {
	pages := []string{"The river ran past the old mill", "where the miller kept a ledger", "of every sack that crossed the bridge"}
	plain := textPDF(pages, false, false)
	summary, ok := summarisePDF(plain, nil)
	if !ok || summary.Pages != 3 || summary.Linearized || summary.Text != strings.Join(pages, " ") {
		t.Fatalf("a plain PDF read whole gives its page count and its text in order: %+v %v", summary, ok)
	}

	// Flate streams, with the file cut as the probe fetches it: a head that
	// ends inside the second stream and a tail that holds the page tree.
	compressed := textPDF(pages, true, false)
	cut := bytes.Index(compressed, []byte("12 0 obj")) + 60
	head, tail := compressed[:cut], compressed[len(compressed)-200:]
	summary, ok = summarisePDF(head, tail)
	if !ok || summary.Pages != 3 || !strings.HasPrefix(summary.Text, pages[0]) || strings.Contains(summary.Text, pages[2]) {
		t.Fatalf("a cut file gives the first page's text from the head and the count from the tail: %+v", summary)
	}
	if summary, _ := summarisePDF(head, nil); summary.Pages != 0 {
		t.Fatalf("no page tree within the bytes read means no count: %+v", summary)
	}

	// A linearised file states its count first.
	summary, ok = summarisePDF(textPDF(pages, true, true)[:400], nil)
	if !ok || !summary.Linearized || summary.Pages != 3 {
		t.Fatalf("a linearised header gives the count before any page tree: %+v", summary)
	}

	// Text arrays, escapes, the ' operator and streams to skip.
	contents := []string{
		`BT /F1 12 Tf [(Hel) -20 (lo\)) 5 (, world)] TJ ET`,
		"BT /F1 12 Tf (second \\(page\\)) ' ET",
	}
	mixed := buildPDF(contents, false, false)
	mixed = bytes.Replace(mixed, []byte("endobj\n3 0 obj"), []byte("endobj\n30 0 obj\n<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /Length 10 >>\nstream\nBT (no) Tj\nendstream\nendobj\n3 0 obj"), 1)
	summary, ok = summarisePDF(mixed, nil)
	if !ok || summary.Text != "Hello), world second (page)" || summary.Pages != 2 {
		t.Fatalf("text arrays and escapes read as text and image streams are skipped: %+v", summary)
	}

	// Several page tree nodes: the root has no parent and the largest root
	// count wins over an intermediate node's.
	tree := []byte("5 0 obj\n<< /Type /Pages /Parent 3 0 R /Kids [ 6 0 R ] /Count 9 >>\nendobj\n3 0 obj\n<< /Type /Pages /Kids [ 5 0 R 7 0 R ] /Count 12 >>\nendobj\n")
	if n := pdfRootCount(tree); n != 12 {
		t.Fatalf("the root's count is the page count, not a branch's: %d", n)
	}

	if _, ok := summarisePDF([]byte("<html><body>not a pdf</body></html>"), nil); ok {
		t.Fatal("an HTML answer is not a PDF")
	}
	if !isPDF([]byte("\xef\xbb\xbf\n%PDF-1.7\n")) || isPDF([]byte("PDF-1.7")) {
		t.Fatal("the header may follow a byte-order mark or whitespace and must be present")
	}
}
