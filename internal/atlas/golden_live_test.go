package atlas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

func goldenReport(t *testing.T, name string) scout.SearchReport {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "reports", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var report scout.SearchReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return report
}

// TestGoldenReportsRecordAndLookUpAgainstPostgreSQL writes the recorded
// reports into a disposable database and reads them back the way the
// streamed search does.
func TestGoldenReportsRecordAndLookUpAgainstPostgreSQL(t *testing.T) {
	store, ctx := liveStore(t)
	cases := []struct {
		name          string
		locations     int
		firstLevel    int
		firstURL      string
		knownAtLeast  int
		firstLevelMin int
	}{
		{name: "global-discovery-sintel", locations: 3, firstURL: "https://archive.org/embed/sintel_202604", firstLevel: lens.LevelRendered, knownAtLeast: 3},
		{name: "playback-sintel-first", locations: 3, firstURL: "https://archive.org/embed/sintel_202409", firstLevel: lens.LevelIdentified, knownAtLeast: 3},
		{name: "live-global-search", locations: 5, firstLevel: lens.LevelListed, knownAtLeast: 2},
		{name: "live-tanzania-search", locations: 4, firstLevel: lens.LevelListed, knownAtLeast: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.pool.Exec(ctx, "DROP SCHEMA IF EXISTS atlas CASCADE"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			report := goldenReport(t, tc.name)
			q := lens.Parse(report.Query)

			summary, err := store.RecordSearch(ctx, q, report)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Locations != tc.locations || summary.Evidence < tc.locations || summary.Skipped != 0 {
				t.Fatalf("summary %+v, want %d locations with evidence and nothing skipped", summary, tc.locations)
			}

			known, err := store.Lookup(ctx, q, lens.DefaultLimit)
			if err != nil {
				t.Fatal(err)
			}
			if len(known) < tc.knownAtLeast {
				t.Fatalf("lookup found %d locations for %q, want at least %d: %+v", len(known), report.Query, tc.knownAtLeast, known)
			}
			if known[0].EffectiveLevel != tc.firstLevel || (tc.firstURL != "" && known[0].Location.URL != tc.firstURL) {
				t.Fatalf("first known location is %s at %s, want %s at %s", known[0].Location.URL, known[0].LevelName, tc.firstURL, lens.LevelName(tc.firstLevel))
			}
			for _, k := range known {
				if k.Evidence == nil || k.Evidence.Method == "" || k.Evidence.ObservedAt.IsZero() {
					t.Fatalf("every known location carries evidence with a method and a time: %+v", k)
				}
			}

			again, err := store.RecordSearch(ctx, q, report)
			if err != nil || again.Locations != tc.locations {
				t.Fatalf("recording again must update in place: %+v %v", again, err)
			}
			var count int
			if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM atlas.locations").Scan(&count); err != nil || count != tc.locations {
				t.Fatalf("locations are unique by URL: %d %v", count, err)
			}
		})
	}
}
