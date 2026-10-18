package lens

import "testing"

func TestDurationSecondsReadsEveryListedForm(t *testing.T) {
	cases := map[string]float64{
		"4508":       4508,
		"9m56s":      596,
		"1h 35m 12s": 5712,
		"1:40:00":    6000,
		"35:12":      2112,
		"PT1H38M36S": 5916,
		"PT01:38:36": 5916,
		"PT52M":      3120,
		"PT3M":       180,
		"":           0,
		"soon":       0,
		"1:75":       0,
		"-5":         0,
		"PTnonsense": 0,
		"1:2:3:4":    0,
		"NaN":        0,
		"Inf":        0,
	}
	for in, want := range cases {
		if got := DurationSeconds(in); got != want {
			t.Fatalf("DurationSeconds(%q) = %v, want %v", in, got, want)
		}
	}
	if FormatDuration(596) != "9m56s" || FormatDuration(0) != "" || FormatDuration(5916) != "1h38m36s" {
		t.Fatal("FormatDuration")
	}
}
