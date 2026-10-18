package console

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/scout"
)

type movieDiscoveryFunc func(context.Context, string, string) scout.MovieDiscoveryReport

func movieDiscoveryHandler(discover, fawesome movieDiscoveryFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use GET to browse movie suggestions."})
			return
		}
		mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
		if mode == "" {
			mode = "popular"
		}
		region := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("region")))
		if region == "" {
			region = "ALL"
		}
		if mode != "popular" && mode != "recent" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Choose Popular or Recent movies."})
			return
		}
		if region != "ALL" && (len(region) != 2 || region[0] < 'A' || region[0] > 'Z' || region[1] < 'A' || region[1] > 'Z') {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Choose Worldwide or a two-letter country code."})
			return
		}
		source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
		provider := discover
		switch source {
		case "", "apple":
		case "fawesome":
			if fawesome == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Fawesome suggestions are unavailable. You can still search a title."})
				return
			}
			provider = fawesome
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Choose Apple charts or Fawesome movie suggestions."})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, provider(ctx, mode, region))
	}
}
