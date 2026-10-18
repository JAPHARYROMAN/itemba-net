package console

import (
	"context"
	"net/http"
	"time"
)

// crawlerStatus answers GET /api/crawler/status. Without a crawler it says
// why: the role does not run one, or no database is configured.
func (s *Server) crawlerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use GET to read the crawl status."})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.crawler == nil {
		detail := "This process does not run the crawler. Start the binary with --role all or --role crawler."
		if s.atlas == nil {
			detail = "The crawler needs a database. Set ITEMBA_DATABASE_URL and restart."
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "detail": detail})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.crawler.Status(ctx))
}
