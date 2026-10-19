package relay

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/shield"
)

// Every relay response is nosniffed, and every response but a PDF is
// sandboxed, so an upstream that answers a media request with a page cannot
// run script in the console's origin while the document reader can still
// frame a PDF for Chrome's viewer, which refuses a sandboxed response.
func TestRelayResponsesAreSandboxed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Accept-Ranges", "bytes")
			_, _ = w.Write([]byte("not really an mp4"))
		case "/wall.mp4":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("<script>alert(document.cookie)</script>"))
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4.0,\nseg0.ts\n"))
		case "/paper.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	handler := NewHandler(shield.New())
	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/relay/stream?url="+url.QueryEscape(upstream.URL+path), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/clip.mp4", "/wall.mp4", "/index.m3u8"} {
		rec := serve(path)
		if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
			t.Errorf("%s: Content-Security-Policy = %q, want %q", path, got, "sandbox")
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want %q", path, got, "nosniff")
		}
	}

	pdf := serve("/paper.pdf")
	if got := pdf.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("/paper.pdf: Content-Security-Policy = %q, want none so Chrome's viewer renders it", got)
	}
	if got := pdf.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("/paper.pdf: X-Content-Type-Options = %q, want %q", got, "nosniff")
	}

	// The upstream status and type still pass through: the player's error
	// path depends on seeing the real failure, not a relay-invented one.
	wall := serve("/wall.mp4")
	if wall.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", wall.Code, http.StatusTooManyRequests)
	}
	if ct := wall.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want the upstream's text/html", ct)
	}
}
