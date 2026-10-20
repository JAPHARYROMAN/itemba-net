package atlas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// The fingerprint library. A probe that heard or saw a location's media
// hands Atlas a fingerprint with its listing; the library stores one per
// location and type, compares a new one with everything of its type it
// holds, and lets the comparison decide which object the location belongs
// to: the same sound joins the recording it was heard in, whatever the
// tags say, and a different sound under the same tags is kept apart as a
// recording of its own. The comparison runs here, not in the database,
// because it searches alignments, which no index can shift; the scan is
// bounded by fingerprintScanLimit, the most recent rows of the type.
//
// A stored fingerprint keeps the verdict reached when it arrived, against
// what the library held then. A later arrival revisits that verdict where
// it is a better answer (revisitFingerprints), so the order in which
// locations were recorded does not show in what the library says of them.

// fingerprintScanLimit is how many stored fingerprints one comparison
// reads: at a kilobyte and a half each, a few megabytes, which a personal
// index reaches only after thousands of sampled recordings.
const fingerprintScanLimit = 4000

// FingerprintObservation is a fingerprint as a producer hands it to Atlas
// with a listing: the fingerprint, the length of the file it was taken from
// as the probe read it (which judges an edit against a full cut when the
// beginnings agree) and when it was taken.
type FingerprintObservation struct {
	Fingerprint  lens.Fingerprint `json:"fingerprint"`
	MediaSeconds float64          `json:"mediaSeconds,omitempty"`
	ObservedAt   time.Time        `json:"observedAt"`
}

// FingerprintNeighbour is a stored fingerprint compared with a given one:
// whose location and object it belongs to, the length of its file, how the
// comparison went, and what the library concluded about it when it arrived
// (the location it was nearest to then, 0 when none, how far, and the
// verdict and effect), which a later arrival may revisit.
type FingerprintNeighbour struct {
	LocationID     int64
	Object         Object
	URL            string
	Title          string
	MediaSeconds   float64
	Comparison     lens.FingerprintComparison
	StoredPeer     int64
	StoredDistance float64
	StoredVerdict  string
	StoredEffect   string
}

// RecordFingerprint stores a location's fingerprint of its type, replacing
// an earlier one, together with the verdict the library reached on it and
// the location it was nearest to.
func (s *Store) RecordFingerprint(ctx context.Context, locationID int64, o FingerprintObservation, match *lens.IdentityMatch, peerLocationID int64) error {
	if s == nil || s.pool == nil {
		return errNoDatabase
	}
	if locationID == 0 || !o.Fingerprint.Valid() {
		return errors.New("atlas: a fingerprint needs a location and valid words")
	}
	observed := o.ObservedAt
	if observed.IsZero() {
		observed = time.Now()
	}
	verdict, effect := "", ""
	var distance *float64
	var peer *int64
	if match != nil {
		verdict, effect = match.Verdict, match.Effect
		if match.PeerURL != "" {
			d := match.Distance
			distance = &d
		}
		if peerLocationID != 0 {
			peer = &peerLocationID
		}
	}
	variants := []lens.FingerprintVariant{}
	if o.Fingerprint.Variants != nil {
		variants = o.Fingerprint.Variants
	}
	encoded, err := json.Marshal(variants)
	if err != nil {
		return fmt.Errorf("atlas: record fingerprint: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO atlas.fingerprints (location_id, type, length, seconds, media_seconds, data, variants, observed_at, peer_location_id, distance, verdict, effect)
		VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, 0), $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (location_id, type) DO UPDATE
		SET length = EXCLUDED.length,
		    seconds = EXCLUDED.seconds,
		    media_seconds = EXCLUDED.media_seconds,
		    data = EXCLUDED.data,
		    variants = EXCLUDED.variants,
		    observed_at = EXCLUDED.observed_at,
		    peer_location_id = EXCLUDED.peer_location_id,
		    distance = EXCLUDED.distance,
		    verdict = EXCLUDED.verdict,
		    effect = EXCLUDED.effect`,
		locationID, o.Fingerprint.Type, o.Fingerprint.Frames, o.Fingerprint.Seconds, o.MediaSeconds, o.Fingerprint.Data, encoded, observed, peer, distance, verdict, effect)
	if err != nil {
		return fmt.Errorf("atlas: record fingerprint: %w", err)
	}
	return nil
}

// NearestFingerprints compares a fingerprint with the stored fingerprints
// of its type and returns the nearest, closest first, at most limit of
// them. Fingerprints that cannot be compared (too short an overlap) are
// left out, so an empty answer means nothing comparable has been heard.
func (s *Store) NearestFingerprints(ctx context.Context, fp lens.Fingerprint, limit int) ([]FingerprintNeighbour, error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	if !fp.Valid() {
		return nil, errors.New("atlas: cannot compare an invalid fingerprint")
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.pool.Query(ctx, `
		SELECT f.location_id, l.url, COALESCE(l.title, ''), COALESCE(f.media_seconds, 0), f.length, COALESCE(f.seconds, 0), f.data, f.variants,
		       COALESCE(f.peer_location_id, 0), COALESCE(f.distance, -1), f.verdict, f.effect,
		       o.id, o.type, o.title, o.title_key, COALESCE(o.creator, ''), o.creator_key, COALESCE(o.year, 0), COALESCE(o.language, ''), o.content_key
		FROM atlas.fingerprints f
		JOIN atlas.locations l ON l.id = f.location_id
		JOIN atlas.objects o ON o.id = l.object_id
		WHERE f.type = $1
		ORDER BY f.observed_at DESC
		LIMIT $2`, fp.Type, fingerprintScanLimit)
	if err != nil {
		return nil, fmt.Errorf("atlas: nearest fingerprints: %w", err)
	}
	defer rows.Close()
	var neighbours []FingerprintNeighbour
	for rows.Next() {
		var n FingerprintNeighbour
		stored := lens.Fingerprint{Type: fp.Type}
		var mediaSeconds, seconds, storedDistance float32
		var variants []byte
		o := &n.Object
		if err := rows.Scan(&n.LocationID, &n.URL, &n.Title, &mediaSeconds, &stored.Frames, &seconds, &stored.Data, &variants,
			&n.StoredPeer, &storedDistance, &n.StoredVerdict, &n.StoredEffect,
			&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Creator, &o.CreatorKey, &o.Year, &o.Language, &o.ContentKey); err != nil {
			return nil, fmt.Errorf("atlas: nearest fingerprints scan: %w", err)
		}
		if len(variants) > 0 {
			if err := json.Unmarshal(variants, &stored.Variants); err != nil {
				return nil, fmt.Errorf("atlas: nearest fingerprints variants: %w", err)
			}
		}
		n.MediaSeconds, stored.Seconds, n.StoredDistance = float64(mediaSeconds), float64(seconds), float64(storedDistance)
		n.Comparison = lens.CompareFingerprints(fp, stored)
		if n.Comparison.Known {
			neighbours = append(neighbours, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("atlas: nearest fingerprints rows: %w", err)
	}
	sort.SliceStable(neighbours, func(i, j int) bool { return neighbours[i].Comparison.Distance < neighbours[j].Comparison.Distance })
	if len(neighbours) > limit {
		neighbours = neighbours[:limit]
	}
	return neighbours, nil
}

// sameMedia says a stored neighbour is the same recording, picture or text
// as the arriving fingerprint: the comparison says so, and the files'
// lengths agree where both are known, the stored one scaled to the speed
// the sounds matched at.
func sameMedia(o FingerprintObservation, n FingerprintNeighbour) bool {
	return n.Comparison.Same() && (o.MediaSeconds == 0 || n.MediaSeconds == 0 || lens.EditionLengthsAgree(o.MediaSeconds, n.MediaSeconds*n.Comparison.Speed))
}

// objectBySound lets a recording's fingerprint overrule its tags. The
// stored fingerprints nearest to it, its own earlier one aside, decide:
// the nearest that sounds the same, on a file whose length agrees or is
// unknown, names the object the listing joins, confirmed when that is the
// object its tags name and joined when it is another; failing that, when
// the object its tags name has been heard and sounds different (or sounds
// the same only at the start, on a file of another length), the listing is
// kept apart as an object of its own under a content key; otherwise it
// stays under its tags and the nearest fingerprint is reported for what it
// was. The tag candidate is stored only when the listing stays under it.
// The match says which; the returned location ID is the peer it was
// compared with, for the record, and the neighbours it was compared with
// come back for the revisit.
func (s *Store) objectBySound(ctx context.Context, listing Listing, candidate Object, cache map[string]Object, summary *RecordSummary) (Object, *lens.IdentityMatch, int64, []FingerprintNeighbour, error) {
	o := *listing.Fingerprint
	neighbours, err := s.NearestFingerprints(ctx, o.Fingerprint, 8)
	if err != nil {
		return Object{}, nil, 0, nil, err
	}
	var self *FingerprintNeighbour
	var others []FingerprintNeighbour
	for i := range neighbours {
		if neighbours[i].URL == listing.URL {
			n := neighbours[i]
			self = &n
			continue
		}
		others = append(others, neighbours[i])
	}
	match := &lens.IdentityMatch{Method: lens.FingerprintMethod(o.Fingerprint.Type)}
	describe := func(n FingerprintNeighbour) {
		match.PeerURL, match.PeerTitle = n.URL, n.Title
		match.Distance = math.Round(n.Comparison.Distance*1000) / 1000
		match.Seconds = n.Comparison.OverlapSeconds()
		match.Verdict = lens.VerdictDifferent
		if n.Comparison.Same() {
			match.Verdict = lens.VerdictSame
		}
	}
	// Sampled again and still itself: the variant it already is, unless
	// something nearer decides otherwise below.
	variantOfItself := self != nil && self.Comparison.Same() && self.Object.ContentKey != "" && self.Object.ID != 0
	// The nearest same sound on a file of a compatible length is the
	// recording itself; the stored file's length is scaled to the speed
	// the sounds matched at before the lengths are compared.
	for _, n := range others {
		if !sameMedia(o, n) {
			continue
		}
		describe(n)
		match.Effect = lens.EffectJoined
		if sameIdentity(n.Object, candidate) {
			match.Effect = lens.EffectConfirmed
		}
		match.Detail = lens.IdentityDetail(*match)
		return n.Object, match, n.LocationID, others, nil
	}
	// Nothing it is the same recording as. Has the object its tags name
	// been heard? Then it sounds different and the listing is kept apart.
	for _, n := range others {
		if !sameIdentity(n.Object, candidate) {
			continue
		}
		describe(n)
		match.Effect = lens.EffectKeptApart
		match.Detail = lens.IdentityDetail(*match)
		if variantOfItself {
			return self.Object, match, n.LocationID, others, nil
		}
		variant := candidate
		variant.ID, variant.ContentKey = 0, lens.ContentKey(o.Fingerprint)
		created, err := s.resolveTagObject(ctx, variant, cache, summary)
		if err != nil {
			return Object{}, nil, 0, nil, err
		}
		return created, match, n.LocationID, others, nil
	}
	// The object its tags name has not been heard; the nearest fingerprint
	// belongs to another recording, which tells nothing new about this one,
	// unless there is none at all.
	peer := int64(0)
	if len(others) > 0 {
		describe(others[0])
		peer = others[0].LocationID
	}
	match.Detail = lens.IdentityDetail(*match)
	if variantOfItself {
		return self.Object, match, peer, others, nil
	}
	object, err := s.resolveTagObject(ctx, candidate, cache, summary)
	if err != nil {
		return Object{}, nil, 0, nil, err
	}
	return object, match, peer, others, nil
}

// revisitFingerprints lets a new arrival revise what the library concluded
// about the fingerprints it was compared with, once the arrival has its
// location and object. A neighbour the arrival is the same as takes it as
// its peer with the verdict same, confirmed when the two now sit in one
// object, unless the neighbour already has a same peer nearer than this
// one; a neighbour that had nothing to compare with takes the arrival as
// its nearest, different. A nearer different fingerprint tells a neighbour
// that already has a peer nothing about its identity and is left alone.
// The revised verdicts come back by the neighbours' URLs.
func (s *Store) revisitFingerprints(ctx context.Context, o FingerprintObservation, arrival Location, object Object, neighbours []FingerprintNeighbour) (map[string]lens.IdentityMatch, error) {
	revised := map[string]lens.IdentityMatch{}
	for _, n := range neighbours {
		if n.LocationID == arrival.ID {
			continue
		}
		same := sameMedia(o, n)
		switch {
		case same && n.StoredPeer != 0 && n.StoredVerdict == lens.VerdictSame && n.StoredDistance <= n.Comparison.Distance:
			continue
		case !same && n.StoredPeer != 0:
			continue
		}
		match := lens.IdentityMatch{
			Method:    lens.FingerprintMethod(o.Fingerprint.Type),
			Verdict:   lens.VerdictDifferent,
			PeerURL:   arrival.URL,
			PeerTitle: arrival.Title,
			Distance:  math.Round(n.Comparison.Distance*1000) / 1000,
			Seconds:   n.Comparison.OverlapSeconds(),
		}
		if same {
			match.Verdict = lens.VerdictSame
			if n.Object.ID == object.ID {
				match.Effect = lens.EffectConfirmed
			}
		}
		match.Detail = lens.IdentityDetail(match)
		if _, err := s.pool.Exec(ctx, `
			UPDATE atlas.fingerprints
			SET peer_location_id = $3, distance = $4, verdict = $5, effect = $6
			WHERE location_id = $1 AND type = $2`,
			n.LocationID, o.Fingerprint.Type, arrival.ID, match.Distance, match.Verdict, match.Effect); err != nil {
			return nil, fmt.Errorf("atlas: revisit fingerprint: %w", err)
		}
		revised[n.URL] = match
	}
	return revised, nil
}
