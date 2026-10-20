package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/itemba-net/core/internal/convert"
	"github.com/itemba-net/core/internal/formats"
)

// TestFormatsRouteJudgesTheMatrixAgainstThisHost proves the route: every
// row of the matrix comes back with whether this host can take it and why
// not, from the converters found, the HLS library and the browser.
func TestFormatsRouteJudgesTheMatrixAgainstThisHost(t *testing.T) {
	stubConverter(t, convert.Command{Name: "ebook-convert", Path: "ebook-convert", Formats: []string{"epub", "mobi"}})
	previous := browserPresent
	browserPresent = func() bool { return false }
	t.Cleanup(func() { browserPresent = previous })
	handler := newTestHandler(t, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/formats", nil))
	var body struct {
		Host    formats.Host        `json:"host"`
		Formats []formats.Available `json:"formats"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Formats) != len(formats.All()) {
		t.Fatalf("the matrix comes back whole: %d %s", response.Code, response.Body.String())
	}
	if len(body.Host.Converters) != 1 || body.Host.Converters[0] != "ebook-convert" || body.Host.Browser {
		t.Fatalf("the host says what it has: %+v", body.Host)
	}
	rows := map[string]formats.Available{}
	for _, row := range body.Formats {
		rows[row.Kind+"/"+row.Format] = row
	}
	if row := rows["document/epub"]; !row.RelayAvailable || row.SandboxAvailable || row.Because == "" {
		t.Fatalf("an EPUB is readable here, not renderable without a browser: %+v", row)
	}
	if row := rows["document/djvu"]; row.RelayAvailable {
		t.Fatalf("DjVu needs a tool this host lacks: %+v", row)
	}
	if row := rows["document/pdf"]; !row.RelayAvailable || row.SandboxAvailable {
		t.Fatalf("a PDF is readable, and not renderable without a browser: %+v", row)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/formats", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("the matrix is read with GET: %d", response.Code)
	}
}
