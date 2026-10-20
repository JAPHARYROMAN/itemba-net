package lens

import "testing"

func TestParseOpenReadsTheKindFromTheWordsOrLeavesItOpen(t *testing.T) {
	cases := []struct {
		raw            string
		kind           string
		open           bool
		title, creator string
		year           int
	}{
		{"Sintel", TypeVideo, true, "Sintel", "", 0},
		{"sintel 2010", TypeVideo, true, "sintel", "", 2010},
		{"big buck bunny film", TypeVideo, false, "big buck bunny", "", 0},
		{"the film Das Cabinet des Dr Caligari", TypeVideo, false, "Das Cabinet des Dr Caligari", "", 0},
		{"Nosferatu 1922 movie", TypeVideo, false, "Nosferatu", "", 1922},
		{"Kind of Blue by Miles Davis", TypeVideo, true, "Kind of Blue", "Miles Davis", 0},
		{"Miles Davis - Kind of Blue", TypeVideo, true, "Kind of Blue", "Miles Davis", 0},
		{"the recording of So What by Miles Davis", TypeAudio, false, "So What", "Miles Davis", 0},
		{"the painting Starry Night by Van Gogh", TypeImage, false, "Starry Night", "Van Gogh", 0},
		{"a photo of the moon landing 1969", TypeImage, false, "the moon landing", "", 1969},
		{"the book Alice au pays des merveilles", TypeDocument, false, "Alice au pays des merveilles", "", 0},
		{"the novel The Wretched by Victor Hugo", TypeDocument, false, "The Wretched", "Victor Hugo", 0},
		{"Mondscheinsonate", TypeVideo, true, "Mondscheinsonate", "", 0},
		{"the one with the rabbit hole", TypeVideo, true, "the one with the rabbit hole", "", 0},
	}
	for _, tc := range cases {
		q := ParseOpen(tc.raw)
		if q.Type != tc.kind || q.Open != tc.open || q.Title != tc.title || q.Creator != tc.creator || q.Year != tc.year {
			t.Errorf("ParseOpen(%q) = kind %q open %v title %q creator %q year %d; want %q %v %q %q %d", tc.raw, q.Type, q.Open, q.Title, q.Creator, q.Year, tc.kind, tc.open, tc.title, tc.creator, tc.year)
		}
		if q.Raw != tc.raw {
			t.Errorf("ParseOpen(%q) keeps the raw query: %q", tc.raw, q.Raw)
		}
	}
	if q := ParseOpen("https://archive.org/details/sintel"); q.URL == "" || q.Open {
		t.Fatalf("an address is identified as it is: %+v", q)
	}
	if q := ParseOpen("the"); !q.Open || q.Title != "the" {
		t.Fatalf("a bare article is a title: %+v", q)
	}
	if q := Parse("big buck bunny film"); q.Open || q.Type != TypeVideo {
		t.Fatalf("Parse is unchanged: %+v", q)
	}
}
