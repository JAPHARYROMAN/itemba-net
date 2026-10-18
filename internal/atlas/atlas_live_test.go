package atlas

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// liveStore opens the disposable test database named by
// ITEMBA_TEST_DATABASE_URL, drops the atlas schema and migrates it fresh.
func liveStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("ITEMBA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ITEMBA_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(ctx, "DROP SCHEMA IF EXISTS atlas CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func TestObjectsHostsLocationsAndEvidenceRoundTrip(t *testing.T) {
	store, ctx := liveStore(t)

	first, err := store.UpsertObject(ctx, Object{Title: "Sintel", Year: 2010})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.UpsertObject(ctx, Object{Title: "SINTEL", Year: 2010})
	if err != nil || again.ID != first.ID || again.Title != "SINTEL" || again.TitleKey != "sintel" || again.Type != lens.TypeVideo {
		t.Fatalf("the same object must upsert to the same row with the newer title: %+v %v", again, err)
	}
	noYear, err := store.UpsertObject(ctx, Object{Title: "Sintel"})
	if err != nil || noYear.ID == first.ID {
		t.Fatalf("a missing year is a distinct identity: %+v %v", noYear, err)
	}
	noYearAgain, err := store.UpsertObject(ctx, Object{Title: "Sintel"})
	if err != nil || noYearAgain.ID != noYear.ID {
		t.Fatalf("two objects without a year must not duplicate: %+v %v", noYearAgain, err)
	}
	if err := store.AddAlias(ctx, first.ID, "Sintel: The Durian Open Movie"); err != nil {
		t.Fatal(err)
	}

	host, err := store.UpsertHost(ctx, "archive.org", "Internet Archive")
	if err != nil {
		t.Fatal(err)
	}
	sameHost, err := store.UpsertHost(ctx, "archive.org", "")
	if err != nil || sameHost.ID != host.ID || sameHost.Provider != "Internet Archive" {
		t.Fatalf("an empty provider must not erase the known one: %+v %v", sameHost, err)
	}

	location, err := store.UpsertLocation(ctx, Location{ObjectID: first.ID, HostID: host.ID, URL: "https://archive.org/details/sintel-dvd", AccessType: "free", Price: "", Title: "Sintel DVD"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpsertLocation(ctx, Location{ObjectID: first.ID, HostID: host.ID, URL: "https://archive.org/details/sintel-dvd", AccessType: "unknown", MediaURL: "https://archive.org/download/sintel-dvd/sintel.mp4"})
	if err != nil || updated.ID != location.ID || updated.AccessType != "free" || updated.MediaURL == "" || updated.Title != "Sintel DVD" {
		t.Fatalf("an upsert must keep known access and title while adding the media URL: %+v %v", updated, err)
	}

	if err := store.RecordEvidence(ctx, Evidence{LocationID: location.ID, Level: 9, Method: "x", Status: "y"}); err == nil {
		t.Fatal("a level off the ladder must be refused")
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := store.RecordEvidence(ctx, Evidence{LocationID: location.ID, Level: lens.LevelRendered, Method: "isolated_video_sample", Status: "started", Egress: "TZ", ObservedAt: old, Artefacts: map[string]any{"decodedFrames": 30}}); err != nil {
		t.Fatal(err)
	}

	known, err := store.Lookup(ctx, lens.Parse("sintel 2010"), 100)
	if err != nil || len(known) != 1 {
		t.Fatalf("exact lookup: %v %+v", err, known)
	}
	k := known[0]
	if k.Evidence == nil || k.Evidence.Level != lens.LevelRendered || k.EffectiveLevel != lens.LevelIdentified || k.LevelName != "identified" || k.Evidence.Egress != "TZ" {
		t.Fatalf("two-day-old playback evidence must decay one level: %+v", k)
	}
	if k.Relevance != 1 || k.Host.Hostname != "archive.org" || k.Location.MediaURL == "" {
		t.Fatalf("lookup must carry host, location and relevance: %+v", k)
	}

	byAlias, err := store.Lookup(ctx, lens.Parse("Sintel: The Durian Open Movie"), 100)
	if err != nil || len(byAlias) != 1 || byAlias[0].Object.ID != first.ID {
		t.Fatalf("alias lookup: %v %+v", err, byAlias)
	}
	byWords, err := store.Lookup(ctx, lens.Parse("durian sintel"), 100)
	if err != nil || len(byWords) == 0 {
		t.Fatalf("full-text lookup across word order: %v %+v", err, byWords)
	}
	byURL, err := store.Lookup(ctx, lens.Parse("https://archive.org/details/sintel-dvd"), 100)
	if err != nil || len(byURL) != 1 || byURL[0].Location.ID != location.ID {
		t.Fatalf("URL lookup: %v %+v", err, byURL)
	}
	wrongYear, err := store.Lookup(ctx, lens.Parse("sintel 1999"), 100)
	if err != nil || len(wrongYear) != 0 {
		t.Fatalf("a different year must not match a dated object: %v %+v", err, wrongYear)
	}
	none, err := store.Lookup(ctx, lens.Parse("completely unrelated title"), 100)
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown title: %v %+v", err, none)
	}
}

func TestRecordSearchWritesLocationsAndEvidenceThatLookupFinds(t *testing.T) {
	store, ctx := liveStore(t)
	now := time.Now().UTC().Format(time.RFC3339)
	report := scout.SearchReport{
		Query:              "Sintel",
		Region:             "ALL",
		AvailabilityChecks: &scout.AvailabilityCheckCoverage{Country: "TZ"},
		Sources: []scout.VerifiedSource{
			{
				CandidateSource:   scout.CandidateSource{Title: "Sintel (the Durian Open Movie project)", URL: "https://archive.org/details/sintel-dvd", DirectStreamURL: "https://archive.org/download/sintel-dvd/sintel.mp4", Provider: "Internet Archive", AccessType: "free", SourceType: "Public Domain", ReleaseYear: 2010},
				AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", CheckedAt: now, Method: "media_probe", Detail: "video/mp4"},
				PlaybackCheck:     &scout.PlaybackCheck{Status: "started", PlaybackConfirmed: true, Country: "TZ", CheckedAt: now, Method: "isolated_video_sample", DecodedFrames: 40, SecondsAdvanced: 1.2, Width: 1920, Height: 818},
			},
			{
				CandidateSource:   scout.CandidateSource{Title: "Sintel", URL: "https://archive.org/details/sintel_cc_by_2010", Provider: "Internet Archive", AccessType: "free", ReleaseYear: 2010},
				AvailabilityCheck: &scout.AvailabilityCheck{Status: "page_reachable", CheckedAt: now, Method: "page_check"},
			},
			{
				CandidateSource: scout.CandidateSource{Title: "Big Buck Bunny", URL: "https://peertube.example/w/bbb", Provider: "PeerTube", AccessType: "free", ReleaseYear: 2008},
			},
			{
				CandidateSource: scout.CandidateSource{Title: "Broken", URL: "not a url"},
			},
		},
	}
	q := lens.Parse("Sintel 2010")
	summary, err := store.RecordSearch(ctx, q, report)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Objects != 3 || summary.Locations != 3 || summary.Evidence != 4 || summary.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	known, err := store.Lookup(ctx, q, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 2 {
		t.Fatalf("the query object must hold exactly the two Sintel locations, got %+v", known)
	}
	if known[0].Location.URL != "https://archive.org/details/sintel-dvd" || known[0].EffectiveLevel != lens.LevelRendered || known[0].Evidence.Method != "isolated_video_sample" {
		t.Fatalf("the rendered location must rank first with its playback evidence: %+v", known[0])
	}
	if known[1].EffectiveLevel != lens.LevelReachable || known[1].Location.AccessType != "free" {
		t.Fatalf("the page-reachable location must follow: %+v", known[1])
	}

	bunny, err := store.Lookup(ctx, lens.Parse("Big Buck Bunny"), 100)
	if err != nil || len(bunny) != 1 || bunny[0].Evidence == nil || bunny[0].Evidence.Method != "provider_listing" || bunny[0].EffectiveLevel != lens.LevelListed {
		t.Fatalf("an unrelated source gets its own object and a provider listing: %v %+v", err, bunny)
	}

	again, err := store.RecordSearch(ctx, q, report)
	if err != nil || again.Locations != 3 {
		t.Fatalf("recording the same report again must update, not duplicate: %+v %v", again, err)
	}
	var locations int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM atlas.locations").Scan(&locations); err != nil || locations != 3 {
		t.Fatalf("locations must be unique by URL, got %d %v", locations, err)
	}
}

func TestRecordSearchOnANilStoreIsAnExplicitError(t *testing.T) {
	var store *Store
	if _, err := store.RecordSearch(context.Background(), lens.Parse("x"), scout.SearchReport{}); err == nil {
		t.Fatal("nil store must refuse to record")
	}
	if known, err := store.Lookup(context.Background(), lens.Parse("x"), 10); err != nil || known != nil {
		t.Fatal("nil store lookup returns nothing without error")
	}
}
