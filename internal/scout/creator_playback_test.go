package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/shield"
)

const creatorFixtureAuthorization = `<main><p>Watch on the French <a href="https://www.catsuka.com/player/Seder_Masochism">Catsuka Player</a>.</p></main>`
const creatorFixturePlayer = `<title>:: CATSUKA PLAYER :: Seder-Masochism (Nina Paley) VOSTF 720p</title><main><p>Animated feature film by Nina Paley.</p><video id="div_video" poster="https://www.catsuka.com/videos/player/vignettes/Seder_Masochism.jpg"><source src="https://www.catsuka.com/videos/files/Seder_Masochism.mp4" type="video/mp4"></video></main>`

func creatorFixtureSource() CandidateSource {
	return CandidateSource{Title: "Seder-Masochism", ReleaseYear: 2018, URL: creatorSederPage, AccessType: "free", ListingOnly: true}
}

func creatorFixtureClient(t *testing.T, creator, player string, calls *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("creator metadata check forwarded account state")
		}
		var body string
		switch req.URL.String() {
		case creatorSederPage:
			body = creator
		case creatorSederPlayer:
			body = player
		default:
			t.Fatalf("metadata check requested unbound movie media or another site: %s", req.URL)
		}
		response := searchFixtureResponse(http.StatusOK, "text/html", body)
		response.Request = req
		return response, nil
	})}
}

func TestCreatorMovieTargetsOnlyAuthorizedFeaturePages(t *testing.T) {
	for _, raw := range []string{creatorSederPage, "https://sedermasochism.com/", creatorSederPlayer} {
		source := creatorFixtureSource()
		source.URL = raw
		if !creatorMovieTarget(source) {
			t.Errorf("known creator feature target rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"https://www.catsuka.com/player/Sita_Sings_The_Blues", "https://www.catsuka.com/player/Seder_Masochism_trailer",
		"https://sedermasochism.com/trailer/", "https://sedermasochism.com.evil.net/download/", "https://www.catsuka.com.evil.net/player/Seder_Masochism",
		"http://127.0.0.1/player/Seder_Masochism", creatorSederPlayer + "?video=other", "https://user:pass@www.catsuka.com/player/Seder_Masochism",
	} {
		source := creatorFixtureSource()
		source.URL = raw
		if creatorMovieTarget(source) {
			t.Errorf("unapproved creator/player target accepted: %s", raw)
		}
	}
	source := creatorFixtureSource()
	source.AccessType = "subscription"
	if creatorMovieTarget(source) {
		t.Fatal("paid source accepted for a free creator sample")
	}
	if got := creatorMovieCandidates("Seder-Masochism 2018"); len(got) != 1 || got[0].ReleaseYear != 2018 || got[0].Duration != "78:00" {
		t.Fatal("creator catalog did not preserve its primary feature metadata")
	}
	if len(creatorMovieCandidates("An unrelated movie 2026")) != 0 || len(creatorMovieCandidates("Seder-Masochism 2026")) != 0 {
		t.Fatal("creator catalog inserted a feature into an unrelated search")
	}
}

func TestCreatorPlaybackRequiresCreatorAuthorizationAndOneExplicitMainVideo(t *testing.T) {
	calls := 0
	client := creatorFixtureClient(t, creatorFixtureAuthorization, creatorFixturePlayer, &calls)
	source, check := inspectCreatorPlayback(context.Background(), client, creatorFixtureSource())
	if check != nil || calls != 2 || !source.providerContentBound || source.providerSSAI || source.providerStreamURL != creatorSederMedia || source.AccessBasis != "creator_movie_metadata" || source.URL != creatorSederPlayer || source.AccessEvidenceURL != creatorSederPage || source.DirectStreamURL != "" || len(source.KnownManifests) != 0 {
		t.Fatalf("explicit creator/publisher main feature was not privately bound: %+v, %+v", source, check)
	}
	page, _ := url.Parse(source.URL)
	media, _ := url.Parse(source.providerStreamURL)
	evidence, _ := url.Parse(source.AccessEvidenceURL)
	if !creatorResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("verified creator/publisher pair was rejected")
	}
	data, _ := json.Marshal(source)
	if strings.Contains(string(data), creatorSederMedia) {
		t.Fatal("the privately bound movie stream leaked into a listing before native verification")
	}
	media, _ = url.Parse("https://www.catsuka.com/videos/files/trailer.mp4")
	if creatorResolvedPlaybackPair(source, page, media, evidence) {
		t.Fatal("a different publisher movie escaped the identity binding")
	}
}

func TestCreatorPlaybackDoesNotPromoteTrailersHiddenLinksOrUnrelatedMedia(t *testing.T) {
	for _, tc := range []struct {
		name, creator, player string
		calls                 int
	}{
		{"missing authorization", `<main>No published movie link.</main>`, creatorFixturePlayer, 1},
		{"hidden authorization", `<div hidden>` + creatorFixtureAuthorization + `</div>`, creatorFixturePlayer, 1},
		{"script authorization", `<script>` + creatorFixtureAuthorization + `</script>`, creatorFixturePlayer, 1},
		{"trailer title", creatorFixtureAuthorization, strings.Replace(creatorFixturePlayer, "Seder-Masochism (Nina Paley)", "Seder-Masochism Trailer (Nina Paley)", 1), 2},
		{"different movie URI", creatorFixtureAuthorization, strings.Replace(creatorFixturePlayer, creatorSederMedia, "https://www.catsuka.com/videos/files/trailer.mp4", 1), 2},
		{"outside publisher", creatorFixtureAuthorization, strings.Replace(creatorFixturePlayer, creatorSederMedia, "https://unrelated.example.com/movie.mp4", 1), 2},
		{"two movie elements", creatorFixtureAuthorization, creatorFixturePlayer + `<video id="second"><source src="` + creatorSederMedia + `"></video>`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			source, check := inspectCreatorPlayback(context.Background(), creatorFixtureClient(t, tc.creator, tc.player, &calls), creatorFixtureSource())
			if check == nil || check.Reason != "content_unconfirmed" || check.PlaybackConfirmed || check.Method != "provider_playback_preflight" || source.providerContentBound || source.providerStreamURL != "" || calls != tc.calls {
				t.Fatalf("unclear/unauthorized video became a movie start: %+v, %+v, calls=%d", source, check, calls)
			}
		})
	}
}

func TestCreatorPlaybackRejectsDifferentYearBeforeAnyRequest(t *testing.T) {
	source := creatorFixtureSource()
	source.ReleaseYear = 2026
	client := &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("a different movie year triggered creator metadata requests")
		return nil, fmt.Errorf("unexpected request")
	})}
	if _, check := inspectCreatorPlayback(context.Background(), client, source); check == nil || check.PlaybackConfirmed || check.Reason != "content_unconfirmed" {
		t.Fatal("a different edition received a playback claim")
	}
}

func TestCreatorMetadataRejectsRedirectToDifferentMovieOrPrivateHost(t *testing.T) {
	for _, location := range []string{"https://www.catsuka.com/player/another_movie", "http://127.0.0.1/private"} {
		calls := 0
		client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			response := searchFixtureResponse(http.StatusFound, "text/html", "")
			response.Header.Set("Location", location)
			response.Request = req
			return response, nil
		})}
		if _, _, err := fetchCreatorPlaybackDocument(context.Background(), client, creatorSederPlayer); err == nil || calls != 1 {
			t.Fatal("creator metadata followed an unrelated/private redirect")
		}
	}
}

func TestLiveCreatorSederProtectedPlayback(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_CREATOR_PLAYBACK") != "1" {
		t.Skip("set ITEMBA_LIVE_CREATOR_PLAYBACK=1 for the bounded creator-authorized full feature sample")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := newMovieAvailabilityClient()
	sh := shield.New()
	scout := &Scout{availabilityHTTPClient: client, shield: sh}
	country, _ := scout.availabilityCheckCountry(ctx)
	source, preflight := inspectCreatorPlayback(ctx, client, creatorFixtureSource())
	if preflight != nil {
		data, _ := json.Marshal(preflight)
		t.Fatalf("creator/publisher authorization could not be confirmed: %s", data)
	}
	internal := source
	internal.DirectStreamURL, internal.ListingOnly = source.providerStreamURL, false
	verified := verifyFastHTTPWithClient(ctx, sh, internal, client)
	if !verified.Verified {
		t.Fatalf("published movie media did not return a recognizable response: %s", verified.PrunedReason)
	}
	verified.CandidateSource = source
	verified.ListingOnly = false
	verified.DetectedManifests = nil
	check := scout.probeFreeMoviePlayback(ctx, verified, "ALL", country)
	encoded, _ := json.MarshalIndent(check, "", "  ")
	_ = os.WriteFile("../../.itemba-runtime/creator-catsuka-playback.json", encoded, 0600)
	t.Logf("creator-authorized Catsuka protected feature sample: %s", encoded)
	if !playbackStarted(VerifiedSource{CandidateSource: source, PlaybackCheck: check}) || check.DurationSeconds < 40*60 {
		t.Fatal("the public creator feature did not prove a decoded full-length movie sample")
	}
}
