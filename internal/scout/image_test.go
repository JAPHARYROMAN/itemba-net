package scout

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// testPicture encodes a smooth pattern as PNG, so a smaller copy hashes
// like the original, as a thumbnail of a picture does.
func testPicture(width, height int, phaseX, phaseY float64) []byte {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := 128 + 100*math.Sin(float64(x)/float64(width)*6+phaseX)*math.Cos(float64(y)/float64(height)*5+phaseY)
			img.SetGray(x, y, color.Gray{Y: uint8(math.Round(v))})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// imageFixtureScout answers for the Commons API and the upload host only:
// a picture, the same picture at a quarter of the size under another name,
// a different picture under the first picture's name, and a page without a
// file. Every other host is an error.
func imageFixtureScout(t *testing.T) (*Scout, func() []string) {
	t.Helper()
	pictures := map[string][]byte{
		"/wikipedia/commons/a/a1/Moon_landing.png":       testPicture(128, 96, 0, 0),
		"/wikipedia/commons/thumb/a/a1/320px-Moon.png":   testPicture(128, 96, 0, 0),
		"/wikipedia/commons/b/b2/Eagle_has_landed.png":   testPicture(64, 48, 0, 0),
		"/wikipedia/commons/thumb/b/b2/320px-Eagle.png":  testPicture(64, 48, 0, 0),
		"/wikipedia/commons/c/c3/Moon_landing_other.png": testPicture(128, 96, 2.1, 1.3),
		"/wikipedia/commons/thumb/c/c3/320px-Other.png":  testPicture(128, 96, 2.1, 1.3),
	}
	var mu sync.Mutex
	var requests []string
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, req.URL.Host+req.URL.Path)
		mu.Unlock()
		switch {
		case req.URL.Host == "commons.wikimedia.org" && req.URL.Path == "/w/api.php":
			if q := req.URL.Query().Get("gsrsearch"); q != "filetype:bitmap Moon landing" {
				t.Errorf("unexpected picture query: %s", q)
			}
			return searchFixtureResponse(200, "application/json", `{"query":{"pages":{
				"1":{"pageid":1,"title":"File:Moon landing.png","imageinfo":[{"width":128,"height":96,"thumburl":"https://upload.wikimedia.org/wikipedia/commons/thumb/a/a1/320px-Moon.png?utm_source=x","url":"https://upload.wikimedia.org/wikipedia/commons/a/a1/Moon_landing.png?utm_source=x","descriptionurl":"https://commons.wikimedia.org/wiki/File:Moon_landing.png","extmetadata":{"Artist":{"value":"NASA"},"LicenseShortName":{"value":"Public domain"}},"mime":"image/png"}]},
				"2":{"pageid":2,"title":"File:Eagle has landed.png","imageinfo":[{"width":64,"height":48,"thumburl":"https://upload.wikimedia.org/wikipedia/commons/thumb/b/b2/320px-Eagle.png","url":"https://upload.wikimedia.org/wikipedia/commons/b/b2/Eagle_has_landed.png","descriptionurl":"https://commons.wikimedia.org/wiki/File:Eagle_has_landed.png","extmetadata":{"Artist":{"value":"Someone"}},"mime":"image/png"}]},
				"3":{"pageid":3,"title":"File:Moon landing other.png","imageinfo":[{"width":128,"height":96,"thumburl":"https://upload.wikimedia.org/wikipedia/commons/thumb/c/c3/320px-Other.png","url":"https://upload.wikimedia.org/wikipedia/commons/c/c3/Moon_landing_other.png","descriptionurl":"https://commons.wikimedia.org/wiki/File:Moon_landing_other.png","extmetadata":{"Artist":{"value":"NASA"}},"mime":"image/png"}]},
				"4":{"pageid":4,"title":"File:Moon landing page only.png","imageinfo":[{"width":10,"height":10,"descriptionurl":"https://commons.wikimedia.org/wiki/File:Moon_landing_page_only.png","extmetadata":{},"mime":"image/png"}]}
			}}}`), nil
		case req.URL.Host == "upload.wikimedia.org":
			if data, ok := pictures[req.URL.Path]; ok {
				if req.URL.RawQuery != "" {
					t.Errorf("the file was fetched with its tracking parameters: %s", req.URL)
				}
				return searchFixtureResponse(200, "image/png", string(data)), nil
			}
			return searchFixtureResponse(404, "text/plain", "no such file"), nil
		case req.URL.Host == "commons.wikimedia.org" && req.URL.Path == "/wiki/File:Moon_landing_page_only.png":
			return searchFixtureResponse(200, "text/html; charset=utf-8", `<html><head><title>File:Moon landing page only.png - Wikimedia Commons</title></head><body><h1>File:Moon landing page only.png</h1><p>A picture page on Wikimedia Commons that lists a file the API did not hand out, kept here as a page for the probe to reach and read.</p></body></html>`), nil
		}
		return nil, fmt.Errorf("unexpected host for a picture search: %s", req.URL)
	})
	s.UseDrivers(testDrivers(t))
	// No browser runs in a unit test: the render is stubbed away unless a
	// test puts its own in.
	s.renderProbe = func(context.Context, VerifiedSource, string, string, string) *RenderCheck { return nil }
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func TestImageSearchDecodesFilesAndMatchesPicturesBySight(t *testing.T) {
	s, requests := imageFixtureScout(t)
	report := s.SearchWithOptions(context.Background(), "Moon landing", "ALL", SearchOptions{Kind: lens.TypeImage, FullLengthOnly: true})
	if report.Kind != lens.TypeImage || report.FullLengthOnly {
		t.Fatalf("a picture report names its kind and ignores the movie length preference: %+v", report)
	}
	for _, request := range requests() {
		if !strings.HasPrefix(request, "commons.wikimedia.org/") && !strings.HasPrefix(request, "upload.wikimedia.org/") && request != "www.cloudflare.com/cdn-cgi/trace" {
			t.Fatalf("a picture search reached %s; only the image drivers' hosts may be asked", request)
		}
	}
	if len(report.Sources) != 4 || report.VerifiedCount != 3 {
		t.Fatalf("four listings answer, three files decode: %+v", report)
	}
	byID := map[string]VerifiedSource{}
	for _, source := range report.Sources {
		byID[source.ID] = source
	}
	moon := byID["wm-image-1"]
	if moon.Kind != lens.TypeImage || moon.Creator != "NASA" || moon.License != "Public domain" || moon.ImageCheck == nil || moon.ImageCheck.Status != "decoded" || moon.ImageCheck.Width != 128 || moon.ImageCheck.Height != 96 || moon.ImageCheck.Format != "png" {
		t.Fatalf("the file's header decodes with its dimensions and format: %+v %+v", moon.CandidateSource, moon.ImageCheck)
	}
	if !moon.Verified || moon.VerificationStatus != "media_reachable" || moon.StreamFormat != "PNG image" || moon.AvailabilityCheck == nil || moon.AvailabilityCheck.Status != "media_reachable" || moon.ImageCheck.FingerprintStatus != "computed" || !moon.ImageCheck.Fingerprint.Valid() {
		t.Fatalf("a decoded file is reachable media with a hash: %+v %+v", moon, moon.ImageCheck)
	}
	eagle := byID["wm-image-2"]
	if eagle.ImageCheck == nil || eagle.ImageCheck.Width != 64 || !eagle.ImageCheck.Fingerprint.Valid() {
		t.Fatalf("the smaller copy decodes too: %+v", eagle.ImageCheck)
	}
	if moon.Identity == nil || moon.Identity.Method != lens.MethodPerceptualHash || moon.Identity.Verdict != lens.VerdictSame || moon.Identity.Effect != lens.EffectJoined || moon.Identity.PeerURL != eagle.URL || moon.Identity.Distance > lens.SameSightThreshold {
		t.Fatalf("the same picture under another name is matched by its hash: %+v", moon.Identity)
	}
	if !strings.Contains(moon.Identity.Detail, "Looks the same as") || strings.Contains(moon.Identity.Detail, "ound") {
		t.Fatalf("the verdict speaks of pictures: %q", moon.Identity.Detail)
	}
	other := byID["wm-image-3"]
	if other.Identity == nil || other.Identity.Verdict != lens.VerdictDifferent || other.Identity.Distance < 0.3 {
		t.Fatalf("a different picture is told apart: %+v", other.Identity)
	}
	if other.Identity.Effect == lens.EffectKeptApart {
		t.Fatalf("different titles do not share tags, so nothing is kept apart: %+v", other.Identity)
	}
	pageOnly := byID["wm-image-4"]
	if pageOnly.ImageCheck != nil || !pageOnly.ListingOnly || pageOnly.VerificationStatus != "page_reachable" || pageOnly.AvailabilityCheck == nil || pageOnly.AvailabilityCheck.Status != "page_reachable" {
		t.Fatalf("a listing without a file has its page checked: %+v %+v", pageOnly.CandidateSource, pageOnly.AvailabilityCheck)
	}
	if strings.Contains(strings.ToLower(pageOnly.AvailabilityCheck.Detail), "movie") || strings.Contains(strings.ToLower(pageOnly.AvailabilityCheck.Detail), "playback") {
		t.Fatalf("the page check speaks of a picture: %q", pageOnly.AvailabilityCheck.Detail)
	}
	if report.Sources[0].VerificationStatus != "media_reachable" || report.Sources[3].ID != "wm-image-4" {
		t.Fatalf("decoded files lead and the page-only listing trails: %v", []string{report.Sources[0].ID, report.Sources[1].ID, report.Sources[2].ID, report.Sources[3].ID})
	}
	coverage := report.AvailabilityChecks
	if coverage == nil || coverage.CheckedCount != 4 || coverage.FingerprintCount != 3 || !strings.Contains(coverage.Detail, "perceptual hash") || strings.Contains(strings.ToLower(coverage.Detail), "movie") {
		t.Fatalf("the coverage counts the hashes in the picture's words: %+v", coverage)
	}
	for _, provider := range report.Providers {
		if provider.Name != "Wikimedia Commons pictures" || provider.Status != "ok" {
			t.Fatalf("only the picture driver reports, and it answered: %+v", report.Providers)
		}
	}
	if lens.LevelFromChecks(moon.AvailabilityCheck.Status, "", false) != lens.LevelIdentified {
		t.Fatal("a decoded picture is identified on the ladder")
	}
}

func TestImageProbeTellsUnsupportedAndUndecodableFilesApart(t *testing.T) {
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/webp.webp":
			return searchFixtureResponse(200, "image/webp", "RIFF\x24\x00\x00\x00WEBPVP8 "+strings.Repeat("\x00", 32)), nil
		case "/page.html":
			return searchFixtureResponse(200, "text/html", "<html><body>not a picture</body></html>"), nil
		case "/gone.png":
			return searchFixtureResponse(404, "text/plain", "gone"), nil
		}
		return searchFixtureResponse(200, "image/png", string(testPicture(20, 10, 0, 0))), nil
	})}
	source := CandidateSource{Kind: lens.TypeImage, URL: "https://commons.wikimedia.org/wiki/File:X", DirectStreamURL: "https://upload.wikimedia.org/webp.webp"}
	if check := probeImage(context.Background(), client, source); check.Status != "unsupported" || check.Format != "webp" || imageAvailability(check, "ALL", "TZ").Status != "media_reachable" {
		t.Fatalf("a picture Go cannot decode is unsupported yet reachable: %+v", check)
	}
	source.DirectStreamURL = "https://upload.wikimedia.org/page.html"
	if check := probeImage(context.Background(), client, source); check.Status != "undecodable" || imageAvailability(check, "ALL", "TZ").Status != "unconfirmed" {
		t.Fatalf("an HTML answer is undecodable: %+v", check)
	}
	source.DirectStreamURL = "https://upload.wikimedia.org/gone.png"
	if check := probeImage(context.Background(), client, source); check.Status != "unreachable" || check.HTTPStatus != 404 || imageAvailability(check, "ALL", "TZ").Status != "not_found" {
		t.Fatalf("a missing file is not found: %+v", check)
	}
	source.DirectStreamURL = "https://upload.wikimedia.org/small.png"
	if check := probeImage(context.Background(), client, source); check.Status != "decoded" || check.Width != 20 || check.FingerprintStatus != "computed" {
		t.Fatalf("a small original without a thumbnail is hashed from the bytes already fetched: %+v", check)
	}
	if w, h := parseResolution("2356x2795"); w != 2356 || h != 2795 {
		t.Fatal("resolution")
	}
	if w, h := parseResolution("tall"); w != 0 || h != 0 {
		t.Fatal("an unreadable resolution is zero")
	}
}
