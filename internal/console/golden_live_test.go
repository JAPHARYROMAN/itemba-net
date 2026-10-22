package console

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// liveStore opens the disposable database named by ITEMBA_TEST_DATABASE_URL
// with a fresh atlas schema, holding the cross-process test lock until the
// test ends.
func liveStore(t *testing.T) (*atlas.Store, context.Context) {
	t.Helper()
	url := os.Getenv("ITEMBA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ITEMBA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	store, err := atlas.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	release, err := store.ResetForTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

// freshen moves the dated checks of a recorded report to the present, so the
// levels it recorded do not decay down the ladder as the fixture ages: the
// newest dated check lands at now and the rest keep their distance behind
// it, which keeps the order and the ties the fixture is judged by. Evidence
// the conversion stamped with the present, because the report carried no
// time for it, is left where it is.
func freshen(observations []atlas.Observation) []atlas.Observation {
	now := time.Now()
	dated := func(at time.Time) bool { return !at.IsZero() && now.Sub(at) > time.Hour }
	var latest time.Time
	for _, o := range observations {
		for _, e := range o.Evidence {
			if dated(e.ObservedAt) && e.ObservedAt.After(latest) {
				latest = e.ObservedAt
			}
		}
		if f := o.Fingerprint; f != nil && dated(f.ObservedAt) && f.ObservedAt.After(latest) {
			latest = f.ObservedAt
		}
	}
	if latest.IsZero() {
		return observations
	}
	shift := now.Sub(latest)
	for i := range observations {
		for j := range observations[i].Evidence {
			if e := &observations[i].Evidence[j]; dated(e.ObservedAt) {
				e.ObservedAt = e.ObservedAt.Add(shift)
			}
		}
		if f := observations[i].Fingerprint; f != nil && dated(f.ObservedAt) {
			f.ObservedAt = f.ObservedAt.Add(shift)
		}
	}
	return observations
}

// TestGoldenReportsRecordAndLookUpAgainstPostgreSQL writes the recorded
// reports into a disposable database through the same conversion the search
// routes use, and reads them back the way the streamed search does. The
// reports were captured on 18 October 2026; their checks are moved to the
// present before recording, so the levels read back are the ones recorded.
func TestGoldenReportsRecordAndLookUpAgainstPostgreSQL(t *testing.T) {
	store, ctx := liveStore(t)
	cases := []struct {
		name         string
		locations    int
		firstLevel   int
		firstURL     string
		knownAtLeast int
	}{
		{name: "global-discovery-sintel", locations: 3, firstURL: "https://archive.org/embed/sintel_202604", firstLevel: lens.LevelRendered, knownAtLeast: 3},
		{name: "playback-sintel-first", locations: 3, firstURL: "https://archive.org/embed/sintel-dvd", firstLevel: lens.LevelIdentified, knownAtLeast: 3},
		{name: "live-global-search", locations: 5, firstLevel: lens.LevelListed, knownAtLeast: 2},
		{name: "live-tanzania-search", locations: 4, firstLevel: lens.LevelListed, knownAtLeast: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.DropForTests(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			report := goldenReport(t, tc.name)
			q := lens.Parse(report.Query)

			summary, err := store.RecordObservations(ctx, q, freshen(observationsFromReport(report)))
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

			again, err := store.RecordObservations(ctx, q, freshen(observationsFromReport(report)))
			if err != nil || again.Locations != tc.locations {
				t.Fatalf("recording again must update in place: %+v %v", again, err)
			}
		})
	}
}
