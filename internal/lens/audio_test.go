package lens

import "testing"

func TestParseAsReadsPerformersIntoAudioQueries(t *testing.T) {
	cases := []struct {
		raw, kind string
		want      ObjectQuery
		why       string
	}{
		{"Kind of Blue by Miles Davis", "audio", ObjectQuery{Type: TypeAudio, Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis"}, "the by form"},
		{"Miles Davis - Kind of Blue", "audio", ObjectQuery{Type: TypeAudio, Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis"}, "the dash form names the performer first"},
		{"Kind of Blue by Miles Davis 1959", "audio", ObjectQuery{Type: TypeAudio, Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis", Year: 1959}, "a trailing year is still the year"},
		{"Stand by Me by Ben E. King", "audio", ObjectQuery{Type: TypeAudio, Title: "Stand by Me", TitleKey: "stand by me", Creator: "Ben E. King", CreatorKey: "ben e king"}, "the last by is the credit"},
		{"Stand by Me", "audio", ObjectQuery{Type: TypeAudio, Title: "Stand by Me", TitleKey: "stand by me"}, "a by between single words is part of the title"},
		{"Blinded by the Light", "audio", ObjectQuery{Type: TypeAudio, Title: "Blinded by the Light", TitleKey: "blinded by the light"}, "an article after by is part of the title"},
		{"Killed by Death", "audio", ObjectQuery{Type: TypeAudio, Title: "Killed by Death", TitleKey: "killed by death"}, "single words either side stay a title"},
		{"Hallelujah by Leonard Cohen", "audio", ObjectQuery{Type: TypeAudio, Title: "Hallelujah", TitleKey: "hallelujah", Creator: "Leonard Cohen", CreatorKey: "leonard cohen"}, "a one-word title with a two-word credit"},
		{"Kind of Blue by Miles Davis", "video", ObjectQuery{Type: TypeVideo, Title: "Kind of Blue by Miles Davis", TitleKey: "kind of blue by miles davis"}, "a video query keeps its title whole"},
		{"Dune audiobook", "video", ObjectQuery{Type: TypeVideo, Title: "Dune", TitleKey: "dune"}, "the form's kind wins over a type word"},
		{"Sintel", "", ObjectQuery{Type: TypeVideo, Title: "Sintel", TitleKey: "sintel"}, "no kind leaves Parse's reading"},
		{"Sintel", "hologram", ObjectQuery{Type: TypeVideo, Title: "Sintel", TitleKey: "sintel"}, "an unknown kind leaves Parse's reading"},
		{"https://archive.org/details/kindofblue_202103", "audio", ObjectQuery{Type: TypeVideo, URL: "https://archive.org/details/kindofblue_202103"}, "a URL is identified as it is"},
	}
	for _, tc := range cases {
		got := ParseAs(tc.raw, tc.kind)
		tc.want.Raw = got.Raw
		tc.want.RawKey = got.RawKey
		if got != tc.want {
			t.Errorf("ParseAs(%q, %q) = %+v, want %+v: %s", tc.raw, tc.kind, got, tc.want, tc.why)
		}
	}
}

func TestAudioListingReadsTagsAndTitles(t *testing.T) {
	cases := []struct {
		title, creator, duration string
		year                     int
		want                     AudioIdentity
	}{
		{"Kind of Blue", "Miles Davis", "45:44", 1959, AudioIdentity{Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis", Year: 1959, DurationSeconds: 2744}},
		{"Kind of Blue (1959)", "", "", 0, AudioIdentity{Title: "Kind of Blue", TitleKey: "kind of blue", Year: 1959}},
		{"Miles Davis - Kind of Blue", "", "2744", 0, AudioIdentity{Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis", DurationSeconds: 2744}},
		{"Miles Davis - Kind of Blue", "Miles Davis Sextet", "", 0, AudioIdentity{Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis Sextet", CreatorKey: "miles davis sextet"}},
		{"Kind of Blue (Note)", "Radio FREE Crockett", "", 2022, AudioIdentity{Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Radio FREE Crockett", CreatorKey: "radio free crockett", Year: 2022}},
	}
	for _, tc := range cases {
		if got := AudioListing(tc.title, tc.creator, tc.duration, tc.year); got != tc.want {
			t.Errorf("AudioListing(%q, %q, %q, %d) = %+v, want %+v", tc.title, tc.creator, tc.duration, tc.year, got, tc.want)
		}
	}
}

func TestSameRecordingAndDurations(t *testing.T) {
	davis := AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis", DurationSeconds: 2744}
	cases := []struct {
		name  string
		other AudioIdentity
		want  bool
	}{
		{"same tags", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis", DurationSeconds: 2744}, true},
		{"performer unknown", AudioIdentity{TitleKey: "kind of blue", DurationSeconds: 2740}, true},
		{"duration unknown", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis sextet"}, true},
		{"duration within two percent", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis", DurationSeconds: 2790}, true},
		{"radio edit", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis", DurationSeconds: 1800}, false},
		{"another performer", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "radio free crockett", DurationSeconds: 2744}, false},
		{"another title", AudioIdentity{TitleKey: "blue in green", CreatorKey: "miles davis", DurationSeconds: 2744}, false},
	}
	for _, tc := range cases {
		if got := SameRecording(davis, tc.other); got != tc.want {
			t.Errorf("%s: SameRecording = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !DurationsAgree(60, 62) || DurationsAgree(60, 64) || DurationsAgree(0, 60) || !DurationsAgree(3600, 3660) || DurationsAgree(3600, 3700) {
		t.Fatal("durations agree within three seconds or two percent, and an unknown duration never agrees")
	}
	if SameRecording(AudioIdentity{}, AudioIdentity{}) {
		t.Fatal("an identity without a title names nothing")
	}
}

func TestAudioRelevanceWeighsPerformersAndYears(t *testing.T) {
	q := ParseAs("Kind of Blue by Miles Davis", TypeAudio)
	cases := []struct {
		name string
		id   AudioIdentity
		want float64
	}{
		{"exact", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis"}, 1},
		{"performer unknown", AudioIdentity{TitleKey: "kind of blue"}, 0.8},
		{"ensemble named", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis sextet"}, 1},
		{"other performer", AudioIdentity{TitleKey: "kind of blue", CreatorKey: "radio free crockett"}, 0},
		{"title contains", AudioIdentity{TitleKey: "miles davis kind of blue", CreatorKey: "miles davis"}, 0.8},
		{"other title", AudioIdentity{TitleKey: "sketches of spain", CreatorKey: "miles davis"}, 0},
	}
	for _, tc := range cases {
		if got := AudioRelevance(q, tc.id); got != tc.want {
			t.Errorf("%s: AudioRelevance = %v, want %v", tc.name, got, tc.want)
		}
	}
	dated := ParseAs("Kind of Blue by Miles Davis 1959", TypeAudio)
	if got := AudioRelevance(dated, AudioIdentity{TitleKey: "kind of blue", CreatorKey: "miles davis", Year: 2021}); got != 0.5 {
		t.Fatalf("a different known year halves the score, got %v", got)
	}
	plain := ParseAs("Kind of Blue", TypeAudio)
	if got := AudioRelevance(plain, AudioIdentity{TitleKey: "kind of blue", CreatorKey: "radio free crockett"}); got != 1 {
		t.Fatalf("a query without a performer accepts any performer, got %v", got)
	}
}
