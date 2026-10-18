package lens

import (
	"strings"
	"testing"
)

func TestCanonicalQueryStripsSearchPhrasingAndTrailingYears(t *testing.T) {
	cases := []struct {
		query string
		title string
		year  int
	}{
		{"Watch Sintel 2010 full movie online", "Sintel", 2010},
		{"Sintel (2010)", "Sintel", 2010},
		{"Night of the Living Dead [1968]", "Night of the Living Dead", 1968},
		{"2001", "2001", 0},
		{"2001: A Space Odyssey", "2001: A Space Odyssey", 0},
		{"Blade Runner 2049", "Blade Runner 2049", 0},
		{"Blade Runner 2049 (2017)", "Blade Runner 2049", 2017},
		{"the matrix movie", "the matrix", 0},
		{"online", "online", 0},
	}
	for _, tc := range cases {
		title, year := CanonicalQuery(tc.query)
		if title != tc.title || year != tc.year {
			t.Fatalf("CanonicalQuery(%q) = %q, %d; want %q, %d", tc.query, title, year, tc.title, tc.year)
		}
	}
}

func TestTitleDetailsKeepsTitleYearsAndReadsReleaseLabels(t *testing.T) {
	cases := []struct {
		title       string
		releaseYear int
		wantTitle   string
		wantYear    int
	}{
		{"Night of the Living Dead (1968)", 0, "Night of the Living Dead", 1968},
		{"Night of the Living Dead (1968 — George A. Romero)", 0, "Night of the Living Dead", 1968},
		{"2001: A Space Odyssey", 0, "2001: A Space Odyssey", 0},
		{"2001: A Space Odyssey (1968)", 0, "2001: A Space Odyssey", 1968},
		{"Sintel (2010)", 2011, "Sintel", 2011},
		{"Sintel [HD] (restored 2024)", 0, "Sintel", 2024},
		{"  ", 0, "", 0},
	}
	for _, tc := range cases {
		title, year := TitleDetails(tc.title, tc.releaseYear)
		if title != tc.wantTitle || year != tc.wantYear {
			t.Fatalf("TitleDetails(%q, %d) = %q, %d; want %q, %d", tc.title, tc.releaseYear, title, year, tc.wantTitle, tc.wantYear)
		}
	}
}

func TestListedTitleKeepsTitleWordsAndSeparatesReleaseLabels(t *testing.T) {
	cases := []struct {
		title       string
		releaseYear int
		wantTitle   string
		wantYear    int
	}{
		{"Fixture Film", 0, "Fixture Film", 0},
		{"Disaster Movie", 2008, "Disaster Movie", 2008},
		{"Sintel (2010)", 0, "Sintel", 2010},
		{"Sintel (the Durian Open Movie project) 4-DVD Box set", 0, "Sintel", 0},
		{"Sintel 2010", 0, "Sintel", 2010},
		{"Blade Runner 2049", 0, "Blade Runner 2049", 0},
		{"2001: A Space Odyssey (1968)", 0, "2001: A Space Odyssey", 1968},
		{"Night of the Living Dead (1968 — George A. Romero)", 1969, "Night of the Living Dead", 1969},
		{"Interstellar (2014): Where to Watch & Stream | Flickd", 0, "Interstellar", 2014},
		{"", 0, "", 0},
	}
	for _, tc := range cases {
		title, year := ListedTitle(tc.title, tc.releaseYear)
		if title != tc.wantTitle || year != tc.wantYear {
			t.Fatalf("ListedTitle(%q, %d) = %q, %d; want %q, %d", tc.title, tc.releaseYear, title, year, tc.wantTitle, tc.wantYear)
		}
	}
}

func TestWordsAndSubstantiveWords(t *testing.T) {
	if got := strings.Join(Words("The Night-of the_Living Dead: 1968!"), ","); got != "the,night,of,the,living,dead,1968" {
		t.Fatalf("Words: %s", got)
	}
	if got := strings.Join(SubstantiveWords("The Night of the Living Dead and a Cat"), ","); got != "night,living,dead,cat" {
		t.Fatalf("SubstantiveWords: %s", got)
	}
}

func TestHasUnrequestedDerivativeDetectsRelatedVideosOnly(t *testing.T) {
	if !HasUnrequestedDerivative("Sintel", "Sintel Official Trailer") {
		t.Fatal("a trailer is a derivative of a film query")
	}
	if HasUnrequestedDerivative("Sintel trailer", "Sintel Official Trailer") {
		t.Fatal("a requested trailer is not unrequested")
	}
	if !HasUnrequestedDerivative("Dune", "Dune (2021) Behind the Scenes") || !HasUnrequestedDerivative("Dune", "Dune ending explained") {
		t.Fatal("multi-word phrases and plural forms are derivatives")
	}
	if HasUnrequestedDerivative("Dune", "Dune (2021) Full Movie") || HasUnrequestedDerivative("The Score", "The Score (2001)") {
		t.Fatal("a plain feature title, or a derivative word the query itself contains, is not a derivative")
	}
}

func TestFileYearConflictsAndSameFeatureTitle(t *testing.T) {
	if !FileYearConflicts("sintel_2009.mp4", "Sintel", 2010) {
		t.Fatal("a file year that is neither the release year nor in the title conflicts")
	}
	if FileYearConflicts("Sintel.2010.1080p.mp4", "Sintel", 2010) || FileYearConflicts("2001-a-space-odyssey-1968.mkv", "2001: A Space Odyssey", 1968) {
		t.Fatal("the release year and years that are part of the title do not conflict")
	}
	if FileYearConflicts("anything_1999.mp4", "Sintel", 0) {
		t.Fatal("without a release year nothing can conflict")
	}
	if !SameFeatureTitle("Night of the Living Dead (1968)", "Night of the Living Dead (1968) Full Movie Review") {
		t.Fatal("the same feature with a review suffix after its year label still matches")
	}
	if SameFeatureTitle("Night of the Living Dead (1968)", "Night of the Living Dead Trailer") || SameFeatureTitle("", "Night of the Living Dead") {
		t.Fatal("a derivative or an empty title is not the same feature")
	}
}
