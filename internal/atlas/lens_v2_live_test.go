package atlas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// The Lens version two query set: objects as Atlas holds them, with the
// titles other languages give them as aliases, and loose queries with the
// object each should resolve to. The test seeds the objects, resolves
// every query and reports the top-one resolution rate, twice: by the
// title key and the text search alone, which was the whole of Lens before
// this, and with the vector step, so the gain is a number. The three
// description queries are expected to miss without a model; they stay in
// the set to keep the rate honest.
type querySet struct {
	Objects []struct {
		Key     string   `json:"key"`
		Kind    string   `json:"kind"`
		Title   string   `json:"title"`
		Creator string   `json:"creator"`
		Year    int      `json:"year"`
		Aliases []string `json:"aliases"`
	} `json:"objects"`
	Queries []struct {
		Query  string `json:"query"`
		Expect string `json:"expect"`
		Note   string `json:"note"`
	} `json:"queries"`
}

func TestLensVersionTwoResolvesTheQuerySet(t *testing.T) {
	store, ctx := liveStore(t)
	data, err := os.ReadFile(filepath.Join("testdata", "lens_v2_queries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var set querySet
	if err := json.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, o := range set.Objects {
		object, err := store.UpsertObject(ctx, Object{Type: o.Kind, Title: o.Title, Creator: o.Creator, Year: o.Year})
		if err != nil {
			t.Fatalf("seed %s: %v", o.Key, err)
		}
		for _, alias := range o.Aliases {
			if err := store.AddAlias(ctx, object.ID, alias); err != nil {
				t.Fatalf("alias %q: %v", alias, err)
			}
		}
		ids[o.Key] = object.ID
	}

	resolve := func(vectors bool) (hits int, misses []string) {
		for _, c := range set.Queries {
			q := lens.ParseOpen(c.Query)
			var top int64
			if vectors {
				matches, err := store.Resolve(ctx, q, 5)
				if err != nil {
					t.Fatalf("resolve %q: %v", c.Query, err)
				}
				if len(matches) > 0 {
					top = matches[0].Object.ID
				}
			} else {
				matches, err := store.resolveByKeys(ctx, q, 5)
				if err != nil {
					t.Fatalf("resolve by keys %q: %v", c.Query, err)
				}
				if len(matches) > 0 {
					top = matches[0].Object.ID
				}
			}
			if top == ids[c.Expect] {
				hits++
			} else {
				misses = append(misses, c.Query+" ("+c.Note+")")
			}
		}
		return hits, misses
	}
	baselineHits, baselineMisses := resolve(false)
	hits, misses := resolve(true)
	total := len(set.Queries)
	t.Logf("top-one resolution: keys and text search alone %d/%d (%.0f%%); with title vectors %d/%d (%.0f%%)", baselineHits, total, 100*float64(baselineHits)/float64(total), hits, total, 100*float64(hits)/float64(total))
	t.Logf("baseline misses: %v", baselineMisses)
	t.Logf("misses with vectors: %v", misses)
	if hits < baselineHits {
		t.Fatalf("the vector step must not lose queries the keys resolved: %d < %d", hits, baselineHits)
	}
	if float64(hits)/float64(total) < 0.85 {
		t.Fatalf("Lens version two resolves at least 85%% of the query set: %d/%d, misses %v", hits, total, misses)
	}

	// The lookup falls back to the same resolution when its own matching
	// finds nothing, so a misspelt title still reaches the object's locations.
	host, err := store.UpsertHost(ctx, "archive.org", "Internet Archive")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertLocation(ctx, Location{ObjectID: ids["alice"], HostID: host.ID, URL: "https://archive.org/details/alice-scan", AccessType: "free", Title: "Alice's Adventures in Wonderland"}); err != nil {
		t.Fatal(err)
	}
	known, err := store.Lookup(ctx, lens.ParseOpen("Alicia en el pais de las maravillas"), 10)
	if err != nil || len(known) != 1 || known[0].Object.ID != ids["alice"] || known[0].Relevance < lens.SimilarTitleThreshold {
		t.Fatalf("a Spanish title the index holds as an alias reaches the location through the vector fallback: %+v %v", known, err)
	}
	known, err = store.Lookup(ctx, lens.ParseOpen("Alice's Adventures in Wonderlnd"), 10)
	if err != nil || len(known) != 1 || known[0].Object.ID != ids["alice"] {
		t.Fatalf("a misspelt title reaches the location: %+v %v", known, err)
	}
}
