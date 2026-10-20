package atlas

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/itemba-net/core/internal/lens"
)

// Lens version two in Atlas: resolving a query to objects. Resolve ranks
// objects, not locations, by how well they answer the query: an exact
// title or alias key first, then the text search, then the nearest title
// vectors of objects and aliases, with the creator and the year the query
// names adjusting the score as the kinds' own relevance rules do. A query
// that names no kind is resolved across every kind. Lookup falls back to
// it when its own matching finds nothing, so a misspelt title, a title in
// another language that Atlas holds as an alias, or a description that
// shares its grams still reaches the locations.

// ObjectMatch is one object a query resolved to, with the score it earned
// and how it was found: title, alias, search, vector or alias_vector.
type ObjectMatch struct {
	Object Object  `json:"object"`
	Score  float64 `json:"score"`
	Method string  `json:"method"`
	Alias  string  `json:"alias,omitempty"`
}

// resolveVectorCandidates bounds the nearest-neighbour scans.
const resolveVectorCandidates = 24

// Resolve ranks the objects a query names, at most limit of them.
func (s *Store) Resolve(ctx context.Context, q lens.ObjectQuery, limit int) ([]ObjectMatch, error) {
	return s.resolve(ctx, q, limit, true)
}

// resolveByKeys is Resolve without the vector step: the title key, the
// alias keys and the text search alone, which was the whole of Lens
// before version two. The query-set test measures the baseline with it.
func (s *Store) resolveByKeys(ctx context.Context, q lens.ObjectQuery, limit int) ([]ObjectMatch, error) {
	return s.resolve(ctx, q, limit, false)
}

func (s *Store) resolve(ctx context.Context, q lens.ObjectQuery, limit int, vectors bool) ([]ObjectMatch, error) {
	if s == nil || s.pool == nil {
		return nil, errNoDatabase
	}
	if limit <= 0 {
		limit = 10
	}
	if q.TitleKey == "" {
		return nil, nil
	}
	kind := q.Type
	if q.Open {
		kind = ""
	}
	type scored struct {
		match ObjectMatch
		title float64
	}
	best := map[int64]*scored{}
	consider := func(o Object, score float64, method, alias string) {
		if score <= 0 {
			return
		}
		score = adjustForQuery(q, o, score)
		if current, ok := best[o.ID]; !ok || score > current.match.Score {
			best[o.ID] = &scored{match: ObjectMatch{Object: o, Score: math.Round(score*1000) / 1000, Method: method, Alias: alias}}
		}
	}

	// Exact title and alias keys, and the text search.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT o.id, o.type, o.title, o.title_key, COALESCE(o.creator, ''), o.creator_key, COALESCE(o.year, 0), COALESCE(o.language, ''), o.content_key,
		       COALESCE(a.alias, ''), COALESCE(a.alias_key, '')
		FROM atlas.objects o
		LEFT JOIN atlas.aliases a ON a.object_id = o.id AND (a.alias_key = $2 OR a.alias_key = $4 OR a.search @@ plainto_tsquery('simple', $3))
		WHERE ($1 = '' OR o.type = $1)
		  AND (o.title_key = $2 OR o.title_key = $4 OR a.object_id IS NOT NULL OR o.search @@ plainto_tsquery('simple', $3))
		LIMIT 200`, kind, q.TitleKey, q.Title, q.RawKey)
	if err != nil {
		return nil, fmt.Errorf("atlas: resolve: %w", err)
	}
	for rows.Next() {
		var o Object
		var alias, aliasKey string
		if err := rows.Scan(&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Creator, &o.CreatorKey, &o.Year, &o.Language, &o.ContentKey, &alias, &aliasKey); err != nil {
			rows.Close()
			return nil, fmt.Errorf("atlas: resolve scan: %w", err)
		}
		switch {
		case o.TitleKey == q.TitleKey || o.TitleKey == q.RawKey:
			consider(o, 1, "title", "")
		case aliasKey != "" && (aliasKey == q.TitleKey || aliasKey == q.RawKey):
			consider(o, 0.98, "alias", alias)
		case aliasKey != "":
			consider(o, lens.TitleRelevance(q.TitleKey, aliasKey)*0.98, "alias", alias)
		default:
			consider(o, lens.TitleRelevance(q.TitleKey, o.TitleKey), "search", "")
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("atlas: resolve rows: %w", err)
	}

	// The nearest title vectors of objects and of aliases.
	if vector := lens.VectorLiteral(lens.TitleVector(q.Title)); vectors && vector != "" {
		rows, err = s.pool.Query(ctx, `
			SELECT o.id, o.type, o.title, o.title_key, COALESCE(o.creator, ''), o.creator_key, COALESCE(o.year, 0), COALESCE(o.language, ''), o.content_key,
			       1 - (o.embedding <=> $2::vector) AS similarity
			FROM atlas.objects o
			WHERE o.embedding IS NOT NULL AND ($1 = '' OR o.type = $1)
			ORDER BY o.embedding <=> $2::vector
			LIMIT $3`, kind, vector, resolveVectorCandidates)
		if err != nil {
			return nil, fmt.Errorf("atlas: resolve vectors: %w", err)
		}
		for rows.Next() {
			var o Object
			var similarity float64
			if err := rows.Scan(&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Creator, &o.CreatorKey, &o.Year, &o.Language, &o.ContentKey, &similarity); err != nil {
				rows.Close()
				return nil, fmt.Errorf("atlas: resolve vectors scan: %w", err)
			}
			consider(o, lens.VectorRelevance(similarity), "vector", "")
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("atlas: resolve vectors rows: %w", err)
		}
		rows, err = s.pool.Query(ctx, `
			SELECT o.id, o.type, o.title, o.title_key, COALESCE(o.creator, ''), o.creator_key, COALESCE(o.year, 0), COALESCE(o.language, ''), o.content_key,
			       a.alias, 1 - (a.embedding <=> $2::vector) AS similarity
			FROM atlas.aliases a
			JOIN atlas.objects o ON o.id = a.object_id
			WHERE a.embedding IS NOT NULL AND ($1 = '' OR o.type = $1)
			ORDER BY a.embedding <=> $2::vector
			LIMIT $3`, kind, vector, resolveVectorCandidates)
		if err != nil {
			return nil, fmt.Errorf("atlas: resolve alias vectors: %w", err)
		}
		for rows.Next() {
			var o Object
			var alias string
			var similarity float64
			if err := rows.Scan(&o.ID, &o.Type, &o.Title, &o.TitleKey, &o.Creator, &o.CreatorKey, &o.Year, &o.Language, &o.ContentKey, &alias, &similarity); err != nil {
				rows.Close()
				return nil, fmt.Errorf("atlas: resolve alias vectors scan: %w", err)
			}
			consider(o, lens.VectorRelevance(similarity), "alias_vector", alias)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("atlas: resolve alias vectors rows: %w", err)
		}
	}

	matches := make([]ObjectMatch, 0, len(best))
	for _, entry := range best {
		matches = append(matches, entry.match)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Object.ID < matches[j].Object.ID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// adjustForQuery applies what the query says beside the title: a creator
// that disagrees halves the score and one that agrees lifts it a little; a
// year that disagrees lowers it; a kind the query named with the object of
// another kind, when the query was open, is left to the title.
func adjustForQuery(q lens.ObjectQuery, o Object, score float64) float64 {
	if q.CreatorKey != "" && o.CreatorKey != "" {
		if lens.CreatorsAgree(q.CreatorKey, o.CreatorKey) {
			score = math.Min(1, score+0.1)
		} else {
			score *= 0.5
		}
	}
	if q.Year != 0 && o.Year != 0 && q.Year != o.Year {
		score *= 0.8
	}
	return score
}
