package atlas

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// RecordSummary counts what one search wrote to Atlas.
type RecordSummary struct {
	Objects   int `json:"objects"`
	Locations int `json:"locations"`
	Evidence  int `json:"evidence"`
	Skipped   int `json:"skipped,omitempty"`
}

// RecordSearch writes every source of a search report back to Atlas as a
// location with evidence. Sources whose title matches the query join the
// query's object; others get their own object, because a location belongs
// to what its title says it holds. Pruned sources are not locations and are
// skipped. This is the Phase 1 bridge from scout's untyped results; it goes
// away when scout's providers become typed drivers.
func (s *Store) RecordSearch(ctx context.Context, q lens.ObjectQuery, report scout.SearchReport) (RecordSummary, error) {
	var summary RecordSummary
	if s == nil || s.pool == nil {
		return summary, errNoDatabase
	}
	sources := report.Sources
	if len(sources) == 0 {
		sources = report.VerifiedSources
	}
	if len(sources) == 0 {
		return summary, nil
	}

	var queryObject *Object
	if q.TitleKey != "" {
		o, err := s.UpsertObject(ctx, Object{Type: q.Type, Title: q.Title, TitleKey: q.TitleKey, Year: q.Year})
		if err != nil {
			return summary, err
		}
		queryObject = &o
		summary.Objects++
	}
	objects := map[string]Object{}
	hosts := map[string]Host{}
	egress := ""
	if report.AvailabilityChecks != nil {
		egress = report.AvailabilityChecks.Country
	}

	for _, source := range sources {
		hostname := lens.Hostname(source.URL)
		if hostname == "" {
			summary.Skipped++
			continue
		}
		object, err := s.objectFor(ctx, q, queryObject, source, objects, &summary)
		if err != nil {
			return summary, err
		}
		host, ok := hosts[hostname]
		if !ok {
			host, err = s.UpsertHost(ctx, hostname, source.Provider)
			if err != nil {
				return summary, err
			}
			hosts[hostname] = host
		}
		location, err := s.UpsertLocation(ctx, Location{
			ObjectID:   object.ID,
			HostID:     host.ID,
			URL:        source.URL,
			MediaURL:   source.DirectStreamURL,
			AccessType: accessType(source.AccessType),
			Price:      source.Price,
			Market:     source.Region,
			Title:      source.Title,
			SourceType: source.SourceType,
		})
		if err != nil {
			return summary, err
		}
		summary.Locations++
		for _, e := range evidenceFor(location.ID, source, egress) {
			if err := s.RecordEvidence(ctx, e); err != nil {
				return summary, err
			}
			summary.Evidence++
		}
	}
	return summary, nil
}

// objectFor decides which object a source belongs to. The source title is
// parsed like a query, so "Sintel (2010)" is the title Sintel with a year and
// joins the Sintel object rather than becoming a second one.
func (s *Store) objectFor(ctx context.Context, q lens.ObjectQuery, queryObject *Object, source scout.VerifiedSource, cache map[string]Object, summary *RecordSummary) (Object, error) {
	parsed := lens.Parse(source.Title)
	key := parsed.TitleKey
	if queryObject != nil && (key == "" || key == q.TitleKey) {
		return *queryObject, nil
	}
	if key == "" {
		return Object{}, fmt.Errorf("atlas: source %s has no title and the query names no object", source.URL)
	}
	year := source.ReleaseYear
	if year == 0 {
		year = parsed.Year
	}
	cacheKey := fmt.Sprintf("%s|%d", key, year)
	if o, ok := cache[cacheKey]; ok {
		return o, nil
	}
	o, err := s.UpsertObject(ctx, Object{Type: lens.TypeVideo, Title: parsed.Title, TitleKey: key, Year: year})
	if err != nil {
		return Object{}, err
	}
	cache[cacheKey] = o
	summary.Objects++
	return o, nil
}

func accessType(raw string) string {
	switch t := strings.ToLower(strings.TrimSpace(raw)); t {
	case "free", "subscription", "rental", "purchase", "paid":
		return t
	case "rent":
		return "rental"
	case "buy":
		return "purchase"
	}
	return "unknown"
}

// evidenceFor turns a source's checks into ladder evidence. A source with no
// checks is Listed by its provider.
func evidenceFor(locationID int64, source scout.VerifiedSource, defaultEgress string) []Evidence {
	var out []Evidence
	if check := source.AvailabilityCheck; check != nil && check.Status != "" {
		out = append(out, Evidence{
			LocationID: locationID,
			Level:      lens.LevelFromChecks(check.Status, "", false),
			Method:     firstNonEmpty(check.Method, "availability_check"),
			Status:     check.Status,
			Detail:     check.Detail,
			Egress:     firstNonEmpty(check.Country, defaultEgress),
			ObservedAt: parseObserved(check.CheckedAt),
			Artefacts:  map[string]any{"httpStatus": check.HTTPStatus, "evidenceUrl": check.EvidenceURL, "requestedRegion": check.RequestedRegion},
		})
	}
	if check := source.PlaybackCheck; check != nil && check.Status != "" && check.Status != "skipped" {
		level := lens.LevelListed
		if check.Status == "started" && check.PlaybackConfirmed {
			level = lens.LevelRendered
		} else if source.AvailabilityCheck != nil {
			level = lens.LevelFromChecks(source.AvailabilityCheck.Status, "", false)
		}
		out = append(out, Evidence{
			LocationID: locationID,
			Level:      level,
			Method:     firstNonEmpty(check.Method, "playback_sample"),
			Status:     check.Status,
			Detail:     firstNonEmpty(check.Reason, check.Detail),
			Egress:     firstNonEmpty(check.Country, defaultEgress),
			ObservedAt: parseObserved(check.CheckedAt),
			Artefacts: map[string]any{
				"secondsAdvanced": check.SecondsAdvanced,
				"decodedFrames":   check.DecodedFrames,
				"width":           check.Width,
				"height":          check.Height,
				"durationSeconds": check.DurationSeconds,
				"evidenceUrl":     check.EvidenceURL,
				"requestedRegion": check.RequestedRegion,
			},
		})
	}
	if len(out) == 0 {
		out = append(out, Evidence{
			LocationID: locationID,
			Level:      lens.LevelListed,
			Method:     "provider_listing",
			Status:     "listed",
			Detail:     source.Provider,
			Egress:     defaultEgress,
			ObservedAt: time.Now(),
		})
	}
	return out
}

// parseObserved reads a check timestamp, falling back to now so evidence is
// never recorded without a time.
func parseObserved(value string) time.Time {
	if t, ok := lens.ObservedTime(value); ok {
		return t
	}
	return time.Now()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
