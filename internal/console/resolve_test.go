package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// TestResolveRouteReadsTheQueryAcrossKinds proves the route reads the
// words with Lens version two and answers honestly without an index.
func TestResolveRouteReadsTheQueryAcrossKinds(t *testing.T) {
	handler := newTestHandler(t, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/atlas/resolve?q=the+painting+Starry+Night+by+Van+Gogh", nil))
	var body struct {
		Query   lens.ObjectQuery `json:"query"`
		Matches []any            `json:"matches"`
		Error   string           `json:"error"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("the route answers JSON: %d %s", response.Code, response.Body.String())
	}
	if body.Query.Type != lens.TypeImage || body.Query.Open || body.Query.Title != "Starry Night" || body.Query.Creator != "Van Gogh" {
		t.Fatalf("the words are read for their kind, title and maker: %+v", body.Query)
	}
	if body.Matches == nil || len(body.Matches) != 0 || !strings.Contains(body.Error, "No index") {
		t.Fatalf("without an index the answer says so and lists nothing: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/atlas/resolve?q=Sintel", nil))
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || !body.Query.Open {
		t.Fatalf("a bare title is open to every kind: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/atlas/resolve", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("nothing to resolve is refused: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/atlas/resolve?q=x", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("resolution is GET only: %d", response.Code)
	}
}
