package scout

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestArchiveFileCannotContradictReleaseYearOrSelectDVDMenu(t *testing.T) {
	payload := archiveResolutionFixture("night_movie")
	payload["files"] = []map[string]any{
		{"name": "Night_Of_The_Living_Dead_1990.mp4", "length": "5752", "size": "1000000"},
		{"name": "Night_Of_The_Living_Dead_1968_menu.mp4", "length": "5752", "size": "1000000"},
		{"name": "Night_Of_The_Living_Dead_1968.mp4", "length": "5752", "size": "300000000"},
	}
	client := &http.Client{Transport: freeSourceTransport(func(req *http.Request) (*http.Response, error) { return freeSourceJSONResponse(req, payload), nil })}
	result, ok := resolveFreeMovieSource(context.Background(), client, freeArchiveCandidate("night_movie"))
	if !ok || !strings.HasSuffix(result.DirectStreamURL, "/Night_Of_The_Living_Dead_1968.mp4") {
		t.Fatalf("wrong edition or menu selected: %+v", result)
	}
}

func TestMovieFileYearCanBePartOfMovieTitle(t *testing.T) {
	if movieFileYearConflicts("2001_A_Space_Odyssey_1968.mp4", "2001: A Space Odyssey", 1968) {
		t.Fatal("movie title number was confused with its release year")
	}
	if !movieFileYearConflicts("The_Saints_Girl_Friday_1953.mp4", "His Girl Friday", 1940) {
		t.Fatal("contradictory filename year accepted")
	}
}
