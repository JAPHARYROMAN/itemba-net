package scout

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func blenderProviderFixtureVideo(id, name string) map[string]any {
	return map[string]any{"uuid": id, "name": name, "duration": 596, "privacy": map[string]any{"id": 1}, "url": "https://video.blender.org/videos/watch/" + id}
}

func TestBlenderProviderDiscoversOfficialPublicMovieWithoutPlaybackClaims(t *testing.T) {
	const id = "bf1f3fb5-b119-4f9f-9930-8e20e892b898"
	client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "video.blender.org" || request.URL.Path != "/api/v1/search/videos" || request.URL.Query().Get("search") != "Big Buck Bunny" || request.URL.Query().Get("count") != "6" || request.URL.Query().Get("isLocal") != "true" || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("unexpected official catalog request: %s", request.URL)
		}
		return freeSourceJSONResponse(request, map[string]any{"data": []any{blenderProviderFixtureVideo(id, "Big Buck Bunny")}}), nil
	})}
	scout := &Scout{availabilityHTTPClient: client}
	items, err := scout.queryBlenderMovies(context.Background(), "Big Buck Bunny 2008")
	if err != nil || len(items) != 1 {
		t.Fatalf("official result missing: %#v, %v", items, err)
	}
	item := items[0]
	if item.URL != "https://video.blender.org/videos/watch/"+id || item.DirectStreamURL != "" || !item.ListingOnly || item.AccessType != "unknown" || item.AccessBasis != "" || item.Duration != "9m56s" || item.Provider != "PeerTube (video.blender.org)" {
		t.Fatalf("search result prematurely claimed movie access: %#v", item)
	}
	if kind, resolvedID, ok := freeResolutionTarget(item); !ok || kind != "peertube" || resolvedID != id {
		t.Fatal("discovered movie did not enter existing metadata-resolution flow")
	}
}

func TestBlenderProviderFiltersPrivateLiveForeignTrailersAndInvalidIDs(t *testing.T) {
	const id = "bf1f3fb5-b119-4f9f-9930-8e20e892b898"
	tests := map[string]func(map[string]any){
		"private":      func(v map[string]any) { v["privacy"] = map[string]any{"id": 3} },
		"live":         func(v map[string]any) { v["isLive"] = true },
		"password":     func(v map[string]any) { v["passwordProtected"] = true },
		"invalid uuid": func(v map[string]any) { v["uuid"] = "../private" },
		"foreign page": func(v map[string]any) { v["url"] = "https://other.video.net/videos/watch/" + id },
		"different video page": func(v map[string]any) {
			v["url"] = "https://video.blender.org/videos/watch/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		},
		"short clip": func(v map[string]any) { v["duration"] = 90 },
		"trailer":    func(v map[string]any) { v["name"] = "Big Buck Bunny Trailer" },
		"tutorial":   func(v map[string]any) { v["name"] = "Blender animation tutorial using Big Buck Bunny as an example" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			video := blenderProviderFixtureVideo(id, "Big Buck Bunny")
			mutate(video)
			scout := &Scout{availabilityHTTPClient: &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return freeSourceJSONResponse(request, map[string]any{"data": []any{video}}), nil
			})}}
			items, err := scout.queryBlenderMovies(context.Background(), "Big Buck Bunny")
			if err != nil || len(items) != 0 {
				t.Fatalf("unsupported official search result accepted: %#v, %v", items, err)
			}
		})
	}
}

func TestBlenderProviderNormalizesOnlyOfficialWrappers(t *testing.T) {
	for original, expected := range map[string]string{
		"Sintel - Third Open Movie by Blender Foundation":                  "Sintel",
		"Tears of Steel - Blender VFX Open Movie":                          "Tears of Steel",
		"Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film": "Big Buck Bunny",
		"Agent 327: Operation Barbershop":                                  "Agent 327: Operation Barbershop",
		"A tutorial about Blender movies":                                  "A tutorial about Blender movies",
	} {
		if normalized := normalizeBlenderMovieTitle(original); normalized != expected {
			t.Errorf("normalized %q to %q, want %q", original, normalized, expected)
		}
	}
}

func TestBlenderOfficialWrapperMatchesMetadataWithoutHidingTrailers(t *testing.T) {
	for _, test := range []struct {
		name, host, metadataTitle string
		accepted                  bool
	}{
		{"official movie", "video.blender.org", "Sintel - Third Open Movie by Blender Foundation", true},
		{"official trailer", "video.blender.org", "Sintel Trailer - Third Open Movie by Blender Foundation", false},
		{"foreign branding", "films.peer-video.net", "Sintel - Third Open Movie by Blender Foundation", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := peerTubeResolutionFixture()
			payload["name"] = test.metadataTitle
			source := freePeerTubeCandidate()
			source.URL = "https://" + test.host + "/videos/watch/" + freePeerTubeFixtureID
			source.Provider = "PeerTube (" + test.host + ")"
			client := &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				return freeSourceJSONResponse(request, payload), nil
			})}
			resolved, accepted := resolveFreeMovieSource(context.Background(), client, source)
			if accepted != test.accepted || accepted && resolved.Title != "Sintel" {
				t.Fatalf("official wrapper identity result: %#v, accepted %t", resolved, accepted)
			}
		})
	}
}

func TestBlenderProviderBoundsResultsAndHandlesUnavailableCatalog(t *testing.T) {
	t.Run("result count", func(t *testing.T) {
		var videos []any
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("bf1f3fb5-b119-4f9f-9930-%012d", i)
			videos = append(videos, blenderProviderFixtureVideo(id, "Big Buck Bunny"))
		}
		scout := &Scout{availabilityHTTPClient: &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
			return freeSourceJSONResponse(request, map[string]any{"data": videos}), nil
		})}}
		items, err := scout.queryBlenderMovies(context.Background(), "Big Buck Bunny")
		if err != nil || len(items) != 6 {
			t.Fatalf("official search result cap failed: %d, %v", len(items), err)
		}
	})
	for _, test := range []struct {
		name     string
		status   int
		body     string
		location string
	}{
		{"unavailable", 503, "unavailable", ""},
		{"invalid json", 200, "not json", ""},
		{"missing data", 200, `{}`, ""},
		{"oversized", 200, strings.Repeat("x", availabilityPageLimit+1), ""},
		{"private redirect", 302, "", "http://127.0.0.1/private"},
		{"foreign redirect", 302, "", "https://other.video.net/api/v1/search/videos"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			scout := &Scout{availabilityHTTPClient: &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
				requests++
				header := http.Header{}
				if test.location != "" {
					header.Set("Location", test.location)
				}
				return &http.Response{StatusCode: test.status, Header: header, Body: io.NopCloser(strings.NewReader(test.body)), Request: request}, nil
			})}}
			if items, err := scout.queryBlenderMovies(context.Background(), "Big Buck Bunny"); err == nil || len(items) != 0 || requests > 5 {
				t.Fatalf("unavailable/untrusted catalog accepted: %#v, %v, requests %d", items, err, requests)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	scout := &Scout{availabilityHTTPClient: &http.Client{Transport: freeSourceTransport(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}
	if _, err := scout.queryBlenderMovies(ctx, "Big Buck Bunny"); err == nil {
		t.Fatal("cancelled official catalog search unexpectedly succeeded")
	}
}
