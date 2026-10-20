package atlas

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/itemba-net/core/internal/lens"
)

// Object is a canonical thing people search for.
type Object struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	TitleKey string `json:"titleKey"`
	// Creator is the performer of a recording, part of an audio object's
	// identity; it is empty for every other type so far.
	Creator    string `json:"creator,omitempty"`
	CreatorKey string `json:"creatorKey,omitempty"`
	Year       int    `json:"year,omitempty"`
	Language   string `json:"language,omitempty"`
	// ContentKey tells apart objects whose tags agree but whose content
	// does not, a radio edit beside the album cut: empty when the content
	// is unknown or agrees, a digest of the fingerprint that set it apart
	// otherwise.
	ContentKey string `json:"contentKey,omitempty"`
}

// Host is a site that holds locations.
type Host struct {
	ID       int64  `json:"id"`
	Hostname string `json:"hostname"`
	Provider string `json:"provider,omitempty"`
}

// Location is a URL on a host that holds, or claims to hold, an object.
type Location struct {
	ID         int64  `json:"id"`
	ObjectID   int64  `json:"objectId"`
	HostID     int64  `json:"hostId"`
	URL        string `json:"url"`
	MediaURL   string `json:"mediaUrl,omitempty"`
	AccessType string `json:"accessType"`
	Price      string `json:"price,omitempty"`
	Market     string `json:"market,omitempty"`
	Title      string `json:"title,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	// DurationSeconds is the length the host listed, part of a recording's
	// identity; zero when the host gave none.
	DurationSeconds int       `json:"durationSeconds,omitempty"`
	LastSeen        time.Time `json:"lastSeen"`
}

// Evidence is one probe result for one location.
type Evidence struct {
	LocationID int64          `json:"-"`
	Level      int            `json:"level"`
	Method     string         `json:"method"`
	Status     string         `json:"status"`
	Detail     string         `json:"detail,omitempty"`
	Egress     string         `json:"egress,omitempty"`
	ObservedAt time.Time      `json:"observedAt"`
	Artefacts  map[string]any `json:"artefacts,omitempty"`
}

// KnownLocation is what a lookup returns: the location, its host and object,
// the latest evidence, and the level that evidence still earns at its age.
type KnownLocation struct {
	Object         Object    `json:"object"`
	Host           Host      `json:"host"`
	Location       Location  `json:"location"`
	Evidence       *Evidence `json:"evidence,omitempty"`
	EffectiveLevel int       `json:"effectiveLevel"`
	LevelName      string    `json:"levelName"`
	AgeSeconds     int64     `json:"ageSeconds"`
	Relevance      float64   `json:"relevance"`
	// Identity is what the fingerprint library concluded when the
	// location's latest fingerprint arrived; absent when none was taken.
	Identity *lens.IdentityMatch `json:"identity,omitempty"`
}

var errNoDatabase = errors.New("atlas: no database configured")

// UpsertObject creates or refreshes the object identified by type, title key,
// creator key and year, and returns it with its ID. The conflict target is
// the identity constraint by name, so the statement is the same whichever
// columns the constraint spans.
func (s *Store) UpsertObject(ctx context.Context, o Object) (Object, error) {
	if s == nil || s.pool == nil {
		return Object{}, errNoDatabase
	}
	if o.Type == "" {
		o.Type = lens.TypeVideo
	}
	if o.TitleKey == "" {
		o.TitleKey = lens.NormalizeTitle(o.Title)
	}
	if o.TitleKey == "" {
		return Object{}, errors.New("atlas: an object needs a title")
	}
	if o.CreatorKey == "" {
		o.CreatorKey = lens.NormalizeTitle(o.Creator)
	}
	var out Object
	err := s.pool.QueryRow(ctx, `
		INSERT INTO atlas.objects (type, title, title_key, creator, creator_key, year, language, content_key)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, 0), NULLIF($7, ''), $8)
		ON CONFLICT ON CONSTRAINT objects_identity DO UPDATE
		SET title = EXCLUDED.title,
		    creator = COALESCE(EXCLUDED.creator, atlas.objects.creator),
		    language = COALESCE(EXCLUDED.language, atlas.objects.language),
		    updated_at = now()
		RETURNING id, type, title, title_key, COALESCE(creator, ''), creator_key, COALESCE(year, 0), COALESCE(language, ''), content_key`,
		o.Type, o.Title, o.TitleKey, o.Creator, o.CreatorKey, o.Year, o.Language, o.ContentKey,
	).Scan(&out.ID, &out.Type, &out.Title, &out.TitleKey, &out.Creator, &out.CreatorKey, &out.Year, &out.Language, &out.ContentKey)
	if err != nil {
		return Object{}, fmt.Errorf("atlas: upsert object: %w", err)
	}
	return out, nil
}

// AddAlias records another name for an object.
func (s *Store) AddAlias(ctx context.Context, objectID int64, alias string) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	key := lens.NormalizeTitle(alias)
	if key == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO atlas.aliases (object_id, alias, alias_key) VALUES ($1, $2, $3)
		ON CONFLICT (object_id, alias_key) DO NOTHING`, objectID, alias, key)
	if err != nil {
		return fmt.Errorf("atlas: add alias: %w", err)
	}
	return nil
}

// UpsertHost creates or touches a host by hostname.
func (s *Store) UpsertHost(ctx context.Context, hostname, provider string) (Host, error) {
	if s == nil || s.pool == nil {
		return Host{}, errNoDatabase
	}
	if hostname == "" {
		return Host{}, errors.New("atlas: a host needs a hostname")
	}
	var out Host
	err := s.pool.QueryRow(ctx, `
		INSERT INTO atlas.hosts (hostname, provider) VALUES ($1, NULLIF($2, ''))
		ON CONFLICT (hostname) DO UPDATE
		SET provider = COALESCE(EXCLUDED.provider, atlas.hosts.provider), last_seen = now()
		RETURNING id, hostname, COALESCE(provider, '')`, hostname, provider,
	).Scan(&out.ID, &out.Hostname, &out.Provider)
	if err != nil {
		return Host{}, fmt.Errorf("atlas: upsert host: %w", err)
	}
	return out, nil
}

// UpsertLocation creates or refreshes a location by URL. Empty fields never
// erase known values; an access type of unknown never replaces a known one.
func (s *Store) UpsertLocation(ctx context.Context, l Location) (Location, error) {
	if s == nil || s.pool == nil {
		return Location{}, errNoDatabase
	}
	if l.URL == "" || l.ObjectID == 0 || l.HostID == 0 {
		return Location{}, errors.New("atlas: a location needs a URL, an object and a host")
	}
	if l.AccessType == "" {
		l.AccessType = "unknown"
	}
	var out Location
	err := s.pool.QueryRow(ctx, `
		INSERT INTO atlas.locations (object_id, host_id, url, media_url, access_type, price, market, title, source_type, duration_seconds)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, 0))
		ON CONFLICT (url) DO UPDATE
		SET object_id = EXCLUDED.object_id,
		    host_id = EXCLUDED.host_id,
		    media_url = COALESCE(EXCLUDED.media_url, atlas.locations.media_url),
		    access_type = CASE WHEN EXCLUDED.access_type <> 'unknown' THEN EXCLUDED.access_type ELSE atlas.locations.access_type END,
		    price = COALESCE(EXCLUDED.price, atlas.locations.price),
		    market = COALESCE(EXCLUDED.market, atlas.locations.market),
		    title = COALESCE(EXCLUDED.title, atlas.locations.title),
		    source_type = COALESCE(EXCLUDED.source_type, atlas.locations.source_type),
		    duration_seconds = COALESCE(EXCLUDED.duration_seconds, atlas.locations.duration_seconds),
		    last_seen = now()
		RETURNING id, object_id, host_id, url, COALESCE(media_url, ''), access_type, COALESCE(price, ''), COALESCE(market, ''), COALESCE(title, ''), COALESCE(source_type, ''), COALESCE(duration_seconds, 0), last_seen`,
		l.ObjectID, l.HostID, l.URL, l.MediaURL, l.AccessType, l.Price, l.Market, l.Title, l.SourceType, l.DurationSeconds,
	).Scan(&out.ID, &out.ObjectID, &out.HostID, &out.URL, &out.MediaURL, &out.AccessType, &out.Price, &out.Market, &out.Title, &out.SourceType, &out.DurationSeconds, &out.LastSeen)
	if err != nil {
		return Location{}, fmt.Errorf("atlas: upsert location: %w", err)
	}
	return out, nil
}

// RecordEvidence appends one probe result to a location's history.
func (s *Store) RecordEvidence(ctx context.Context, e Evidence) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	if e.LocationID == 0 || e.Method == "" || e.Status == "" {
		return errors.New("atlas: evidence needs a location, a method and a status")
	}
	if e.Level < lens.LevelListed || e.Level > lens.LevelClean {
		return fmt.Errorf("atlas: level %d is not on the ladder", e.Level)
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now()
	}
	if e.Artefacts == nil {
		e.Artefacts = map[string]any{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO atlas.evidence (location_id, level, method, status, detail, egress, observed_at, artefacts)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8)`,
		e.LocationID, e.Level, e.Method, e.Status, e.Detail, e.Egress, e.ObservedAt, e.Artefacts)
	if err != nil {
		return fmt.Errorf("atlas: record evidence: %w", err)
	}
	return nil
}

// Lookup returns the locations Atlas already knows for a query, ranked under
// the cap by effective level, relevance and freshness with host diversity.
// A URL query returns that exact location if known. A nil Store returns
// nothing and no error.
func (s *Store) Lookup(ctx context.Context, q lens.ObjectQuery, limit int) ([]KnownLocation, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = lens.DefaultLimit
	}
	const columns = `
		o.id, o.type, o.title, o.title_key, COALESCE(o.creator, ''), o.creator_key, COALESCE(o.year, 0), COALESCE(o.language, ''), o.content_key,
		l.id, l.object_id, l.host_id, l.url, COALESCE(l.media_url, ''), l.access_type, COALESCE(l.price, ''), COALESCE(l.market, ''), COALESCE(l.title, ''), COALESCE(l.source_type, ''), COALESCE(l.duration_seconds, 0), l.last_seen,
		h.id, h.hostname, COALESCE(h.provider, ''),
		e.level, e.method, e.status, e.detail, e.egress, e.observed_at,
		fp.type, fp.seconds, fp.distance, fp.verdict, fp.effect, p.url, p.title`
	// The latest evidence wins; two rows observed in the same instant resolve
	// to the stronger one, then the later insert. The latest fingerprint
	// brings the verdict reached when it arrived and the location it was
	// nearest to.
	const latest = `
		LEFT JOIN LATERAL (
			SELECT level, method, status, detail, egress, observed_at
			FROM atlas.evidence ev WHERE ev.location_id = l.id
			ORDER BY observed_at DESC, level DESC, id DESC LIMIT 1
		) e ON true
		LEFT JOIN LATERAL (
			SELECT f.type, f.seconds, f.distance, f.verdict, f.effect, f.peer_location_id
			FROM atlas.fingerprints f WHERE f.location_id = l.id
			ORDER BY f.observed_at DESC LIMIT 1
		) fp ON true
		LEFT JOIN atlas.locations p ON p.id = fp.peer_location_id`

	var rows pgx.Rows
	var err error
	switch {
	case q.URL != "":
		rows, err = s.pool.Query(ctx, `
			SELECT `+columns+`
			FROM atlas.locations l
			JOIN atlas.objects o ON o.id = l.object_id
			JOIN atlas.hosts h ON h.id = l.host_id`+latest+`
			WHERE l.url = $1
			LIMIT $2`, q.URL, limit)
	case q.TitleKey != "":
		// A performer in the query keeps recordings by other performers out;
		// an object whose performer is unknown still matches, and so does
		// one whose performer names the asked one or is named by it, as
		// Lens's CreatorsAgree reads "Van Gogh" against "Vincent van Gogh".
		rows, err = s.pool.Query(ctx, `
			WITH matched AS (
				SELECT DISTINCT o.id FROM atlas.objects o
				LEFT JOIN atlas.aliases a ON a.object_id = o.id
				WHERE o.type = $1
				  AND ($3 = 0 OR o.year IS NULL OR o.year = $3)
				  AND ($7 = '' OR o.creator_key = '' OR o.creator_key = $7
				       OR position($7 in o.creator_key) > 0 OR position(o.creator_key in $7) > 0)
				  AND (o.title_key = $2 OR a.alias_key = $2 OR a.alias_key = $6
				       OR o.search @@ plainto_tsquery('simple', $4) OR a.search @@ plainto_tsquery('simple', $4))
			)
			SELECT `+columns+`
			FROM matched m
			JOIN atlas.objects o ON o.id = m.id
			JOIN atlas.locations l ON l.object_id = o.id
			JOIN atlas.hosts h ON h.id = l.host_id`+latest+`
			ORDER BY e.level DESC NULLS LAST, e.observed_at DESC NULLS LAST
			LIMIT $5`, q.Type, q.TitleKey, q.Year, q.Title, limit*3, q.RawKey, q.CreatorKey)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("atlas: lookup: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	var candidates []lens.Candidate
	for rows.Next() {
		var k KnownLocation
		var level *int16
		var method, status, detail, egress *string
		var observed *time.Time
		var fpType, fpVerdict, fpEffect, peerURL, peerTitle *string
		var fpSeconds, fpDistance *float32
		if err := rows.Scan(
			&k.Object.ID, &k.Object.Type, &k.Object.Title, &k.Object.TitleKey, &k.Object.Creator, &k.Object.CreatorKey, &k.Object.Year, &k.Object.Language, &k.Object.ContentKey,
			&k.Location.ID, &k.Location.ObjectID, &k.Location.HostID, &k.Location.URL, &k.Location.MediaURL, &k.Location.AccessType, &k.Location.Price, &k.Location.Market, &k.Location.Title, &k.Location.SourceType, &k.Location.DurationSeconds, &k.Location.LastSeen,
			&k.Host.ID, &k.Host.Hostname, &k.Host.Provider,
			&level, &method, &status, &detail, &egress, &observed,
			&fpType, &fpSeconds, &fpDistance, &fpVerdict, &fpEffect, &peerURL, &peerTitle,
		); err != nil {
			return nil, fmt.Errorf("atlas: lookup scan: %w", err)
		}
		if fpType != nil {
			identity := lens.IdentityMatch{Method: lens.MethodFingerprint, Verdict: deref(fpVerdict), Effect: deref(fpEffect), PeerURL: deref(peerURL), PeerTitle: deref(peerTitle)}
			if fpDistance != nil {
				identity.Distance = math.Round(float64(*fpDistance)*1000) / 1000
			}
			if fpSeconds != nil && identity.PeerURL != "" {
				// The span compared is at most the span covered; the exact
				// overlap was not kept, so the covered span stands for it.
				identity.Seconds = math.Round(float64(*fpSeconds)*10) / 10
			}
			identity.Detail = lens.IdentityDetail(identity)
			k.Identity = &identity
		}
		if level != nil && observed != nil {
			k.Evidence = &Evidence{LocationID: k.Location.ID, Level: int(*level), Method: deref(method), Status: deref(status), Detail: deref(detail), Egress: deref(egress), ObservedAt: *observed}
			age := now.Sub(*observed)
			if age < 0 {
				age = 0
			}
			k.AgeSeconds = int64(age / time.Second)
			k.EffectiveLevel = lens.EffectiveLevel(k.Evidence.Level, k.Object.Type, age)
		}
		k.LevelName = lens.LevelName(k.EffectiveLevel)
		k.Relevance = lens.TitleRelevance(q.TitleKey, k.Object.TitleKey)
		if q.Type == lens.TypeAudio {
			k.Relevance = lens.AudioRelevance(q, lens.AudioIdentity{TitleKey: k.Object.TitleKey, CreatorKey: k.Object.CreatorKey, Year: k.Object.Year})
		}
		if q.URL != "" {
			k.Relevance = 1
		}
		observedAt := k.Location.LastSeen
		if k.Evidence != nil {
			observedAt = k.Evidence.ObservedAt
		}
		candidates = append(candidates, lens.Candidate{URL: k.Location.URL, Host: k.Host.Hostname, Level: k.EffectiveLevel, ObservedAt: observedAt, Relevance: k.Relevance, Payload: k})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("atlas: lookup rows: %w", err)
	}
	ranked := lens.Rank(candidates, limit, lens.DefaultMaxPerHost)
	known := make([]KnownLocation, 0, len(ranked))
	for _, c := range ranked {
		known = append(known, c.Payload.(KnownLocation))
	}
	return known, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
