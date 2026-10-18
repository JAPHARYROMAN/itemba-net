package atlas

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// RecordSummary counts what one recording wrote to Atlas.
type RecordSummary struct {
	Objects   int `json:"objects"`
	Locations int `json:"locations"`
	Evidence  int `json:"evidence"`
	Skipped   int `json:"skipped,omitempty"`
}

// Observation is a listing together with whatever was checked about it: the
// neutral form every producer (the search pipeline, the crawler, later the
// probes) hands to Atlas. Evidence without a location ID is attached to the
// listing's location; an observation with no evidence is recorded as listed
// by its provider.
type Observation struct {
	Listing
	Evidence []Evidence
}

// RecordObservations writes observations for a query. A listing whose title
// reads as the query's object joins it; any other title gets its own object,
// because a location belongs to what it says it holds.
func (s *Store) RecordObservations(ctx context.Context, q lens.ObjectQuery, observations []Observation) (RecordSummary, error) {
	var summary RecordSummary
	if s == nil || s.pool == nil {
		return summary, errNoDatabase
	}
	if len(observations) == 0 {
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
	for _, observation := range observations {
		hostname := lens.Hostname(observation.URL)
		if hostname == "" {
			summary.Skipped++
			continue
		}
		object, err := s.objectForTitle(ctx, q, queryObject, observation.Title, observation.Year, objects, &summary)
		if err != nil {
			return summary, err
		}
		host, ok := hosts[hostname]
		if !ok {
			host, err = s.UpsertHost(ctx, hostname, observation.Provider)
			if err != nil {
				return summary, err
			}
			hosts[hostname] = host
		}
		location, err := s.UpsertLocation(ctx, Location{
			ObjectID:   object.ID,
			HostID:     host.ID,
			URL:        observation.URL,
			MediaURL:   observation.MediaURL,
			AccessType: accessType(observation.AccessType),
			Price:      observation.Price,
			Market:     observation.Market,
			Title:      observation.Title,
			SourceType: observation.SourceType,
		})
		if err != nil {
			return summary, err
		}
		summary.Locations++
		evidence := observation.Evidence
		if len(evidence) == 0 {
			evidence = []Evidence{{Level: lens.LevelListed, Method: "provider_listing", Status: "listed", Detail: observation.Provider, ObservedAt: time.Now()}}
		}
		for _, e := range evidence {
			e.LocationID = location.ID
			if err := s.RecordEvidence(ctx, e); err != nil {
				return summary, err
			}
			summary.Evidence++
		}
	}
	return summary, nil
}

// objectForTitle decides which object a listed title belongs to. The title
// is read as a listing, so "Sintel (2010)" is the title Sintel with a year
// and joins the Sintel object rather than becoming a second one, while a
// title that merely ends in "film" keeps that word.
func (s *Store) objectForTitle(ctx context.Context, q lens.ObjectQuery, queryObject *Object, title string, releaseYear int, cache map[string]Object, summary *RecordSummary) (Object, error) {
	base, year := lens.ListedTitle(title, releaseYear)
	key := lens.NormalizeTitle(base)
	if queryObject != nil && (key == "" || key == q.TitleKey) {
		return *queryObject, nil
	}
	if key == "" {
		return Object{}, fmt.Errorf("atlas: a listing titled %q names no object and the query names none", title)
	}
	cacheKey := fmt.Sprintf("%s|%d", key, year)
	if o, ok := cache[cacheKey]; ok {
		return o, nil
	}
	o, err := s.UpsertObject(ctx, Object{Type: lens.TypeVideo, Title: base, TitleKey: key, Year: year})
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
