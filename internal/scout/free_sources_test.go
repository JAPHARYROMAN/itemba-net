package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type freeSourceTransport func(*http.Request) (*http.Response, error)

func (f freeSourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func freeSourceJSONResponse(request *http.Request, payload any) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: request}
}

func archiveResolutionFixture(id string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"identifier": id, "title": "Night of the Living Dead", "mediatype": "movies", "year": "1968"},
		"files": []map[string]any{
			{"name": "Night_Of_The_Living_Dead_trailer.mp4", "length": "100", "size": "1000000"},
			{"name": "Night_Of_The_Living_Dead_HQ.mp4", "length": "5752", "size": "1000000000"},
			{"name": "Night_Of_The_Living_Dead_512kb.mp4", "length": "5752", "size": "300000000"},
		},
	}
}

func freeArchiveCandidate(id string) CandidateSource {
	return CandidateSource{ID: "web-archive", Title: "Night of the Living Dead", ReleaseYear: 1968, URL: "https://archive.org/details/" + id, AccessType: "unknown", Duration: "1:35:52", ListingOnly: true, SourceType: "Open Web"}
}

func TestFreeArchiveWebListingResolvesExactFullMovie(t *testing.T) {
	source := freeArchiveCandidate("night_movie")
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://archive.org/metadata/night_movie" || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("unexpected metadata request: %s", request.URL)
		}
		return freeSourceJSONResponse(request, archiveResolutionFixture("night_movie")), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
	if !ok || resolved.DirectStreamURL != "https://archive.org/download/night_movie/Night_Of_The_Living_Dead_512kb.mp4" || resolved.Duration != "5752" || resolved.ListingOnly || resolved.AccessType != "free" || resolved.AccessBasis != "archive_metadata" || resolved.AccessEvidenceURL != "https://archive.org/metadata/night_movie" {
		t.Fatalf("public full movie did not resolve: %#v", resolved)
	}
	verified := VerifiedSource{CandidateSource: resolved, Verified: true, VerificationStatus: "media_reachable"}
	if !eligibleFreePlayback(verified) {
		t.Fatal("resolved exact movie should be eligible after its separate media check")
	}
	verified.DirectStreamURL = "https://archive.org/download/another_movie/file.mp4"
	if eligibleFreePlayback(verified) {
		t.Fatal("a different Archive item's video must not pass eligibility")
	}
}

func TestFreeArchiveResolverRejectsRestrictedWrongMovieAndClips(t *testing.T) {
	tests := map[string]func(map[string]any){
		"different item":    func(v map[string]any) { v["metadata"].(map[string]any)["identifier"] = "other_movie" },
		"different title":   func(v map[string]any) { v["metadata"].(map[string]any)["title"] = "Dawn of the Dead" },
		"different release": func(v map[string]any) { v["metadata"].(map[string]any)["year"] = "1990" },
		"restricted":        func(v map[string]any) { v["metadata"].(map[string]any)["access-restricted-item"] = "true" },
		"dark":              func(v map[string]any) { v["is_dark"] = true },
		"not movie":         func(v map[string]any) { v["metadata"].(map[string]any)["mediatype"] = "audio" },
		"only trailer":      func(v map[string]any) { v["files"] = []map[string]any{{"name": "trailer.mp4", "length": "5752"}} },
		"only short clip":   func(v map[string]any) { v["files"] = []map[string]any{{"name": "movie.mp4", "length": "240"}} },
		"private file": func(v map[string]any) {
			v["files"] = []map[string]any{{"name": "movie.mp4", "length": "5752", "private": true}}
		},
		"path traversal": func(v map[string]any) {
			v["files"] = []map[string]any{{"name": "../another_movie/movie.mp4", "length": "5752"}}
		},
		"metadata trailer": func(v map[string]any) { v["metadata"].(map[string]any)["title"] = "Night of the Living Dead Trailer" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			payload := archiveResolutionFixture("night_movie")
			mutate(payload)
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return freeSourceJSONResponse(request, payload), nil
			})}
			source := freeArchiveCandidate("night_movie")
			resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
			if ok || resolved.DirectStreamURL != "" || resolved.AccessType != "unknown" || !resolved.ListingOnly {
				t.Fatalf("unsupported movie metadata changed listing: %#v", resolved)
			}
		})
	}
}

func TestFreeArchiveFullLengthPrecedesEncodingSizeForUnknownRuntime(t *testing.T) {
	for _, metadataRuntime := range []string{"", "01:35:52"} {
		t.Run(metadataRuntime, func(t *testing.T) {
			payload := archiveResolutionFixture("night_movie")
			payload["metadata"].(map[string]any)["runtime"] = metadataRuntime
			payload["files"] = []map[string]any{
				{"name": "movie_low.mp4", "length": "240", "size": "400000"},
				{"name": "movie_full.mp4", "length": "5752", "size": "400000000"},
			}
			source := freeArchiveCandidate("night_movie")
			source.Duration = ""
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return freeSourceJSONResponse(request, payload), nil
			})}
			resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
			if !ok || !strings.HasSuffix(resolved.DirectStreamURL, "/movie_full.mp4") || resolved.Duration != "5752" {
				t.Fatalf("unlabeled short clip displaced full movie: %#v", resolved)
			}
		})
	}
}

func TestFreeArchivePrefersDeclaredStreamingEncodingToGenericMP4(t *testing.T) {
	payload := archiveResolutionFixture("night_movie")
	payload["files"] = []map[string]any{
		{"name": "movie_generic.mp4", "format": "MPEG4", "length": "5752", "size": "300000000"},
		{"name": "movie_h264.mp4", "format": "h.264", "length": "5752", "size": "500000000"},
	}
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		return freeSourceJSONResponse(request, payload), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, freeArchiveCandidate("night_movie"))
	if !ok || !strings.HasSuffix(resolved.DirectStreamURL, "/movie_h264.mp4") {
		t.Fatalf("unspecified container displaced declared streaming encoding: %#v", resolved)
	}
}

const freePeerTubeFixtureID = "9c9de5e8-0a1e-484a-b099-e80766180a6d"

func peerTubeResolutionFixture() map[string]any {
	return map[string]any{
		"uuid": freePeerTubeFixtureID, "shortUUID": "2y84q2MQUMWPbiEcxNXMgC", "name": "Sintel", "duration": 888,
		"privacy": map[string]any{"id": 1},
		"files": []map[string]any{
			{"fileUrl": "https://media.movie-cdn.net/static/web-videos/sintel-720.mp4", "resolution": map[string]any{"id": 720}},
			{"fileUrl": "https://media.movie-cdn.net/static/web-videos/sintel-360.mp4", "resolution": map[string]any{"id": 360}},
		},
	}
}

func freePeerTubeCandidate() CandidateSource {
	return CandidateSource{ID: "pt-sepia-result", Title: "Sintel", URL: "https://films.peer-video.net/videos/watch/" + freePeerTubeFixtureID, SourceType: "Federated PeerTube", Provider: "PeerTube (films.peer-video.net)", AccessType: "free", Duration: "14:48"}
}

func TestFreePeerTubeResolutionSupportsPublicCDNAndShortURLs(t *testing.T) {
	for _, route := range []string{"/videos/watch/" + freePeerTubeFixtureID, "/videos/embed/" + freePeerTubeFixtureID, "/w/2y84q2MQUMWPbiEcxNXMgC"} {
		t.Run(route, func(t *testing.T) {
			source := freePeerTubeCandidate()
			source.URL = "https://films.peer-video.net" + route
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != "films.peer-video.net" || !strings.HasPrefix(request.URL.Path, "/api/v1/videos/") {
					t.Fatalf("request escaped video API: %s", request.URL)
				}
				return freeSourceJSONResponse(request, peerTubeResolutionFixture()), nil
			})}
			resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
			if !ok || resolved.DirectStreamURL != "https://media.movie-cdn.net/static/web-videos/sintel-360.mp4" || resolved.AccessBasis != "peertube_api" || resolved.ListingOnly || resolved.Duration != "888" {
				t.Fatalf("public PeerTube movie did not resolve: %#v", resolved)
			}
			verified := VerifiedSource{CandidateSource: resolved, Verified: true, VerificationStatus: "media_reachable"}
			if !eligibleFreePlayback(verified) {
				t.Fatal("metadata-associated public CDN source should be eligible")
			}
			verified.AccessEvidenceURL = "https://another.peer-video.net/api/v1/videos/" + freePeerTubeFixtureID
			if eligibleFreePlayback(verified) {
				t.Fatal("cross-instance metadata must not authorize a media source")
			}
		})
	}
}

func TestFreePeerTubeResolutionSupportsHLSWithoutDownloadingProviderCode(t *testing.T) {
	payload := peerTubeResolutionFixture()
	payload["files"] = []any{}
	payload["streamingPlaylists"] = []map[string]any{{"playlistUrl": "https://media.movie-cdn.net/static/streaming-playlists/hls/sintel/master.m3u8"}}
	requests := 0
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return freeSourceJSONResponse(request, payload), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, freePeerTubeCandidate())
	if !ok || !strings.HasSuffix(resolved.DirectStreamURL, "master.m3u8") || requests != 1 {
		t.Fatalf("expected one metadata request and HLS result: %#v, requests %d", resolved, requests)
	}
}

func TestFreePeerTubeResolverRejectsPrivateLiveWrongVideoAndUnsafeMedia(t *testing.T) {
	tests := map[string]func(map[string]any){
		"private":     func(v map[string]any) { v["privacy"] = map[string]any{"id": 3} },
		"password":    func(v map[string]any) { v["passwordProtected"] = true },
		"live":        func(v map[string]any) { v["isLive"] = true },
		"wrong uuid":  func(v map[string]any) { v["uuid"] = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" },
		"wrong title": func(v map[string]any) { v["name"] = "Sintel Trailer" },
		"short clip":  func(v map[string]any) { v["duration"] = 240 },
		"no video": func(v map[string]any) {
			v["files"] = []map[string]any{{"fileUrl": "https://media.movie-cdn.net/intro.mp3", "resolution": map[string]any{"id": 0}}}
		},
		"private media": func(v map[string]any) {
			v["files"] = []map[string]any{{"fileUrl": "http://127.0.0.1/movie.mp4", "resolution": map[string]any{"id": 360}}}
		},
		"credential media": func(v map[string]any) {
			v["files"] = []map[string]any{{"fileUrl": "https://user:pass@media.movie-cdn.net/movie.mp4", "resolution": map[string]any{"id": 360}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			payload := peerTubeResolutionFixture()
			mutate(payload)
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return freeSourceJSONResponse(request, payload), nil
			})}
			if resolved, ok := resolveFreeMovieSource(context.Background(), client, freePeerTubeCandidate()); ok || resolved.DirectStreamURL != "" {
				t.Fatalf("unsupported PeerTube metadata accepted: %#v", resolved)
			}
		})
	}
}

func TestPublicDomainMoviesUsesOnlyPrimaryDownloadAssociation(t *testing.T) {
	source := freeArchiveCandidate("night_movie")
	source.URL, source.ID = "https://publicdomainmovie.net/movie/night-of-the-living-dead", "web-pdm"
	markup := `<nav><a href="/movie.php?id=wrong_nav&amp;type=.mp4">Download</a></nav><script>"/movie.php?id=wrong_script&type=.mp4"</script><a href="/movie.php?id=wrong_recommendation&amp;type=.mp4">Other movie</a><div class="field field-name-download"><a href="/movie.php?id=night_movie&amp;type=.mp4">Download Movie</a></div>`
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "publicdomainmovie.net" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(markup)), Header: http.Header{}, Request: request}, nil
		}
		if request.URL.String() != "https://archive.org/metadata/night_movie" {
			t.Fatalf("wrong embedded movie association: %s", request.URL)
		}
		return freeSourceJSONResponse(request, archiveResolutionFixture("night_movie")), nil
	})}
	resolved, ok := resolveFreeMovieSource(context.Background(), client, source)
	if !ok || resolved.URL != source.URL || resolved.Provider != "Public Domain Movies" || resolved.SourceType != "Public Domain" || !strings.HasPrefix(resolved.DirectStreamURL, "https://archive.org/download/night_movie/") {
		t.Fatalf("public movie page did not resolve: %#v", resolved)
	}
	if !eligibleFreePlayback(VerifiedSource{CandidateSource: resolved, Verified: true, VerificationStatus: "media_reachable"}) {
		t.Fatal("explicit same-movie public catalog association should permit sample")
	}
	for _, invalid := range []string{
		`<div class="field-name-download"><a href="https://archive.org.evil.net/movie.php?id=night_movie&type=.mp4">Download</a></div>`,
		`<div class="field-name-download"><a href="/movie.php?id=one&type=.mp4">Download</a><a href="/movie.php?id=two&type=.mp4">Download</a></div>`,
		`<script><div class="field-name-download"><a href="/movie.php?id=night_movie&type=.mp4">Download</a></div></script>`,
	} {
		page, _ := safeAvailabilityURL(source.URL)
		if id := publicDomainMovieArchiveItem([]byte(invalid), page); id != "" {
			t.Fatalf("ambiguous or external association accepted: %s", id)
		}
	}
}

func TestFreeResolutionBoundsAndPaidSourcesRemainUntouched(t *testing.T) {
	var requests atomic.Int32
	var running atomic.Int32
	var peak atomic.Int32
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		n := running.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer running.Add(-1)
		time.Sleep(15 * time.Millisecond)
		id := strings.TrimPrefix(request.URL.Path, "/metadata/")
		return freeSourceJSONResponse(request, archiveResolutionFixture(id)), nil
	})}
	scout := &Scout{availabilityHTTPClient: client}
	var candidates []CandidateSource
	for i := 0; i < 9; i++ {
		candidates = append(candidates, freeArchiveCandidate(fmt.Sprintf("movie_%d", i)))
	}
	paid := freeArchiveCandidate("paid_item")
	paid.AccessType = "subscription"
	candidates = append([]CandidateSource{paid}, candidates...)
	resolved, status := scout.resolveFreeMovieSources(context.Background(), candidates)
	if requests.Load() != 6 || peak.Load() > 3 || status.Status != "ok" || resolved[0].DirectStreamURL != "" || resolved[0].AccessType != "subscription" {
		t.Fatalf("resolution bounds failed: requests %d, peak %d, status %#v", requests.Load(), peak.Load(), status)
	}
	if candidates[1].DirectStreamURL != "" {
		t.Fatal("resolver mutated caller candidate slice")
	}
	if resolved[7].DirectStreamURL != "" {
		t.Fatal("candidate outside six-item budget was resolved")
	}
}

func TestFreeResolutionBudgetKeepsCatalogAndInstanceDiversity(t *testing.T) {
	var candidates []CandidateSource
	for i := 0; i < 9; i++ {
		candidates = append(candidates, freeArchiveCandidate(fmt.Sprintf("movie_%d", i)))
	}
	peer := freePeerTubeCandidate()
	candidates = append(candidates, peer)
	pdm := freeArchiveCandidate("night_movie")
	pdm.URL = "https://publicdomainmovie.net/movie/night-of-the-living-dead"
	candidates = append(candidates, pdm)
	peer2 := peer
	peer2.URL = "https://another.peer-video.net/w/2y84q2MQUMWPbiEcxNXMgC"
	candidates = append(candidates, peer2)
	indices := freeSourceResolutionTargets(candidates)
	selected := map[int]bool{}
	for _, index := range indices {
		selected[index] = true
	}
	if len(indices) != 6 || !selected[0] || !selected[9] || !selected[10] || !selected[11] {
		t.Fatalf("one catalog crowded out other supported sites: %v", indices)
	}
}

func TestFreeResolutionCancellationAndUnsafeRedirect(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		scout := &Scout{availabilityHTTPClient: client}
		resolved, status := scout.resolveFreeMovieSources(ctx, []CandidateSource{freeArchiveCandidate("night_movie")})
		if time.Since(start) > time.Second || resolved[0].DirectStreamURL != "" || status.Status != "unconfirmed" {
			t.Fatalf("cancelled resolution did not retain listing promptly: %#v", status)
		}
	})
	t.Run("private redirect", func(t *testing.T) {
		var mu sync.Mutex
		var requests []string
		client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			requests = append(requests, request.URL.String())
			mu.Unlock()
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"http://127.0.0.1/metadata/night_movie"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		})}
		if _, ok := resolveFreeMovieSource(context.Background(), client, freeArchiveCandidate("night_movie")); ok || len(requests) != 1 {
			t.Fatalf("private redirect was followed: %v", requests)
		}
	})
}

func TestFreeResolutionRejectsUnsafeTargetsAndOversizedMetadata(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/videos/watch/" + freePeerTubeFixtureID, "https://archive.org.evil.net/details/night_movie", "https://archive.org/details/night_movie/extra", "https://archive.org/details/../private", "https://user:pass@archive.org/details/night_movie", "https://archive.org:8443/details/night_movie"} {
		if _, _, ok := freeResolutionTarget(CandidateSource{URL: raw, AccessType: "unknown"}); ok {
			t.Errorf("unsafe or unsupported target accepted: %s", raw)
		}
	}
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", availabilityPageLimit+1))), Header: http.Header{}, Request: request}, nil
	})}
	if _, ok := resolveFreeMovieSource(context.Background(), client, freeArchiveCandidate("night_movie")); ok {
		t.Fatal("oversized metadata accepted")
	}
}
