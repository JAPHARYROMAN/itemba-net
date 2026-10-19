package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

func TestValidateSearchReadsTheKind(t *testing.T) {
	cases := []struct {
		query, region, kind string
		wantKind, wantErr   string
	}{
		{"Sintel", "", "", lens.TypeVideo, ""},
		{"Sintel", "us", "video", lens.TypeVideo, ""},
		{"Kind of Blue by Miles Davis", "", "Audio", lens.TypeAudio, ""},
		{"Sintel", "", "image", "", "Choose movies or audio."},
		{"", "", "audio", "", "Enter a recording's title, with its performer if known, using at most 500 characters."},
		{"", "", "", "", "Enter a movie title or URL using at most 500 characters."},
	}
	for _, tc := range cases {
		_, _, kind, errMsg := validateSearch(tc.query, tc.region, tc.kind)
		if kind != tc.wantKind || errMsg != tc.wantErr {
			t.Errorf("validateSearch(%q, %q, %q) = kind %q, error %q; want %q, %q", tc.query, tc.region, tc.kind, kind, errMsg, tc.wantKind, tc.wantErr)
		}
	}
}

// sseEvents splits a server-sent event body into its events by name.
func sseEvents(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	events := map[string]json.RawMessage{}
	for _, chunk := range strings.Split(strings.TrimSpace(body), "\n\n") {
		name, data := "", ""
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(line, "event:") {
				name = strings.TrimSpace(line[6:])
			} else if strings.HasPrefix(line, "data:") {
				data += strings.TrimSpace(line[5:])
			}
		}
		if name != "" {
			events[name] = json.RawMessage(data)
		}
	}
	return events
}

// TestAudioSearchCarriesTheKindFromTheFormToTheIndex runs an audio search
// through the streamed route with a fake burst and proves the kind and the
// performer travel the whole way: into the parsed query the index is asked
// for, into the scout's options, into the report, the merged list and the
// observations the index would be given.
func TestAudioSearchCarriesTheKindFromTheFormToTheIndex(t *testing.T) {
	var gotOptions scout.SearchOptions
	search := func(_ context.Context, query, region string, options scout.SearchOptions) scout.SearchReport {
		gotOptions = options
		source := scout.VerifiedSource{
			CandidateSource: scout.CandidateSource{
				ID: "ia-audio-kind-of-blue-1959", Kind: lens.TypeAudio, Title: "Kind of Blue", Creator: "Miles Davis", URL: "https://archive.org/details/kind-of-blue-1959",
				DirectStreamURL: "https://archive.org/download/kind-of-blue-1959/01.mp3", Provider: "Internet Archive", SourceType: "Archive audio", Duration: "2744", AccessType: "free", ReleaseYear: 1959,
			},
			Verified: true, VerificationStatus: "media_reachable", StreamFormat: "Direct MP3", DetectedManifests: []string{}, CheckedAt: "2026-10-19T20:00:00Z",
			AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: region, CheckedAt: "2026-10-19T20:00:00Z", Method: "http_media_probe"},
		}
		return scout.SearchReport{Query: query, Kind: options.Kind, Region: region, Sources: []scout.VerifiedSource{source}, VerifiedSources: []scout.VerifiedSource{source}, PrunedSources: []scout.VerifiedSource{}, Providers: []scout.ProviderStatus{{Name: "Internet Archive audio", Status: "ok"}}}
	}
	srv, _ := newTestConsole(t, search)
	handler := srv.Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/scout/search/stream?query=Kind+of+Blue+by+Miles+Davis&kind=audio", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("stream returned %d: %s", response.Code, response.Body.String())
	}
	if gotOptions.Kind != lens.TypeAudio {
		t.Fatalf("the burst must be asked for audio, got %+v", gotOptions)
	}
	events := sseEvents(t, response.Body.String())
	var known knownEvent
	if err := json.Unmarshal(events["known"], &known); err != nil {
		t.Fatalf("known event: %v %s", err, events["known"])
	}
	if known.Query.Type != lens.TypeAudio || known.Query.Title != "Kind of Blue" || known.Query.Creator != "Miles Davis" || known.Query.CreatorKey != "miles davis" {
		t.Fatalf("the index is asked for the recording by title and performer: %+v", known.Query)
	}
	var report struct {
		Kind    string `json:"kind"`
		Sources []struct {
			Kind    string `json:"kind"`
			Creator string `json:"creator"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(events["report"], &report); err != nil || report.Kind != lens.TypeAudio || len(report.Sources) != 1 || report.Sources[0].Kind != lens.TypeAudio || report.Sources[0].Creator != "Miles Davis" {
		t.Fatalf("the report carries the kind and the performer: %v %s", err, events["report"])
	}
	var merged mergedEvent
	if err := json.Unmarshal(events["merged"], &merged); err != nil || len(merged.Locations) != 1 {
		t.Fatalf("merged event: %v %s", err, events["merged"])
	}
	if m := merged.Locations[0]; m.Creator != "Miles Davis" || m.Level != lens.LevelIdentified || m.Relevance != 1 || m.Origin != "fresh" {
		t.Fatalf("a reachable recording by the asked performer is identified and fully relevant: %+v", m)
	}

	observations := observationsFromReport(search(context.Background(), "Kind of Blue by Miles Davis", "ALL", scout.SearchOptions{Kind: lens.TypeAudio}))
	if len(observations) != 1 || observations[0].Kind != lens.TypeAudio || observations[0].Creator != "Miles Davis" || observations[0].Duration != "2744" {
		t.Fatalf("observations carry the kind, performer and duration to Atlas: %+v", observations)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/scout/search/stream?query=Sintel&kind=image", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Choose movies or audio.") {
		t.Fatalf("an image search is refused until its slice lands: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/scout/search", strings.NewReader(`{"query":"Kind of Blue by Miles Davis","kind":"audio"}`)))
	if response.Code != http.StatusOK || gotOptions.Kind != lens.TypeAudio || !strings.Contains(response.Body.String(), `"kind":"audio"`) {
		t.Fatalf("the JSON route takes the kind too: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/scout/search", strings.NewReader(`{"query":"Sintel"}`)))
	if response.Code != http.StatusOK || gotOptions.Kind != lens.TypeVideo || !strings.Contains(response.Body.String(), `"kind":"video"`) {
		t.Fatalf("a search without a kind is a video search: %d %s", response.Code, response.Body.String())
	}
}
