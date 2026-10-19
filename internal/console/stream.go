package console

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// validateSearch normalises and checks the query, region and kind shared by
// the search routes. The kind is the object type searched for: video, the
// default, or audio; the other types wait for their Phase 3 slices. It
// returns a user-facing message when the request is bad.
func validateSearch(query, region, kind string) (string, string, string, string) {
	query = strings.TrimSpace(query)
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "ALL"
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = lens.TypeVideo
	}
	if kind != lens.TypeVideo && kind != lens.TypeAudio {
		return "", "", "", "Choose movies or audio."
	}
	if query == "" || len([]rune(query)) > 500 {
		if kind == lens.TypeAudio {
			return "", "", "", "Enter a recording's title, with its performer if known, using at most 500 characters."
		}
		return "", "", "", "Enter a movie title or URL using at most 500 characters."
	}
	if region != "ALL" && (len(region) != 2 || region[0] < 'A' || region[0] > 'Z' || region[1] < 'A' || region[1] > 'Z') {
		return "", "", "", "Choose Worldwide or a two-letter country code."
	}
	return query, region, kind, ""
}

// knownEvent is the first event of a streamed search: what Atlas already
// holds for the object, before any fresh discovery.
type knownEvent struct {
	Configured bool                  `json:"configured"`
	Query      lens.ObjectQuery      `json:"query"`
	Locations  []atlas.KnownLocation `json:"locations"`
	Error      string                `json:"error,omitempty"`
}

// reportEvent is the fresh discovery report plus what was written to Atlas.
type reportEvent struct {
	scout.SearchReport
	Atlas      *atlas.RecordSummary `json:"atlas,omitempty"`
	AtlasError string               `json:"atlasError,omitempty"`
}

// mergedLocation is one entry of the ranked hundred after known and fresh
// locations are combined.
type mergedLocation struct {
	URL   string `json:"url"`
	Host  string `json:"host"`
	Title string `json:"title"`
	// Creator is the performer a recording's location is credited to.
	Creator    string     `json:"creator,omitempty"`
	Level      int        `json:"level"`
	LevelName  string     `json:"levelName"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Egress     string     `json:"egress,omitempty"`
	AccessType string     `json:"accessType"`
	Origin     string     `json:"origin"`
	Relevance  float64    `json:"relevance"`
}

type mergedEvent struct {
	Query     lens.ObjectQuery `json:"query"`
	Locations []mergedLocation `json:"locations"`
	Known     int              `json:"known"`
	Fresh     int              `json:"fresh"`
}

// searchStream answers GET /api/scout/search/stream with server-sent events:
// known (from Atlas, immediately), report (the fresh discovery burst),
// merged (the ranked hundred across both) and done.
func (s *Server) searchStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use GET to stream a search."})
		return
	}
	query, region, kind, errMsg := validateSearch(r.URL.Query().Get("query"), r.URL.Query().Get("region"), r.URL.Query().Get("kind"))
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
		return
	}
	fullLengthOnly := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("fullLengthOnly")), "false")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	started := time.Now()
	q := lens.ParseAs(query, kind)
	known := knownEvent{Configured: s.atlas != nil, Query: q, Locations: []atlas.KnownLocation{}}
	if s.atlas != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		locations, err := s.atlas.Lookup(ctx, q, lens.DefaultLimit)
		cancel()
		if err != nil {
			log.Printf("[console] atlas lookup: %v", err)
			known.Error = "The index could not be read; fresh discovery continues."
		} else if locations != nil {
			known.Locations = locations
		}
	}
	if !send("known", known) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 44*time.Second)
	defer cancel()
	report := s.search(ctx, query, region, scout.SearchOptions{FullLengthOnly: fullLengthOnly, Kind: kind})
	if s.metrics != nil {
		s.metrics.ObserveSearch(report, time.Since(started))
	}
	event := reportEvent{SearchReport: report}
	event.Atlas, event.AtlasError = s.record(r.Context(), q, report)
	if !send("report", event) {
		return
	}
	if !send("merged", mergeLocations(q, known.Locations, report)) {
		return
	}
	send("done", map[string]any{"durationMs": time.Since(started).Milliseconds()})
}

// record writes a report to Atlas under its own deadline, detached from the
// request so a closed connection does not lose evidence. Without a database
// it does nothing.
func (s *Server) record(ctx context.Context, q lens.ObjectQuery, report scout.SearchReport) (*atlas.RecordSummary, string) {
	if s.atlas == nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	summary, err := s.atlas.RecordObservations(ctx, q, observationsFromReport(report))
	if err != nil {
		log.Printf("[console] atlas record: %v", err)
		return nil, "Results could not be saved to the index."
	}
	return &summary, ""
}

// mergeLocations ranks known and fresh locations together under the cap.
func mergeLocations(q lens.ObjectQuery, known []atlas.KnownLocation, report scout.SearchReport) mergedEvent {
	var candidates []lens.Candidate
	knownURLs := make(map[string]bool, len(known))
	for _, k := range known {
		knownURLs[k.Location.URL] = true
		m := mergedLocation{
			URL:        k.Location.URL,
			Host:       k.Host.Hostname,
			Title:      firstNonEmpty(k.Location.Title, k.Object.Title),
			Creator:    k.Object.Creator,
			Level:      k.EffectiveLevel,
			LevelName:  k.LevelName,
			AccessType: k.Location.AccessType,
			Origin:     "index",
			Relevance:  k.Relevance,
		}
		observed := k.Location.LastSeen
		if k.Evidence != nil {
			observed = k.Evidence.ObservedAt
			m.Egress = k.Evidence.Egress
			at := k.Evidence.ObservedAt
			m.ObservedAt = &at
		}
		candidates = append(candidates, lens.Candidate{URL: m.URL, Host: m.Host, Level: m.Level, ObservedAt: observed, Relevance: m.Relevance, Payload: m})
	}

	sources := report.Sources
	if len(sources) == 0 {
		sources = report.VerifiedSources
	}
	freshURLs := make(map[string]bool, len(sources))
	for _, source := range sources {
		host := lens.Hostname(source.URL)
		if host == "" {
			continue
		}
		freshURLs[source.URL] = true
		var availability, playback, egress string
		var confirmed bool
		var observed time.Time
		if check := source.AvailabilityCheck; check != nil {
			availability = check.Status
			egress = check.Country
			if t, ok := lens.ObservedTime(check.CheckedAt); ok {
				observed = t
			}
		}
		if check := source.PlaybackCheck; check != nil {
			playback = check.Status
			confirmed = check.PlaybackConfirmed
			egress = firstNonEmpty(check.Country, egress)
			if t, ok := lens.ObservedTime(check.CheckedAt); ok {
				observed = t
			}
		}
		level := lens.LevelFromChecks(availability, playback, confirmed)
		// A listed title is read as a listing, so "Sintel (2010)" is Sintel.
		listed, _ := lens.ListedTitle(source.Title, source.ReleaseYear)
		relevance := lens.TitleRelevance(q.TitleKey, lens.NormalizeTitle(listed))
		if q.Type == lens.TypeAudio {
			// A recording is matched by its tags, performer included.
			relevance = lens.AudioRelevance(q, lens.AudioListing(source.Title, source.Creator, source.Duration, source.ReleaseYear))
		}
		if q.URL != "" {
			relevance = 1
		}
		m := mergedLocation{
			URL:        source.URL,
			Host:       host,
			Title:      source.Title,
			Creator:    source.Creator,
			Level:      level,
			LevelName:  lens.LevelName(level),
			Egress:     egress,
			AccessType: firstNonEmpty(strings.ToLower(source.AccessType), "unknown"),
			Origin:     "fresh",
			Relevance:  relevance,
		}
		if !observed.IsZero() {
			at := observed
			m.ObservedAt = &at
		}
		candidates = append(candidates, lens.Candidate{URL: m.URL, Host: host, Level: level, ObservedAt: observed, Relevance: relevance, Payload: m})
	}

	ranked := lens.Rank(candidates, lens.DefaultLimit, lens.DefaultMaxPerHost)
	locations := make([]mergedLocation, 0, len(ranked))
	for _, c := range ranked {
		m := c.Payload.(mergedLocation)
		if knownURLs[m.URL] && freshURLs[m.URL] {
			m.Origin = "both"
		}
		locations = append(locations, m)
	}
	return mergedEvent{Query: q, Locations: locations, Known: len(known), Fresh: len(sources)}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
