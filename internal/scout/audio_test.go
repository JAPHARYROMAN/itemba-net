package scout

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// The search answer is the Archive's own to an audio search on 19 October
// 2026, trimmed; the metadata answers follow the item metadata shape the
// video resolver already reads.
const archiveAudioSearchAnswer = `{"response":{"numFound":3,"docs":[
	{"collection":["etree"],"creator":"Miles Davis","date":"1959-08-17T00:00:00Z","identifier":"kind-of-blue-1959","title":"Kind of Blue"},
	{"collection":["radioopensource","podcasts"],"creator":"Open Source Media","identifier":"miles-davis-open-source","title":"Miles Davis' Kind of Blue","year":2014},
	{"collection":["radiofreecrockett"],"creator":"Radio FREE Crockett","identifier":"kindofblue_202103","title":"Kind of Blue","year":2021}
]}}`

var archiveAudioMetadata = map[string]string{
	"kind-of-blue-1959": `{"metadata":{"identifier":"kind-of-blue-1959","mediatype":"audio","title":"Kind of Blue","creator":"Miles Davis","year":"1959"},"files":[
		{"name":"01 So What.mp3","format":"VBR MP3","length":"562.4"},
		{"name":"01 So What.flac","format":"Flac","length":"562.4"},
		{"name":"02 Freddie Freeloader.mp3","format":"VBR MP3","length":"589"},
		{"name":"kind-of-blue-1959_meta.xml","format":"Metadata"}]}`,
	"miles-davis-open-source": `{"metadata":{"identifier":"miles-davis-open-source","mediatype":"audio","title":"Miles Davis' Kind of Blue","creator":"Open Source Media","date":"2014-06-18"},"files":[
		{"name":"episode.mp3","format":"VBR MP3","length":"3600"}]}`,
}

// audioFixtureScout answers for archive.org only: the audio search, the two
// items' metadata, their first files (one ID3-tagged, one a bare MP3 frame)
// and the third item's page. Every other host, the video catalogs included,
// is an error, which proves an audio search never reaches them.
func audioFixtureScout(t *testing.T) (*Scout, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, req.URL.Host+req.URL.Path)
		mu.Unlock()
		if req.URL.Host != "archive.org" {
			return nil, fmt.Errorf("unexpected host for an audio search: %s", req.URL)
		}
		switch {
		case req.URL.Path == "/advancedsearch.php":
			if q := req.URL.Query().Get("q"); q != `title:("Kind of Blue") AND mediatype:audio` {
				t.Errorf("unexpected audio query: %s", q)
			}
			return searchFixtureResponse(200, "application/json", archiveAudioSearchAnswer), nil
		case strings.HasPrefix(req.URL.Path, "/metadata/"):
			if body, ok := archiveAudioMetadata[strings.TrimPrefix(req.URL.Path, "/metadata/")]; ok {
				return searchFixtureResponse(200, "application/json", body), nil
			}
			return searchFixtureResponse(404, "application/json", `{}`), nil
		case req.URL.Path == "/download/kind-of-blue-1959/01 So What.mp3":
			return searchFixtureResponse(206, "audio/mpeg", "ID3\x04\x00\x00\x00\x00\x00\x00"+strings.Repeat("\x00", 64)), nil
		case req.URL.Path == "/download/miles-davis-open-source/episode.mp3":
			return searchFixtureResponse(206, "application/octet-stream", "\xff\xfb\x90\x64"+strings.Repeat("\x00", 64)), nil
		case req.URL.Path == "/details/kindofblue_202103":
			return searchFixtureResponse(200, "text/html; charset=utf-8", `<html><head><title>Kind of Blue : Radio FREE Crockett : Free Download, Borrow, and Streaming : Internet Archive</title></head><body><h1>Kind of Blue</h1><p>A radio hour on the album Kind of Blue, recorded for Radio FREE Crockett and shared on the Internet Archive for anyone to hear.</p></body></html>`), nil
		}
		return searchFixtureResponse(404, "text/plain", "not here"), nil
	})
	s.UseDrivers(testDrivers(t))
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func TestAudioSearchListsRecordingsByTheirTags(t *testing.T) {
	s, requests := audioFixtureScout(t)
	report := s.SearchWithOptions(context.Background(), "Kind of Blue", "ALL", SearchOptions{Kind: lens.TypeAudio, FullLengthOnly: true})
	if report.Kind != lens.TypeAudio || report.FullLengthOnly {
		t.Fatalf("an audio report names its kind and ignores the movie length preference: %+v", report)
	}
	for _, request := range requests() {
		// The check location is observed as for any search; nothing else
		// outside the audio driver's host may be asked.
		if !strings.HasPrefix(request, "archive.org/") && request != "www.cloudflare.com/cdn-cgi/trace" {
			t.Fatalf("an audio search reached %s; only the audio drivers may be searched", request)
		}
	}
	for _, provider := range report.Providers {
		if provider.Name != "Internet Archive audio" || provider.Status != "ok" {
			t.Fatalf("only the audio driver reports, and it answered: %+v", report.Providers)
		}
	}
	if len(report.Sources) != 3 || report.ListedCount != 3 || len(report.PrunedSources) != 0 {
		t.Fatalf("three listings answer the title; none is pruned for length: %+v", report)
	}
	first := report.Sources[0]
	if first.ID != "ia-audio-kind-of-blue-1959" || first.Kind != lens.TypeAudio || first.Creator != "Miles Davis" || first.ReleaseYear != 1959 || first.RelevanceScore != 100 {
		t.Fatalf("the exact title by its performer ranks first with its tags: %+v", first.CandidateSource)
	}
	if first.DirectStreamURL != "https://archive.org/download/kind-of-blue-1959/01%20So%20What.mp3" || first.Duration != "1151.4" || first.AccessType != "free" || first.AccessBasis != "archive_metadata" || first.ListingOnly {
		t.Fatalf("the metadata names the first MP3 track and the summed length of the MP3 tracks only: %+v", first.CandidateSource)
	}
	if !first.Verified || first.VerificationStatus != "media_reachable" || first.StreamFormat != "Direct MP3" || first.AvailabilityCheck == nil || first.AvailabilityCheck.Status != "media_reachable" || first.AvailabilityCheck.Method != "http_media_probe" {
		t.Fatalf("an ID3-tagged file answers the probe as audio: %+v %+v", first, first.AvailabilityCheck)
	}
	if !strings.Contains(first.Description, "2 tracks") || first.PlaybackCheck != nil {
		t.Fatalf("the description counts the tracks and no playback sample runs yet: %q %+v", first.Description, first.PlaybackCheck)
	}
	second := report.Sources[1]
	if second.ID != "ia-audio-miles-davis-open-source" || second.Creator != "Open Source Media" || second.Duration != "3600" || second.VerificationStatus != "media_reachable" || second.RelevanceScore != 80 {
		t.Fatalf("a bare MP3 frame answers the probe and a title containing the query scores lower: status %q (%s), relevance %d: %+v", second.VerificationStatus, second.PrunedReason, second.RelevanceScore, second.CandidateSource)
	}
	third := report.Sources[2]
	if third.ID != "ia-audio-kindofblue_202103" || third.DirectStreamURL != "" || !third.ListingOnly || third.VerificationStatus != "page_reachable" || third.AvailabilityCheck == nil || third.AvailabilityCheck.Status != "page_reachable" {
		t.Fatalf("an item without readable metadata stays a listing whose page is checked: %+v %+v", third.CandidateSource, third.AvailabilityCheck)
	}
	if detail := third.AvailabilityCheck.Detail; strings.Contains(strings.ToLower(detail), "movie") {
		t.Fatalf("the page check speaks of a recording, not a movie: %q", detail)
	}
	if report.AvailabilityChecks == nil || report.AvailabilityChecks.CheckedCount != 3 {
		t.Fatalf("every source was checked: %+v", report.AvailabilityChecks)
	}
}

func TestAudioSearchKeepsOnlyTheNamedPerformer(t *testing.T) {
	s, _ := audioFixtureScout(t)
	report := s.SearchWithOptions(context.Background(), "Kind of Blue by Miles Davis", "ALL", SearchOptions{Kind: lens.TypeAudio})
	if len(report.Sources) != 1 || report.Sources[0].ID != "ia-audio-kind-of-blue-1959" {
		t.Fatalf("a performer in the query keeps other performers' recordings out: %+v", report.Sources)
	}
	if report.Query != "Kind of Blue by Miles Davis" || report.Kind != lens.TypeAudio {
		t.Fatalf("the report echoes the query and its kind: %+v", report)
	}
	if empty := s.SearchWithOptions(context.Background(), "https://archive.org/details/kind-of-blue-1959", "ALL", SearchOptions{Kind: lens.TypeAudio}); len(empty.Sources) != 0 || len(empty.Providers) != 1 || empty.Providers[0].Status != "empty" {
		t.Fatalf("an audio URL is not inspected yet and says so: %+v", empty)
	}
}

func TestProbeMediaRecognisesAudioContainers(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"id3 mp3":     {"ID3\x04\x00\x00\x00\x00\x00\x00" + strings.Repeat("\x00", 16), true},
		"bare mp3":    {"\xff\xfb\x90\x64" + strings.Repeat("\x00", 16), true},
		"flac":        {"fLaC\x00\x00\x00\x22" + strings.Repeat("\x00", 16), true},
		"wave":        {"RIFF\x24\x00\x00\x00WAVEfmt " + strings.Repeat("\x00", 16), true},
		"ogg":         {"OggS\x00\x02" + strings.Repeat("\x00", 16), true},
		"html":        {"<!doctype html><html><body>Not audio</body></html>", false},
		"text":        {"Just some text that is not a recording at all.", false},
		"ff sync bad": {"\xff\xe8\x00\x00" + strings.Repeat("\x00", 16), false},
	}
	for name, tc := range cases {
		sniffed := http.DetectContentType([]byte(tc.body))
		if got := audioBytes([]byte(tc.body), sniffed) || len(tc.body) > 4 && tc.body[:4] == "OggS"; got != tc.want {
			t.Errorf("%s: recognised %v, want %v (sniffed %s)", name, got, tc.want, sniffed)
		}
	}
}
