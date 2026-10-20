package console

import (
	"net/http"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/formats"
	"github.com/itemba-net/core/internal/scout"
)

// formatsRoute answers GET /api/formats: the format matrix judged against
// this host, so the dashboard can say what this console presents and the
// sandbox draws here, and why a row that the code supports is not
// available on this particular host.
func (s *Server) formatsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "The format matrix is read with GET."})
		return
	}
	host := formats.Host{Converters: hostConverter().Names(), HLS: scout.HLSSupported(), Browser: browserPresent()}
	if host.Converters == nil {
		host.Converters = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": host, "formats": formats.Availability(host)})
}

// browserPresent reports whether a browser for the sandbox exists on this
// host; tests put their own answer in its place.
var browserPresent = func() bool {
	_, err := cdp.FindBrowserBinary()
	return err == nil
}
