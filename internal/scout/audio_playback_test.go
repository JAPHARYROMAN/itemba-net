package scout

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

func audioPlaybackFixture() VerifiedSource {
	return VerifiedSource{
		CandidateSource: CandidateSource{
			ID: "ia-audio-kind-of-blue-1959", Kind: lens.TypeAudio, Title: "Kind of Blue", Creator: "Miles Davis", URL: "https://archive.org/details/kind-of-blue-1959",
			DirectStreamURL: "https://archive.org/download/kind-of-blue-1959/01%20So%20What.mp3", Provider: "Internet Archive", SourceType: "Archive audio",
			AccessType: "free", AccessBasis: "archive_metadata", AccessEvidenceURL: "https://archive.org/metadata/kind-of-blue-1959", Duration: "1151.4",
			Tracks: []AudioTrack{
				{Title: "01 So What", URL: "https://archive.org/download/kind-of-blue-1959/01%20So%20What.mp3", DurationSeconds: 562.4, Format: "MP3"},
				{Title: "02 Freddie Freeloader", URL: "https://archive.org/download/kind-of-blue-1959/02%20Freddie%20Freeloader.mp3", DurationSeconds: 589, Format: "MP3"},
			},
		},
		Verified: true, VerificationStatus: "media_reachable", StreamFormat: "Direct MP3",
	}
}

func audioStateFixture(origin string) playbackSnapshot {
	return playbackSnapshot{CurrentTime: .2, ReadyState: 4, PlaybackRate: 1, Duration: 562.4, Source: origin + "/media", Protocol: "native"}
}

func TestEligibleAudioPlaybackRequiresAnArchiveRecordingPairedWithItsFile(t *testing.T) {
	base := audioPlaybackFixture()
	if !eligibleAudioPlayback(base) {
		t.Fatal("a free, reachable Archive recording with its own file was rejected")
	}
	if eligibleFreePlayback(base) {
		t.Fatal("a recording must never pass the movie rule, which judges frames")
	}
	mutations := map[string]func(*VerifiedSource){
		"video kind":     func(s *VerifiedSource) { s.Kind = lens.TypeVideo },
		"paid":           func(s *VerifiedSource) { s.AccessType = "unknown" },
		"listing":        func(s *VerifiedSource) { s.ListingOnly = true },
		"page only":      func(s *VerifiedSource) { s.VerificationStatus = "page_reachable" },
		"unverified":     func(s *VerifiedSource) { s.Verified = false },
		"no metadata":    func(s *VerifiedSource) { s.AccessBasis = "page_text" },
		"other item":     func(s *VerifiedSource) { s.DirectStreamURL = "https://archive.org/download/other-item/01.mp3" },
		"other host":     func(s *VerifiedSource) { s.DirectStreamURL = "https://cdn.example.net/kind-of-blue-1959/01.mp3" },
		"video file":     func(s *VerifiedSource) { s.DirectStreamURL = "https://archive.org/download/kind-of-blue-1959/01.mp4" },
		"other page":     func(s *VerifiedSource) { s.URL = "https://archive.org/details/another" },
		"evidence query": func(s *VerifiedSource) { s.AccessEvidenceURL = "https://archive.org/metadata/kind-of-blue-1959?x=1" },
		"archive lookalike": func(s *VerifiedSource) {
			s.AccessEvidenceURL = "https://archive.org.evil.net/metadata/kind-of-blue-1959"
		},
		"movie source type": func(s *VerifiedSource) { s.SourceType = "Archive video" },
		"private media":     func(s *VerifiedSource) { s.DirectStreamURL = "http://127.0.0.1/download/kind-of-blue-1959/01.mp3" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			source := base
			mutate(&source)
			if eligibleAudioPlayback(source) {
				t.Fatal("ineligible recording accepted")
			}
		})
	}
}

func TestAudioProgressNeedsTimeAtNormalSpeedButNoFrames(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	start := time.Now()
	progress := playbackProgress{audio: true}
	state := audioStateFixture(origin)
	for i := 0; i <= 5; i++ {
		state.CurrentTime = .2 + float64(i)*.2
		confirmed, detail := progress.observe(state, start.Add(time.Duration(i)*200*time.Millisecond), origin, 562.4)
		if detail != "" || confirmed != (i == 5) {
			t.Fatalf("sample %d: confirmed=%t detail=%q", i, confirmed, detail)
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*playbackSnapshot, int)
	}{
		{"frozen time", func(s *playbackSnapshot, i int) { s.CurrentTime = .2 }},
		{"paused", func(s *playbackSnapshot, i int) { s.Paused = true }},
		{"metadata only", func(s *playbackSnapshot, i int) { s.ReadyState = 1 }},
		{"rate trick", func(s *playbackSnapshot, i int) { s.PlaybackRate = 2 }},
		{"time jump", func(s *playbackSnapshot, i int) { s.CurrentTime = .2 + float64(i)*3 }},
		{"wrong source", func(s *playbackSnapshot, i int) { s.Source = "https://tracker.net/track.mp3" }},
		{"another file's length", func(s *playbackSnapshot, i int) { s.Duration = 30 }},
		{"not beginning", func(s *playbackSnapshot, i int) { s.CurrentTime += 30 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := playbackProgress{audio: true}
			for i := 0; i < 8; i++ {
				state := audioStateFixture(origin)
				state.CurrentTime += float64(i) * .2
				test.mutate(&state, i)
				if confirmed, _ := p.observe(state, start.Add(time.Duration(i)*200*time.Millisecond), origin, 562.4); confirmed {
					t.Fatal("unsupported evidence confirmed playback")
				}
			}
		})
	}
}

func TestAudioSamplerConfirmsAnAdvancingRecording(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	browser := &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: audioStateFixture(origin)}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	result := runFreePlaybackSample(ctx, browser, audioPlaybackFixture(), origin, &PlaybackCheck{Method: "isolated_audio_sample"})
	if !result.PlaybackConfirmed || result.Status != "started" || result.SecondsAdvanced < 1 || result.DurationSeconds != 562.4 || result.DecodedFrames != 0 || result.Width != 0 {
		t.Fatalf("an advancing recording was not confirmed: %+v", result)
	}
	if !strings.Contains(result.Detail, "decoded audio") {
		t.Fatalf("the detail speaks of audio: %q", result.Detail)
	}
	// The same evidence judged as video is refused: no frames, no picture.
	video := audioPlaybackFixture()
	video.Kind = lens.TypeVideo
	browser = &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: audioStateFixture(origin)}
	ctx, cancel = context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	if result := runFreePlaybackSample(ctx, browser, video, origin, &PlaybackCheck{}); result.PlaybackConfirmed {
		t.Fatalf("frameless evidence confirmed a video sample: %+v", result)
	}
}

func TestAudioHarnessServesAnAudioPageForARecording(t *testing.T) {
	source := audioPlaybackFixture()
	origin, closeHarness, _, err := startProtectedMoviePlaybackHarness(context.Background(), source, &http.Client{Transport: playbackHarnessTransport(func(*http.Request) (*http.Response, error) {
		return searchFixtureResponse(206, "audio/mpeg", "ID3\x04\x00\x00\x00\x00\x00\x00"), nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHarness()
	response, err := http.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	page, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(page), `<audio id="sample" autoplay muted preload="auto" src="/media">`) || strings.Contains(string(page), "<video") {
		t.Fatalf("a recording's harness page carries one muted audio element: %s", page)
	}
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "media-src 'self' blob:") {
		t.Fatalf("the audio page keeps the harness policy: %q", response.Header.Get("Content-Security-Policy"))
	}
}

func TestAudioSearchSamplesTheBestReachableRecording(t *testing.T) {
	s, _ := audioFixtureScout(t)
	var sampled []string
	s.audioProbe = func(_ context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
		sampled = append(sampled, source.ID)
		if !eligibleAudioPlayback(source) {
			t.Errorf("an ineligible recording was offered to the probe: %+v", source.CandidateSource)
		}
		if len(source.Tracks) == 0 || expectedAudioDuration(source) != source.Tracks[0].DurationSeconds {
			t.Errorf("the probe judges the first track by its own length: %+v", source.Tracks)
		}
		return &PlaybackCheck{Status: "started", PlaybackConfirmed: true, Method: "isolated_audio_sample", SecondsAdvanced: 1.2, DurationSeconds: source.Tracks[0].DurationSeconds, RequestedRegion: region, Country: country, CheckedAt: "2026-10-20T00:00:00Z", Detail: "A muted beginning sample decoded audio and advanced at normal speed."}
	}
	report := s.SearchWithOptions(context.Background(), "Kind of Blue", "ALL", SearchOptions{Kind: lens.TypeAudio})
	if len(sampled) != 2 {
		t.Fatalf("two reachable recordings are sampled, one per budget slot: %v", sampled)
	}
	first := report.Sources[0]
	if first.PlaybackCheck == nil || !audioPlaybackStarted(first) || first.PlaybackCheck.Method != "isolated_audio_sample" {
		t.Fatalf("a started sample leads the sources: %+v %+v", first.CandidateSource, first.PlaybackCheck)
	}
	if len(first.Tracks) != 2 || first.Tracks[0].Title != "01 So What" || first.Tracks[1].URL != "https://archive.org/download/kind-of-blue-1959/02%20Freddie%20Freeloader.mp3" || first.Tracks[1].DurationSeconds != 589 || first.Tracks[0].Format != "MP3" {
		t.Fatalf("the resolver carries the MP3 tracks in order with their lengths: %+v", first.Tracks)
	}
	if third := report.Sources[2]; third.PlaybackCheck != nil && third.PlaybackCheck.Method != "not_checked" {
		t.Fatalf("a listing without a file is not sampled: %+v", third.PlaybackCheck)
	}
	coverage := report.AvailabilityChecks
	if coverage == nil || coverage.PlaybackCheckedCount != 2 || coverage.PlaybackStartedCount != 2 || !strings.Contains(coverage.Detail, "free recordings") || strings.Contains(strings.ToLower(coverage.Detail), "movie") {
		t.Fatalf("the coverage counts audio samples in the recording's words: %+v", coverage)
	}
	if lens.LevelFromChecks(first.AvailabilityCheck.Status, first.PlaybackCheck.Status, first.PlaybackCheck.PlaybackConfirmed) != lens.LevelRendered {
		t.Fatal("a confirmed audio sample is rendered on the ladder")
	}
}
