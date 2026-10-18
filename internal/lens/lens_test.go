package lens

import (
	"testing"
	"time"
)

func TestParseTitlesYearsTypesURLsAndHashes(t *testing.T) {
	cases := []struct {
		raw   string
		want  ObjectQuery
		title string
	}{
		{"Sintel", ObjectQuery{Type: TypeVideo, Title: "Sintel", TitleKey: "sintel"}, ""},
		{"  Sintel (2010) ", ObjectQuery{Type: TypeVideo, Title: "Sintel", TitleKey: "sintel", Year: 2010}, ""},
		{"Blade Runner 2049", ObjectQuery{Type: TypeVideo, Title: "Blade Runner 2049", TitleKey: "blade runner 2049"}, "a number beyond next year is part of the title"},
		{"Blade Runner 2049 (2017)", ObjectQuery{Type: TypeVideo, Title: "Blade Runner 2049", TitleKey: "blade runner 2049", Year: 2017}, ""},
		{"2001", ObjectQuery{Type: TypeVideo, Title: "2001", TitleKey: "2001"}, "a lone year is a title, not a year"},
		{"Amélie 2001 film", ObjectQuery{Type: TypeVideo, Title: "Amélie", TitleKey: "amelie", Year: 2001}, ""},
		{"Dune audiobook", ObjectQuery{Type: TypeAudio, Title: "Dune", TitleKey: "dune"}, ""},
		{"Clean Code pdf", ObjectQuery{Type: TypeDocument, Title: "Clean Code", TitleKey: "clean code"}, ""},
		{"The Game", ObjectQuery{Type: TypeVideo, Title: "The Game", TitleKey: "the game"}, "a title that is only an article plus a hint word keeps the word"},
		{"Portal game", ObjectQuery{Type: TypeGame, Title: "Portal", TitleKey: "portal"}, ""},
		{"https://archive.org/details/sintel-dvd", ObjectQuery{Type: TypeVideo, URL: "https://archive.org/details/sintel-dvd"}, ""},
		{"d41d8cd98f00b204e9800998ecf8427e", ObjectQuery{Type: TypeVideo, Hash: "d41d8cd98f00b204e9800998ecf8427e"}, ""},
	}
	for _, tc := range cases {
		got := Parse(tc.raw)
		tc.want.Raw = got.Raw
		if got.URL == "" && got.Hash == "" {
			if got.RawKey != NormalizeTitle(tc.raw) {
				t.Fatalf("Parse(%q).RawKey = %q, want the whole query normalised", tc.raw, got.RawKey)
			}
			tc.want.RawKey = got.RawKey
		} else if got.RawKey != "" {
			t.Fatalf("Parse(%q) must not set RawKey for a URL or hash", tc.raw)
		}
		if got != tc.want {
			t.Fatalf("Parse(%q) = %+v, want %+v %s", tc.raw, got, tc.want, tc.title)
		}
	}
}

func TestNormalizeTitleRemovesMarksCaseAndPunctuation(t *testing.T) {
	cases := map[string]string{
		"Amélie":                  "amelie",
		"WALL·E":                  "wall e",
		"Tom & Jerry: The Movie!": "tom and jerry the movie",
		"  Spaced   out  ":        "spaced out",
		"Léon: The Professional":  "leon the professional",
		"":                        "",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Fatalf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleRelevanceAndHostname(t *testing.T) {
	if TitleRelevance("sintel", "sintel") != 1 || TitleRelevance("sintel", "sintel 2010 open movie") != 0.8 {
		t.Fatal("exact and containing matches must score 1 and 0.8")
	}
	if got := TitleRelevance("blade runner", "runner up"); got <= 0 || got >= 0.8 {
		t.Fatalf("partial overlap should score between 0 and 0.8, got %v", got)
	}
	if TitleRelevance("", "x") != 0 || TitleRelevance("x", "") != 0 {
		t.Fatal("empty keys score zero")
	}
	if Hostname("https://WWW.Archive.org/details/x") != "archive.org" || Hostname("not a url") != "" {
		t.Fatal("hostname extraction")
	}
}

func TestLadderMapsScoutStatusesAndDecays(t *testing.T) {
	if LevelFromChecks("media_reachable", "started", true) != LevelRendered {
		t.Fatal("a confirmed playback start is Rendered")
	}
	if LevelFromChecks("media_reachable", "started", false) != LevelIdentified {
		t.Fatal("an unconfirmed start cannot be Rendered")
	}
	if LevelFromChecks("page_reachable", "", false) != LevelReachable || LevelFromChecks("region_blocked", "", false) != LevelReachable {
		t.Fatal("responding pages are Reachable")
	}
	if LevelFromChecks("not_found", "", false) != LevelListed || LevelFromChecks("", "", false) != LevelListed {
		t.Fatal("unchecked or missing locations are Listed")
	}
	if LevelName(LevelClean) != "clean" || LevelName(9) != "unknown" {
		t.Fatal("level names")
	}
	if EffectiveLevel(LevelRendered, TypeVideo, time.Hour) != LevelRendered {
		t.Fatal("fresh evidence keeps its level")
	}
	if EffectiveLevel(LevelRendered, TypeVideo, 25*time.Hour) != LevelIdentified {
		t.Fatal("stale playback evidence drops one level")
	}
	if EffectiveLevel(LevelListed, TypeVideo, 1000*time.Hour) != LevelListed || EffectiveLevel(-3, TypeVideo, 0) != LevelListed {
		t.Fatal("Listed never drops below itself")
	}
}

func TestRankDedupesOrdersDiversifiesAndCaps(t *testing.T) {
	now := time.Now()
	var candidates []Candidate
	for i := 0; i < 30; i++ {
		candidates = append(candidates, Candidate{URL: "https://mirror.example/" + string(rune('a'+i)), Host: "mirror.example", Level: LevelIdentified, ObservedAt: now, Relevance: 1})
	}
	candidates = append(candidates,
		Candidate{URL: "https://archive.org/one", Host: "archive.org", Level: LevelRendered, ObservedAt: now.Add(-time.Hour), Relevance: 0.8},
		Candidate{URL: "https://archive.org/one", Host: "archive.org", Level: LevelListed, ObservedAt: now, Relevance: 1},
		Candidate{URL: "https://other.example/x", Host: "other.example", Level: LevelListed, ObservedAt: now, Relevance: 1},
		Candidate{URL: "", Host: "ignored", Level: LevelClean},
	)

	ranked := Rank(candidates, 20, 5)
	if len(ranked) != 20 {
		t.Fatalf("cap not applied: %d", len(ranked))
	}
	if ranked[0].URL != "https://archive.org/one" || ranked[0].Level != LevelRendered {
		t.Fatalf("the strongest duplicate must win and lead: %+v", ranked[0])
	}
	mirrors := 0
	for i, c := range ranked[:7] {
		if c.Host == "mirror.example" {
			mirrors++
		}
		if i == 6 && c.URL != "https://other.example/x" {
			t.Fatalf("diversity must let a weaker host in before the sixth mirror: %+v", ranked[:7])
		}
	}
	if mirrors != 5 {
		t.Fatalf("expected five mirrors in the diverse pass, got %d", mirrors)
	}
	if ranked[len(ranked)-1].Host != "mirror.example" {
		t.Fatal("displaced mirrors fill the remaining slots at the end")
	}
	if len(Rank(nil, 0, 0)) != 0 {
		t.Fatal("empty input")
	}
}
