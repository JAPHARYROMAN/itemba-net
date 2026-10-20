package console

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// resolveRoute answers GET /api/atlas/resolve?q=<words>: what the index
// takes the words to mean, across every kind unless the words name one.
// The query is read by Lens version two (ParseOpen), the objects ranked by
// Atlas (Resolve), and the answer carries the reading and the matches with
// their scores and how each was found, so the dashboard can show what the
// index resolved before the fresh search answers.
func (s *Server) resolveRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Resolution is asked with GET."})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if raw == "" || len([]rune(raw)) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Enter what to resolve, using at most 500 characters."})
		return
	}
	q := lens.ParseOpen(raw)
	answer := map[string]any{"query": q, "matches": []atlas.ObjectMatch{}}
	if s.atlas == nil {
		answer["error"] = "No index is configured on this console."
		writeJSON(w, http.StatusOK, answer)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	matches, err := s.atlas.Resolve(ctx, q, 8)
	if err != nil {
		answer["error"] = "The index could not be read."
		writeJSON(w, http.StatusOK, answer)
		return
	}
	if matches != nil {
		answer["matches"] = matches
	}
	writeJSON(w, http.StatusOK, answer)
}
