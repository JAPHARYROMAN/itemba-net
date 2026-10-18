package console

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/scout"
)

type fawesomeShelfPlaybackService interface {
	StartFawesomeShelfPlayback(context.Context, string) (scout.FawesomeShelfPlaybackReport, error)
	FawesomeShelfPlaybackStatus(string) (scout.FawesomeShelfPlaybackReport, bool)
	CancelFawesomeShelfPlayback(string) (scout.FawesomeShelfPlaybackReport, bool)
}

var fawesomeShelfJobID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func fawesomeShelfPlaybackHandler(service fawesomeShelfPlaybackService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Use POST to start shelf checks, GET for progress, or DELETE to cancel."})
			return
		}
		if r.Method != http.MethodGet && !sameShelfPlaybackOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Start or cancel playback checks from ITEMBA NET."})
			return
		}
		if r.Method == http.MethodPost {
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Send the shelf mode as JSON."})
				return
			}
			var input struct {
				Mode string `json:"mode"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose a Fawesome shelf; movie URLs cannot be submitted here."})
				return
			}
			mode := strings.ToLower(strings.TrimSpace(input.Mode))
			if mode != "popular" && mode != "recent" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose Popular or New on Fawesome."})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			report, err := service.StartFawesomeShelfPlayback(ctx, mode)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Automatic playback checks could not start. Movie listings remain available."})
				return
			}
			writeJSON(w, http.StatusOK, report)
			return
		}
		id := r.URL.Query().Get("job")
		if !fawesomeShelfJobID.MatchString(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "A valid shelf playback job is required."})
			return
		}
		var report scout.FawesomeShelfPlaybackReport
		var found bool
		if r.Method == http.MethodDelete {
			report, found = service.CancelFawesomeShelfPlayback(id)
		} else {
			report, found = service.FawesomeShelfPlaybackStatus(id)
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "This playback check round is no longer available. Reload the shelf to start a new round."})
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}

func sameShelfPlaybackOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && strings.EqualFold(u.Host, r.Host)
}
