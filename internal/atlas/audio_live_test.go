package atlas

import (
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// TestAudioRecordingsAreKeptApartByPerformer proves the Phase 3 identity:
// two recordings with the same title and year but different performers are
// different objects, a video of the same title is a third, a listing joins
// the asked recording only when its performer agrees, and a lookup that
// names a performer leaves the other recordings out.
func TestAudioRecordingsAreKeptApartByPerformer(t *testing.T) {
	store, ctx := liveStore(t)

	davis, err := store.UpsertObject(ctx, Object{Type: lens.TypeAudio, Title: "Kind of Blue", Creator: "Miles Davis", Year: 1959})
	if err != nil || davis.CreatorKey != "miles davis" || davis.Creator != "Miles Davis" {
		t.Fatalf("an audio object carries its performer and key: %+v %v", davis, err)
	}
	crockett, err := store.UpsertObject(ctx, Object{Type: lens.TypeAudio, Title: "Kind of Blue", Creator: "Radio FREE Crockett", Year: 1959})
	if err != nil || crockett.ID == davis.ID {
		t.Fatalf("the same title by another performer is another recording: %+v %v", crockett, err)
	}
	again, err := store.UpsertObject(ctx, Object{Type: lens.TypeAudio, Title: "KIND OF BLUE", Creator: "miles davis", Year: 1959})
	if err != nil || again.ID != davis.ID {
		t.Fatalf("the same recording upserts to the same row: %+v %v", again, err)
	}
	film, err := store.UpsertObject(ctx, Object{Title: "Kind of Blue", Year: 1959})
	if err != nil || film.ID == davis.ID || film.Type != lens.TypeVideo || film.CreatorKey != "" {
		t.Fatalf("a video of the same title is a third object with no performer: %+v %v", film, err)
	}

	// The year is part of the identity, so the query names it to join the
	// 1959 recording rather than an undated one.
	q := lens.ParseAs("Kind of Blue by Miles Davis 1959", lens.TypeAudio)
	summary, err := store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Kind: lens.TypeAudio, Title: "Kind of Blue", Creator: "Miles Davis Sextet", URL: "https://archive.org/details/kind-of-blue-1959", Provider: "Internet Archive", Duration: "2744", AccessType: "free"},
			Evidence: []Evidence{{Level: lens.LevelIdentified, Method: "http_media_probe", Status: "media_reachable", Egress: "TZ"}}},
		{Listing: Listing{Kind: lens.TypeAudio, Title: "Kind of Blue (Note)", Creator: "Radio FREE Crockett", URL: "https://archive.org/details/kobn_20220518", Provider: "Internet Archive", Year: 2022}},
		{Listing: Listing{Kind: lens.TypeAudio, Title: "Kind of Blue", URL: "https://example.org/kind-of-blue", Provider: "Example", Duration: "45:44"}},
	})
	if err != nil || summary.Locations != 3 || summary.Evidence != 3 {
		t.Fatalf("three listings become three locations with evidence: %+v %v", summary, err)
	}

	byPerformer, err := store.Lookup(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]KnownLocation{}
	for _, k := range byPerformer {
		urls[k.Location.URL] = k
	}
	if len(byPerformer) != 2 || urls["https://archive.org/details/kobn_20220518"].Location.ID != 0 {
		t.Fatalf("a lookup naming the performer returns the ensemble's recording and the one with no performer, never another performer's: %+v", byPerformer)
	}
	sextet := urls["https://archive.org/details/kind-of-blue-1959"]
	if sextet.Object.ID != davis.ID || sextet.Location.DurationSeconds != 2744 || sextet.EffectiveLevel != lens.LevelIdentified || sextet.Relevance != 1 {
		t.Fatalf("the ensemble's listing joined the asked recording with its length and level: %+v", sextet)
	}
	if unknown := urls["https://example.org/kind-of-blue"]; unknown.Object.ID != davis.ID || unknown.Location.DurationSeconds != 2744 {
		t.Fatalf("a listing with no performer joins the asked recording and keeps its listed length: %+v", unknown)
	}

	anyPerformer, err := store.Lookup(ctx, lens.ParseAs("Kind of Blue", lens.TypeAudio), 10)
	if err != nil || len(anyPerformer) != 3 {
		t.Fatalf("a lookup without a performer returns every recording of the title: %d %v", len(anyPerformer), err)
	}
	asVideo, err := store.Lookup(ctx, lens.ParseAs("Kind of Blue", lens.TypeVideo), 10)
	if err != nil || len(asVideo) != 0 {
		t.Fatalf("a video lookup never returns recordings: %d %v", len(asVideo), err)
	}
}

// TestPictureLookupAgreesOnMakersByContainment proves the index reads a
// maker the way Lens does: "Van Gogh" asked for finds a picture credited to
// "Vincent van Gogh", and another maker's picture stays out.
func TestPictureLookupAgreesOnMakersByContainment(t *testing.T) {
	store, ctx := liveStore(t)
	q := lens.ParseAs("Starry Night by Vincent van Gogh", lens.TypeImage)
	summary, err := store.RecordObservations(ctx, q, []Observation{
		{Listing: Listing{Kind: lens.TypeImage, Title: "VanGogh-starry night", Creator: "Vincent van Gogh", URL: "https://commons.wikimedia.org/wiki/File:VanGogh-starry_night.jpg", MediaURL: "https://upload.wikimedia.org/a.jpg", Provider: "Wikimedia Commons", AccessType: "free", Year: 1889},
			Evidence: []Evidence{{Level: lens.LevelIdentified, Method: "image_decode", Status: "decoded", Egress: "TZ"}}},
		{Listing: Listing{Kind: lens.TypeImage, Title: "Starry Night", Creator: "Someone Else", URL: "https://example.org/starry", Provider: "Example", AccessType: "free"}},
	})
	if err != nil || summary.Locations != 2 {
		t.Fatalf("two pictures recorded: %+v %v", summary, err)
	}
	short, err := store.Lookup(ctx, lens.ParseAs("Starry Night by Van Gogh", lens.TypeImage), 10)
	if err != nil || len(short) != 1 || short[0].Object.Creator != "Vincent van Gogh" || short[0].EffectiveLevel != lens.LevelIdentified {
		t.Fatalf("a maker named in part finds the picture credited in full and not another maker's: %+v %v", short, err)
	}
	any, err := store.Lookup(ctx, lens.ParseAs("Starry Night", lens.TypeImage), 10)
	if err != nil || len(any) != 2 {
		t.Fatalf("a lookup without a maker returns every picture of the title: %d %v", len(any), err)
	}
}
