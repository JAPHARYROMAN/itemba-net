package atlas

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// FrontierKind says what a frontier row owes: a driver search for an object,
// or a fetch of a known location.
type FrontierKind string

const (
	FrontierSearch FrontierKind = "search"
	FrontierPage   FrontierKind = "page"
)

// FrontierEntry is one URL the crawler owes a fetch.
type FrontierEntry struct {
	ID            int64        `json:"id"`
	Kind          FrontierKind `json:"kind"`
	Driver        string       `json:"driver,omitempty"`
	ObjectID      int64        `json:"objectId,omitempty"`
	LocationID    int64        `json:"locationId,omitempty"`
	Host          string       `json:"host"`
	URL           string       `json:"url"`
	Market        string       `json:"market,omitempty"`
	Priority      int          `json:"priority"`
	Tier          int          `json:"tier"`
	Attempts      int          `json:"attempts"`
	NextFetchAt   time.Time    `json:"nextFetchAt"`
	LastStatus    string       `json:"lastStatus,omitempty"`
	LastFetchedAt time.Time    `json:"lastFetchedAt,omitempty"`
}

// Enqueue adds entries to the frontier. A URL already queued keeps its row and
// takes the higher priority. It returns how many rows were new.
func (s *Store) Enqueue(ctx context.Context, entries []FrontierEntry) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errNoDatabase
	}
	added := 0
	for _, e := range entries {
		if e.URL == "" || e.Host == "" || (e.Kind != FrontierSearch && e.Kind != FrontierPage) {
			return added, fmt.Errorf("atlas: a frontier entry needs a kind, a host and a URL: %+v", e)
		}
		if e.Tier == 0 {
			e.Tier = 1
		}
		if e.NextFetchAt.IsZero() {
			e.NextFetchAt = time.Now()
		}
		var inserted bool
		err := s.pool.QueryRow(ctx, `
			INSERT INTO atlas.frontier (kind, driver, object_id, location_id, host, url, market, priority, tier, next_fetch_at)
			VALUES ($1, NULLIF($2, ''), NULLIF($3, 0), NULLIF($4, 0), $5, $6, NULLIF($7, ''), $8, $9, $10)
			ON CONFLICT (url) DO UPDATE SET priority = GREATEST(atlas.frontier.priority, EXCLUDED.priority)
			RETURNING (xmax = 0)`,
			string(e.Kind), e.Driver, e.ObjectID, e.LocationID, e.Host, e.URL, e.Market, e.Priority, e.Tier, e.NextFetchAt,
		).Scan(&inserted)
		if err != nil {
			return added, fmt.Errorf("atlas: enqueue: %w", err)
		}
		if inserted {
			added++
		}
	}
	return added, nil
}

const frontierColumns = `id, kind, COALESCE(driver, ''), COALESCE(object_id, 0), COALESCE(location_id, 0), host, url, COALESCE(market, ''),
	priority, tier, attempts, next_fetch_at, COALESCE(last_status, ''), COALESCE(last_fetched_at, 'epoch'::timestamptz)`

// LeaseDue leases up to limit due entries, highest priority first, so a
// worker can process them while other workers skip them.
func (s *Store) LeaseDue(ctx context.Context, limit int, leaseFor time.Duration) ([]FrontierEntry, error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	if limit <= 0 {
		limit = 16
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE atlas.frontier SET leased_until = $2
		WHERE id IN (
			SELECT id FROM atlas.frontier
			WHERE next_fetch_at <= now() AND (leased_until IS NULL OR leased_until < now())
			ORDER BY priority DESC, next_fetch_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+frontierColumns, limit, time.Now().Add(leaseFor))
	if err != nil {
		return nil, fmt.Errorf("atlas: lease: %w", err)
	}
	defer rows.Close()
	var entries []FrontierEntry
	for rows.Next() {
		var e FrontierEntry
		var kind string
		if err := rows.Scan(&e.ID, &kind, &e.Driver, &e.ObjectID, &e.LocationID, &e.Host, &e.URL, &e.Market, &e.Priority, &e.Tier, &e.Attempts, &e.NextFetchAt, &e.LastStatus, &e.LastFetchedAt); err != nil {
			return nil, fmt.Errorf("atlas: lease scan: %w", err)
		}
		e.Kind = FrontierKind(kind)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Complete records a fetch outcome, releases the lease and schedules the
// next fetch.
func (s *Store) Complete(ctx context.Context, id int64, status string, next time.Time) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	if len(status) > 200 {
		status = status[:200]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE atlas.frontier
		SET leased_until = NULL, attempts = attempts + 1, last_status = $2, last_fetched_at = now(), next_fetch_at = $3
		WHERE id = $1`, id, status, next)
	if err != nil {
		return fmt.Errorf("atlas: complete: %w", err)
	}
	return nil
}

// FrontierCounts summarises the frontier for the status route.
type FrontierCounts struct {
	Total  int            `json:"total"`
	Due    int            `json:"due"`
	Leased int            `json:"leased"`
	ByKind map[string]int `json:"byKind"`
}

// FrontierCounts reports totals, due and leased rows by kind.
func (s *Store) FrontierCounts(ctx context.Context) (FrontierCounts, error) {
	counts := FrontierCounts{ByKind: map[string]int{}}
	if s == nil || s.pool == nil {
		return counts, errNoDatabase
	}
	rows, err := s.pool.Query(ctx, `
		SELECT kind, count(*),
		       count(*) FILTER (WHERE next_fetch_at <= now() AND (leased_until IS NULL OR leased_until < now())),
		       count(*) FILTER (WHERE leased_until > now())
		FROM atlas.frontier GROUP BY kind`)
	if err != nil {
		return counts, fmt.Errorf("atlas: frontier counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var total, due, leased int
		if err := rows.Scan(&kind, &total, &due, &leased); err != nil {
			return counts, err
		}
		counts.ByKind[kind] = total
		counts.Total += total
		counts.Due += due
		counts.Leased += leased
	}
	return counts, rows.Err()
}

// ObjectByID returns one object.
func (s *Store) ObjectByID(ctx context.Context, id int64) (Object, error) {
	if s == nil || s.pool == nil {
		return Object{}, errNoDatabase
	}
	var o Object
	err := s.pool.QueryRow(ctx, `SELECT id, type, title, title_key, COALESCE(year, 0), COALESCE(language, '') FROM atlas.objects WHERE id = $1`, id).
		Scan(&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Year, &o.Language)
	if err != nil {
		return Object{}, fmt.Errorf("atlas: object %d: %w", id, err)
	}
	return o, nil
}

// ObjectsForCrawl returns the objects the crawler should search for, most
// recently touched first.
func (s *Store) ObjectsForCrawl(ctx context.Context, limit int) ([]Object, error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	rows, err := s.pool.Query(ctx, `SELECT id, type, title, title_key, COALESCE(year, 0), COALESCE(language, '') FROM atlas.objects ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("atlas: objects for crawl: %w", err)
	}
	defer rows.Close()
	var objects []Object
	for rows.Next() {
		var o Object
		if err := rows.Scan(&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Year, &o.Language); err != nil {
			return nil, err
		}
		objects = append(objects, o)
	}
	return objects, rows.Err()
}

// StaleLocation is a location whose latest evidence is older than the
// crawler's refresh window, with the hostname the frontier budgets by.
type StaleLocation struct {
	Location
	Hostname   string
	ObservedAt time.Time
}

// StaleLocations returns locations with no evidence or evidence observed
// before cutoff, oldest first.
func (s *Store) StaleLocations(ctx context.Context, cutoff time.Time, limit int) ([]StaleLocation, error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.object_id, l.host_id, l.url, COALESCE(l.media_url, ''), l.access_type, COALESCE(l.price, ''), COALESCE(l.market, ''), COALESCE(l.title, ''), COALESCE(l.source_type, ''), l.last_seen,
		       h.hostname, COALESCE(e.observed_at, 'epoch'::timestamptz)
		FROM atlas.locations l
		JOIN atlas.hosts h ON h.id = l.host_id
		LEFT JOIN LATERAL (SELECT observed_at FROM atlas.evidence ev WHERE ev.location_id = l.id ORDER BY observed_at DESC LIMIT 1) e ON true
		WHERE e.observed_at IS NULL OR e.observed_at < $1
		ORDER BY e.observed_at ASC NULLS FIRST
		LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("atlas: stale locations: %w", err)
	}
	defer rows.Close()
	var out []StaleLocation
	for rows.Next() {
		var l StaleLocation
		if err := rows.Scan(&l.ID, &l.ObjectID, &l.HostID, &l.URL, &l.MediaURL, &l.AccessType, &l.Price, &l.Market, &l.Title, &l.SourceType, &l.LastSeen, &l.Hostname, &l.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Listing is what a driver extracts from a host: a page that lists an object,
// with whatever the host said about access. It is deliberately independent
// of scout's types.
type Listing struct {
	// ID is the host's identity for the listing when the driver provides one.
	ID          string `json:"id,omitempty"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	MediaURL    string `json:"mediaUrl,omitempty"`
	Provider    string `json:"provider"`
	SourceType  string `json:"sourceType,omitempty"`
	AccessType  string `json:"accessType"`
	AccessNote  string `json:"accessNote,omitempty"`
	Price       string `json:"price,omitempty"`
	Market      string `json:"market,omitempty"`
	Year        int    `json:"year,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Description string `json:"description,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	// Tags are host categories such as Archive collections.
	Tags []string `json:"tags,omitempty"`
	// ListingOnly is true when the page lists the object without a known
	// media URL; verification must open it before any playback claim.
	ListingOnly bool `json:"listingOnly"`
}

// RecordListings writes a driver's listings for an object as locations with
// listed evidence. A listing whose parsed title matches the object joins it;
// any other title gets its own object. method names the driver that found it.
func (s *Store) RecordListings(ctx context.Context, object Object, listings []Listing, method string) (RecordSummary, error) {
	var summary RecordSummary
	if s == nil || s.pool == nil {
		return summary, errNoDatabase
	}
	if object.ID == 0 || strings.TrimSpace(method) == "" {
		return summary, errors.New("atlas: listings need a stored object and a method")
	}
	q := lens.ObjectQuery{Type: object.Type, Title: object.Title, TitleKey: object.TitleKey, RawKey: object.TitleKey, Year: object.Year}
	objects := map[string]Object{}
	hosts := map[string]Host{}
	now := time.Now()
	for _, listing := range listings {
		hostname := lens.Hostname(listing.URL)
		if hostname == "" || strings.TrimSpace(listing.Title) == "" {
			summary.Skipped++
			continue
		}
		target, err := s.objectForTitle(ctx, q, &object, listing.Title, listing.Year, objects, &summary)
		if err != nil {
			return summary, err
		}
		host, ok := hosts[hostname]
		if !ok {
			host, err = s.UpsertHost(ctx, hostname, listing.Provider)
			if err != nil {
				return summary, err
			}
			hosts[hostname] = host
		}
		location, err := s.UpsertLocation(ctx, Location{
			ObjectID:   target.ID,
			HostID:     host.ID,
			URL:        listing.URL,
			MediaURL:   listing.MediaURL,
			AccessType: accessType(listing.AccessType),
			Price:      listing.Price,
			Market:     listing.Market,
			Title:      listing.Title,
			SourceType: listing.SourceType,
		})
		if err != nil {
			return summary, err
		}
		summary.Locations++
		detail := listing.Provider
		if listing.Market != "" {
			detail += " (" + listing.Market + ")"
		}
		if err := s.RecordEvidence(ctx, Evidence{LocationID: location.ID, Level: lens.LevelListed, Method: method, Status: "listed", Detail: detail, ObservedAt: now}); err != nil {
			return summary, err
		}
		summary.Evidence++
	}
	return summary, nil
}
