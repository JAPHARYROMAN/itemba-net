package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/atlas"
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
		{"Sintel", "", "image", lens.TypeImage, ""},
		{"", "", "image", "", "Enter what the picture shows, with the artist if known, using at most 500 characters."},
		{"Sintel", "", "document", "", "Choose movies, audio or pictures."},
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

	// A confirmed audio sample puts the recording at rendered, in the merged
	// list and in the evidence Atlas is given.
	sampled := search(context.Background(), "Kind of Blue by Miles Davis", "ALL", scout.SearchOptions{Kind: lens.TypeAudio})
	sampled.Sources[0].PlaybackCheck = &scout.PlaybackCheck{Status: "started", PlaybackConfirmed: true, Method: "isolated_audio_sample", SecondsAdvanced: 1.4, DurationSeconds: 562.4, Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T00:00:00Z", Detail: "A muted beginning sample decoded audio and advanced at normal speed."}
	merged = mergeLocations(lens.ParseAs("Kind of Blue by Miles Davis", lens.TypeAudio), nil, sampled, nil)
	if len(merged.Locations) != 1 || merged.Locations[0].Level != lens.LevelRendered || merged.Locations[0].LevelName != "rendered" {
		t.Fatalf("a started audio sample is rendered in the merged list: %+v", merged.Locations)
	}
	evidence := observationsFromReport(sampled)[0].Evidence
	rendered := false
	for _, e := range evidence {
		if e.Method == "isolated_audio_sample" && e.Level == lens.LevelRendered && e.Status == "started" {
			rendered = true
		}
	}
	if !rendered {
		t.Fatalf("a started audio sample is rendered evidence for Atlas: %+v", evidence)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/scout/search/stream?query=Sintel&kind=document", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Choose movies, audio or pictures.") {
		t.Fatalf("a document search is refused until its slice lands: %d %s", response.Code, response.Body.String())
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

// TestFingerprintsTravelFromTheSampleToTheIndexAndTheRankedList proves the
// bridge: a sample's fingerprint reaches the index with the length of the
// file it was taken from and the time it was taken, the sample's evidence
// says how the fingerprint went, and the ranked list carries the index's
// verdict on a fresh location, the search's own when the index gave none,
// and a known location's stored verdict.
func TestFingerprintsTravelFromTheSampleToTheIndexAndTheRankedList(t *testing.T) {
	data, err := os.ReadFile("../lens/testdata/fingerprints.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Recordings []struct {
			Name        string           `json:"name"`
			Fingerprint lens.Fingerprint `json:"fingerprint"`
		} `json:"recordings"`
	}
	if err := json.Unmarshal(data, &fx); err != nil || len(fx.Recordings) == 0 {
		t.Fatalf("fixture set: %v", err)
	}
	fp := fx.Recordings[0].Fingerprint
	album, reissue := "https://archive.org/details/kind-of-blue-1959", "https://archive.org/details/kind-of-blue-reissue"
	source := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{ID: "ia-audio-kind-of-blue-1959", Kind: lens.TypeAudio, Title: "Kind of Blue", Creator: "Miles Davis", URL: album, DirectStreamURL: "https://archive.org/download/kind-of-blue-1959/01.mp3", Provider: "Internet Archive", SourceType: "Archive audio", Duration: "2744", AccessType: "free"},
		Verified:        true, VerificationStatus: "media_reachable", CheckedAt: "2026-10-20T00:00:00Z",
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T00:00:00Z", Method: "http_media_probe"},
		PlaybackCheck:     &scout.PlaybackCheck{Status: "started", PlaybackConfirmed: true, Method: "isolated_audio_sample", SecondsAdvanced: 1.2, DurationSeconds: 562.4, Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Fingerprint: &fp, FingerprintStatus: "computed"},
		Identity:          &lens.IdentityMatch{Method: lens.MethodFingerprint, Verdict: lens.VerdictSame, Effect: lens.EffectJoined, PeerURL: reissue, Distance: 0.12, Detail: "the search's own comparison"},
	}
	report := scout.SearchReport{Query: "Kind of Blue by Miles Davis", Kind: lens.TypeAudio, Sources: []scout.VerifiedSource{source}}

	observations := observationsFromReport(report)
	if len(observations) != 1 || observations[0].Fingerprint == nil {
		t.Fatalf("the fingerprint reaches the listing: %+v", observations)
	}
	heard := observations[0].Fingerprint
	if heard.MediaSeconds != 562.4 || !heard.ObservedAt.Equal(time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)) || heard.Fingerprint.Frames != fp.Frames || string(heard.Fingerprint.Data) != string(fp.Data) {
		t.Fatalf("the fingerprint carries the sampled file's length and its time: %+v", heard)
	}
	if evidence := observations[0].Evidence; len(evidence) != 2 || evidence[1].Artefacts["fingerprintStatus"] != "computed" || evidence[1].Level != lens.LevelRendered {
		t.Fatalf("the sample's evidence says how the fingerprint went: %+v", evidence)
	}

	q := lens.ParseAs("Kind of Blue by Miles Davis", lens.TypeAudio)
	merged := mergeLocations(q, nil, report, nil)
	if len(merged.Locations) != 1 || merged.Locations[0].Identity == nil || merged.Locations[0].Identity.Detail != "the search's own comparison" {
		t.Fatalf("without the index's verdict the search's own stands: %+v", merged.Locations)
	}
	recorded := &atlas.RecordSummary{Fingerprints: 1, Identities: map[string]lens.IdentityMatch{album: {Method: lens.MethodFingerprint, Verdict: lens.VerdictDifferent, Effect: lens.EffectKeptApart, PeerURL: reissue, Distance: 0.44, Detail: "the index's verdict"}}}
	known := []atlas.KnownLocation{{
		Object: atlas.Object{ID: 7, Type: lens.TypeAudio, Title: "Kind of Blue", TitleKey: "kind of blue", Creator: "Miles Davis", CreatorKey: "miles davis"},
		Host:   atlas.Host{ID: 1, Hostname: "archive.org"}, Location: atlas.Location{ID: 3, ObjectID: 7, HostID: 1, URL: reissue, AccessType: "free", LastSeen: time.Now()},
		EffectiveLevel: lens.LevelRendered, LevelName: "rendered", Relevance: 1,
		Identity: &lens.IdentityMatch{Method: lens.MethodFingerprint, Verdict: lens.VerdictSame, Effect: lens.EffectConfirmed, PeerURL: album, Distance: 0.1, Detail: "stored with the fingerprint"},
	}}
	merged = mergeLocations(q, known, report, recorded)
	byURL := map[string]mergedLocation{}
	for _, m := range merged.Locations {
		byURL[m.URL] = m
	}
	if m := byURL[album]; m.Identity == nil || m.Identity.Effect != lens.EffectKeptApart || m.Identity.Detail != "the index's verdict" {
		t.Fatalf("the index's verdict replaces the search's on a fresh location: %+v", m.Identity)
	}
	if m := byURL[reissue]; m.Identity == nil || m.Identity.Effect != lens.EffectConfirmed || m.Origin != "index" {
		t.Fatalf("a known location carries its stored verdict: %+v", m)
	}

	// A movie sample has no fingerprint and no verdict.
	movie := source
	movie.Kind, movie.PlaybackCheck.Fingerprint, movie.PlaybackCheck.FingerprintStatus, movie.Identity = lens.TypeVideo, nil, "", nil
	if observations := observationsFromReport(scout.SearchReport{Sources: []scout.VerifiedSource{movie}}); observations[0].Fingerprint != nil || observations[0].Evidence[1].Artefacts["fingerprintStatus"] != nil {
		t.Fatalf("a movie sample sends nothing to the library: %+v", observations[0])
	}
}
