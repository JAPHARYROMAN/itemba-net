package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/convert"
)

var convertedPDF = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")

// stubConverter puts a converter with the given commands in the route's
// hands, whose tool is a fake that writes a fixed PDF.
func stubConverter(t *testing.T, commands ...convert.Command) {
	t.Helper()
	previous := hostConverter
	stub := &convert.Converter{Commands: commands, CacheDir: t.TempDir(), CacheLimit: 1 << 20, InputLimit: 1 << 20, Timeout: 5 * time.Second, Run: func(_ context.Context, cmd *exec.Cmd) error {
		return os.WriteFile(filepath.Join(cmd.Dir, "input.pdf"), convertedPDF, 0o600)
	}}
	hostConverter = func() *convert.Converter { return stub }
	t.Cleanup(func() { hostConverter = previous })
}

// TestConversionRouteServesAConvertedPDFAndSaysWhenItCannot proves the
// reader's conversion path: a supported e-book is fetched, converted once,
// described with info=1 and served as a PDF with ranges; formats the host
// cannot convert and formats the reader never converts are refused with
// the reason, and a host without converters says so.
func TestConversionRouteServesAConvertedPDFAndSaysWhenItCannot(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/book.epub":
			w.Header().Set("Content-Type", "application/epub+zip")
			_, _ = w.Write([]byte("PK\x03\x04 a fake epub"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	stubConverter(t, convert.Command{Name: "ebook-convert", Path: "ebook-convert", Formats: []string{"epub", "mobi"}})
	handler := newTestHandler(t, "")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?info=1&url="+source.URL+"/book.epub", nil))
	var status convertStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || !status.Converted || status.Cached || !status.Supported || status.Converter != "ebook-convert" || status.Format != "epub" || status.Bytes != int64(len(convertedPDF)) || status.Error != "" {
		t.Fatalf("info converts the e-book and says how: %d %s", response.Code, response.Body.String())
	}
	if len(status.Converters) != 1 || len(status.Formats) != 2 {
		t.Fatalf("the status lists the host's converters and formats: %+v", status)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/book.epub", nil)
	request.Header.Set("Range", "bytes=0-0")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Header().Get("Content-Type") != "application/pdf" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Body.Len() != 1 || response.Header().Get("Content-Range") == "" {
		t.Fatalf("the PDF is served with ranges, as the reader probes it: %d %v %d", response.Code, response.Header(), response.Body.Len())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/book.epub", nil))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "%PDF-") || response.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("the whole PDF is served without a policy that would keep the viewer from loading: %d %v", response.Code, response.Header())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?info=1&url="+source.URL+"/book.epub", nil))
	if json.Unmarshal(response.Body.Bytes(), &status) != nil || !status.Converted || !status.Cached {
		t.Fatalf("the second request finds the cached PDF: %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/scan.djvu", nil))
	if response.Code != http.StatusServiceUnavailable || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Supported || !strings.Contains(status.Error, "No converter on this host takes DJVU") {
		t.Fatalf("a format this host cannot convert is refused with the reason: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/notes.txt", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "not one of them") {
		t.Fatalf("a format the reader never converts is refused: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/missing.epub", nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "HTTP 404") {
		t.Fatalf("a file the host does not have is reported: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/convert?url="+source.URL+"/book.epub", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("conversion is GET only: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url=ftp://example.org/book.epub", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("only web addresses are converted: %d", response.Code)
	}

	stubConverter(t)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?info=1&url="+source.URL+"/book.epub", nil))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Supported || status.Converted || !strings.Contains(status.Error, "none is installed") || len(status.Converters) != 0 {
		t.Fatalf("a host without converters says so in the info: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/convert?url="+source.URL+"/book.epub", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("and refuses to serve: %d", response.Code)
	}
}
