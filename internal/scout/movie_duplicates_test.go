package scout

import (
	"reflect"
	"testing"
)

func appleDuplicateFixture(raw, region string) CandidateSource {
	return CandidateSource{ID: raw, Title: "Current Movie", URL: raw, Provider: "Apple TV / iTunes", SourceType: "Movie Store", AccessType: "paid", ListingOnly: true, Region: region}
}

func TestDeduplicateAppleStoreListingsKeepsPricedOriginalURL(t *testing.T) {
	chart := appleDuplicateFixture("https://itunes.apple.com/us/movie/film/id1?uo=2", "US")
	keyword := appleDuplicateFixture("https://itunes.apple.com/us/movie/film/id1?uo=4", "us")
	keyword.Price, keyword.Duration, keyword.ReleaseYear = "Rent USD 5.99 · Buy USD 19.99", "2h 10m", 2026
	other := CandidateSource{Title: "Other source", URL: "https://other.site/watch/movie"}
	input := []CandidateSource{chart, other, keyword}
	before := append([]CandidateSource{}, input...)
	got := deduplicateAppleStoreListings(input)
	if len(got) != 2 || got[0].URL != keyword.URL || got[0].Price != keyword.Price || got[0].Duration != keyword.Duration || got[1].URL != other.URL {
		t.Fatalf("richer catalog URL lost or group ordering changed: %+v", got)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("deduplication rewrote input URLs or records")
	}
}

func TestDeduplicateAppleStoreListingsMetadataPreferenceIsLexicographic(t *testing.T) {
	base := appleDuplicateFixture("https://tv.apple.com/us/movie/film/umc.example?ctx=chart", "US")
	priced := base
	priced.Price = "Buy USD 9.99"
	length := base
	length.Duration = "1h 40m"
	length.ReleaseYear = 2026
	year := base
	year.ReleaseYear = 2026
	unknown := base
	unknown.Duration = "unknown"
	for _, test := range []struct {
		name  string
		input []CandidateSource
		want  CandidateSource
	}{
		{"price before runtime and year", []CandidateSource{length, priced}, priced},
		{"valid duration before year", []CandidateSource{year, length}, length},
		{"known year before absent", []CandidateSource{unknown, year}, year},
		{"invalid duration is not metadata", []CandidateSource{base, unknown}, base},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := deduplicateAppleStoreListings(test.input)
			if len(got) != 1 || !reflect.DeepEqual(got[0], test.want) {
				t.Fatalf("incorrect preferred metadata: %+v", got)
			}
		})
	}
}

func TestDeduplicateAppleStoreListingsPreservesMarketsHostsPathsAndSignedMedia(t *testing.T) {
	inputs := []CandidateSource{
		appleDuplicateFixture("https://itunes.apple.com/us/movie/film/id1?uo=2", "US"),
		appleDuplicateFixture("https://itunes.apple.com/gb/movie/film/id1?uo=2", "GB"),
		appleDuplicateFixture("https://itunes.apple.com/us/movie/film/id1?uo=2", "GB"),
		appleDuplicateFixture("https://tv.apple.com/us/movie/film/id1?uo=2", "US"),
		appleDuplicateFixture("https://itunes.apple.com/us/movie/film/id2?uo=2", "US"),
		appleDuplicateFixture("https://itunes.apple.com/us/movie/a%2Fb/id1?uo=2", "US"),
		appleDuplicateFixture("https://itunes.apple.com/us/movie/a/b/id1?uo=4", "US"),
		appleDuplicateFixture("https://other.site/movie/film?signature=one", "US"),
		appleDuplicateFixture("https://other.site/movie/film?signature=two", "US"),
	}
	mediaOne := appleDuplicateFixture("https://itunes.apple.com/media/movie/file.mp4?signature=one", "US")
	mediaOne.ListingOnly = false
	mediaOne.DirectStreamURL = mediaOne.URL
	mediaTwo := mediaOne
	mediaTwo.URL = "https://itunes.apple.com/media/movie/file.mp4?signature=two"
	mediaTwo.DirectStreamURL = mediaTwo.URL
	inputs = append(inputs, mediaOne, mediaTwo)
	got := deduplicateAppleStoreListings(inputs)
	if !reflect.DeepEqual(got, inputs) {
		t.Fatalf("different markets/catalog pages or signed media were collapsed: %+v", got)
	}
}
