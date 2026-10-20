package atlas

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// RecordSummary counts what one recording wrote to Atlas, and says what
// the fingerprint library concluded about each fingerprinted listing, by
// its URL.
type RecordSummary struct {
	Objects      int                           `json:"objects"`
	Locations    int                           `json:"locations"`
	Evidence     int                           `json:"evidence"`
	Skipped      int                           `json:"skipped,omitempty"`
	Fingerprints int                           `json:"fingerprints,omitempty"`
	Identities   map[string]lens.IdentityMatch `json:"identities,omitempty"`
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
		o, err := s.UpsertObject(ctx, Object{Type: q.Type, Title: q.Title, TitleKey: q.TitleKey, Creator: q.Creator, CreatorKey: q.CreatorKey, Year: q.Year})
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
		candidate, err := tagCandidate(q, queryObject, observation.Listing)
		if err != nil {
			return summary, err
		}
		// A fingerprint lets the sound, or the sight, overrule the tags;
		// without one the tags decide.
		var object Object
		var match *lens.IdentityMatch
		var peer int64
		heard := (candidate.Type == lens.TypeAudio || candidate.Type == lens.TypeImage || candidate.Type == lens.TypeDocument) && observation.Fingerprint != nil && observation.Fingerprint.Fingerprint.Valid()
		if heard {
			object, match, peer, err = s.objectBySound(ctx, observation.Listing, candidate, objects, &summary)
		} else {
			object, err = s.resolveTagObject(ctx, candidate, objects, &summary)
		}
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
			ObjectID:        object.ID,
			HostID:          host.ID,
			URL:             observation.URL,
			MediaURL:        observation.MediaURL,
			AccessType:      accessType(observation.AccessType),
			Price:           observation.Price,
			Market:          observation.Market,
			Title:           observation.Title,
			SourceType:      observation.SourceType,
			DurationSeconds: int(lens.DurationSeconds(observation.Duration)),
		})
		if err != nil {
			return summary, err
		}
		summary.Locations++
		if heard {
			if err := s.RecordFingerprint(ctx, location.ID, *observation.Fingerprint, match, peer); err != nil {
				return summary, err
			}
			summary.Fingerprints++
			if summary.Identities == nil {
				summary.Identities = map[string]lens.IdentityMatch{}
			}
			summary.Identities[observation.URL] = *match
		}
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

// objectForTitle decides which object a listing belongs to by its tags:
// the candidate its tags name, stored.
func (s *Store) objectForTitle(ctx context.Context, q lens.ObjectQuery, queryObject *Object, listing Listing, cache map[string]Object, summary *RecordSummary) (Object, error) {
	candidate, err := tagCandidate(q, queryObject, listing)
	if err != nil {
		return Object{}, err
	}
	return s.resolveTagObject(ctx, candidate, cache, summary)
}

// tagCandidate reads which object a listing's tags name, without storing
// it. A title is read as a listing, so "Sintel (2010)" is the title Sintel
// with a year and joins the Sintel object rather than becoming a second
// one, while a title that merely ends in "film" keeps that word. An audio
// listing is read by its tags: it joins the query's recording when the
// title matches and its performer is unknown or agrees with the one the
// query names, and otherwise becomes a recording of its own under its
// performer. So a search that names no performer gathers only
// performer-less listings under its own object, and every credited
// recording of that title stays apart. A listing without a kind takes the
// query's. The query's own object comes back with its ID; any other
// candidate has none until resolveTagObject stores it.
func tagCandidate(q lens.ObjectQuery, queryObject *Object, listing Listing) (Object, error) {
	kind := listing.Kind
	if kind == "" {
		kind = q.Type
	}
	if kind == "" {
		kind = lens.TypeVideo
	}
	candidate := Object{Type: kind}
	if kind == lens.TypeAudio {
		id := lens.AudioListing(listing.Title, listing.Creator, listing.Duration, listing.Year)
		candidate.Title, candidate.TitleKey, candidate.Year = id.Title, id.TitleKey, id.Year
		candidate.Creator, candidate.CreatorKey = id.Creator, id.CreatorKey
	} else if kind == lens.TypeImage {
		id := lens.ImageListing(listing.Title, listing.Creator, listing.Year, 0, 0)
		candidate.Title, candidate.TitleKey, candidate.Year = id.Title, id.TitleKey, id.Year
		candidate.Creator, candidate.CreatorKey = id.Creator, id.CreatorKey
	} else if kind == lens.TypeDocument {
		id := lens.DocumentListing(listing.Title, listing.Creator, listing.Year, listing.Pages)
		candidate.Title, candidate.TitleKey, candidate.Year = id.Title, id.TitleKey, id.Year
		candidate.Creator, candidate.CreatorKey = id.Creator, id.CreatorKey
	} else {
		base, year := lens.ListedTitle(listing.Title, listing.Year)
		candidate.Title, candidate.TitleKey, candidate.Year = base, lens.NormalizeTitle(base), year
	}
	if queryObject != nil && queryObject.Type == kind {
		sameTitle := candidate.TitleKey == q.TitleKey
		performersAgree := candidate.CreatorKey == "" || lens.CreatorsAgree(candidate.CreatorKey, q.CreatorKey)
		if candidate.TitleKey == "" || sameTitle && performersAgree {
			return *queryObject, nil
		}
	}
	if candidate.TitleKey == "" {
		return Object{}, fmt.Errorf("atlas: a listing titled %q names no object and the query names none", listing.Title)
	}
	return candidate, nil
}

// resolveTagObject stores a candidate the tags named, once per recording
// run, and returns it with its ID; the query's own object is returned as
// it is.
func (s *Store) resolveTagObject(ctx context.Context, candidate Object, cache map[string]Object, summary *RecordSummary) (Object, error) {
	if candidate.ID != 0 {
		return candidate, nil
	}
	cacheKey := fmt.Sprintf("%s|%s|%s|%d|%s", candidate.Type, candidate.TitleKey, candidate.CreatorKey, candidate.Year, candidate.ContentKey)
	if o, ok := cache[cacheKey]; ok {
		return o, nil
	}
	o, err := s.UpsertObject(ctx, candidate)
	if err != nil {
		return Object{}, err
	}
	cache[cacheKey] = o
	summary.Objects++
	return o, nil
}

// sameIdentity reports whether two objects have the same identity, stored
// or not: the columns of the identity constraint.
func sameIdentity(a, b Object) bool {
	return a.Type == b.Type && a.TitleKey == b.TitleKey && a.CreatorKey == b.CreatorKey && a.Year == b.Year && a.ContentKey == b.ContentKey
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
