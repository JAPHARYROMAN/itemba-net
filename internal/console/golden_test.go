package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// goldenReport loads one of the recorded search reports under testdata/reports
// at the repository root. They were captured from live runs on 18 October
// 2026 and are the fixtures the white paper's ranking is judged against.
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

func TestGoldenReportsMergeOntoTheLadderAsRecorded(t *testing.T) {
	cases := []struct {
		name      string
		count     int
		firstURL  string
		levels    []int
		hostCount int
	}{
		{
			// Two decoded starts and one skipped sample on Internet Archive.
			name:     "global-discovery-sintel",
			count:    3,
			firstURL: "https://archive.org/embed/sintel_202604",
			levels:   []int{lens.LevelRendered, lens.LevelRendered, lens.LevelIdentified},
		},
		{
			// Media reachable but the sampler could not play it, plus a storefront page.
			// Both Archive copies read as Sintel and were checked in the same second,
			// so the tie falls to URL order.
			name:     "playback-sintel-first",
			count:    3,
			firstURL: "https://archive.org/embed/sintel-dvd",
			levels:   []int{lens.LevelIdentified, lens.LevelIdentified, lens.LevelReachable},
		},
		{
			// Web and storefront listings with no checks at all.
			name:      "live-global-search",
			count:     5,
			levels:    []int{lens.LevelListed, lens.LevelListed, lens.LevelListed, lens.LevelListed, lens.LevelListed},
			hostCount: 5,
		},
		{
			name:     "live-tanzania-search",
			count:    4,
			firstURL: "https://archive.org/embed/PhantasmagoriaTheater-NightOfTheLivingDead1968321-3",
			levels:   []int{lens.LevelListed, lens.LevelListed, lens.LevelListed, lens.LevelListed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := goldenReport(t, tc.name)
			q := lens.Parse(report.Query)
			merged := mergeLocations(q, nil, report, nil)
			if len(merged.Locations) != tc.count || merged.Fresh != tc.count || merged.Known != 0 {
				t.Fatalf("expected %d fresh locations, got %+v", tc.count, merged)
			}
			if len(merged.Locations) > lens.DefaultLimit {
				t.Fatal("the hundred cap was not applied")
			}
			if tc.firstURL != "" && merged.Locations[0].URL != tc.firstURL {
				t.Fatalf("first location is %s (%s), want %s", merged.Locations[0].URL, merged.Locations[0].LevelName, tc.firstURL)
			}
			hosts := map[string]bool{}
			for i, loc := range merged.Locations {
				if loc.Level != tc.levels[i] || loc.LevelName != lens.LevelName(tc.levels[i]) {
					t.Fatalf("location %d %s is %s, want %s", i, loc.URL, loc.LevelName, lens.LevelName(tc.levels[i]))
				}
				if loc.Origin != "fresh" || loc.Host == "" {
					t.Fatalf("location %d must be a fresh result with a host: %+v", i, loc)
				}
				if i > 0 && merged.Locations[i-1].Level < loc.Level {
					t.Fatalf("levels must not rise down the list: %+v", merged.Locations)
				}
				hosts[loc.Host] = true
			}
			if tc.hostCount != 0 && len(hosts) != tc.hostCount {
				t.Fatalf("expected %d distinct hosts, got %d", tc.hostCount, len(hosts))
			}
		})
	}
}

func TestGoldenQueriesParseToTheirObjects(t *testing.T) {
	cases := map[string]lens.ObjectQuery{
		"global-discovery-sintel": {Type: lens.TypeVideo, Title: "Sintel", TitleKey: "sintel"},
		"live-global-search":      {Type: lens.TypeVideo, Title: "Interstellar", TitleKey: "interstellar", Year: 2014},
		"live-tanzania-search":    {Type: lens.TypeVideo, Title: "Night of the Living Dead", TitleKey: "night of the living dead", Year: 1968},
	}
	for name, want := range cases {
		report := goldenReport(t, name)
		got := lens.Parse(report.Query)
		if got.Type != want.Type || got.Title != want.Title || got.TitleKey != want.TitleKey || got.Year != want.Year {
			t.Fatalf("%s: Parse(%q) = %+v, want %+v", name, report.Query, got, want)
		}
	}
}
