package scout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/convert"
	"github.com/itemba-net/core/internal/lens"
)

// fakeRenderBrowser answers the render runner's commands: it emits the
// harness's media response on navigation, reports the page state the test
// scripts by evaluation, and hands back the screenshot the test chose.
type fakeRenderBrowser struct {
	handler       cdp.EventHandler
	origin        string
	evaluations   int
	state         func(evaluation int) *renderSnapshot
	mediaResource string
	mediaType     string
	mediaStatus   int
	screenshot    []byte
	calls         []string
}

func (b *fakeRenderBrowser) OnEvent(h cdp.EventHandler)                { b.handler = h }
func (b *fakeRenderBrowser) CallAsync(method string, params any) error { return nil }
func (b *fakeRenderBrowser) Call(ctx context.Context, method string, params any, result any) error {
	b.calls = append(b.calls, method)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch method {
	case "Page.navigate":
		if b.mediaStatus != 0 {
			data, _ := json.Marshal(map[string]any{"type": b.mediaResource, "response": map[string]any{"url": b.origin + "/media", "status": b.mediaStatus, "mimeType": b.mediaType}})
			b.handler("Network.responseReceived", data)
		}
	case "Runtime.evaluate":
		b.evaluations++
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"value": b.state(b.evaluations)}})
		return json.Unmarshal(data, result)
	case "Page.captureScreenshot":
		data, _ := json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(b.screenshot)})
		return json.Unmarshal(data, result)
	}
	return nil
}

// screenshotPNG draws a sandbox screenshot: a grey frame, with a block of
// black text-like rows covering drawn of it when drawn is positive.
func screenshotPNG(drawn float64) []byte {
	img := image.NewGray(image.Rect(0, 0, 320, 180))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	rows := int(float64(180) * drawn)
	for y := 0; y < rows; y++ {
		for x := 0; x < 320; x++ {
			if (x/4)%2 == 0 {
				img.SetGray(x, y, color.Gray{Y: 10})
			}
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}

// renderCells is the grey grid of a smooth picture.
func renderCells(phase float64) []int {
	cells := make([]int, lens.VideoFrameCells)
	columns := lens.ImageHashColumns + 1
	for y := 0; y < lens.ImageHashRows; y++ {
		for x := 0; x < columns; x++ {
			v := 128 + 100*sinApprox(float64(x)/float64(columns)*6+phase)*sinApprox(float64(y)/float64(lens.ImageHashRows)*5+1.2)
			cells[y*columns+x] = int(v)
		}
	}
	return cells
}

// sinApprox is a smooth periodic function without pulling in math for one
// fixture.
func sinApprox(x float64) float64 {
	for x > 3.14159265 {
		x -= 2 * 3.14159265
	}
	for x < -3.14159265 {
		x += 2 * 3.14159265
	}
	return x - x*x*x/6 + x*x*x*x*x/120 - x*x*x*x*x*x*x/5040
}

func TestRenderHarnessPagesAndPolicies(t *testing.T) {
	page := string(imageRenderHTML("abc"))
	for _, want := range []string{`<img id="sample" src="/media" decoding="async"`, "img.decode()", "done('rendered',{cells:grey})", "COLUMNS=17,ROWS=16", `nonce="abc"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the image page draws the picture onto the hash grid; missing %q", want)
		}
	}
	doc := string(documentRenderHTML("abc", 640, 360))
	for _, want := range []string{`<iframe id="sample" src="/media" width="640" height="360"`, "state:'loaded'", `nonce="abc"`} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the document page frames the PDF and records its load; missing %q", want)
		}
	}
	if csp := harnessCSP("image", "n"); !strings.Contains(csp, "img-src 'self'") || strings.Contains(csp, "frame-src") {
		t.Fatalf("the image policy admits the picture and nothing else: %s", csp)
	}
	if csp := harnessCSP("document", "n"); !strings.Contains(csp, "frame-src 'self'") || !strings.Contains(csp, "img-src 'none'") {
		t.Fatalf("the document policy admits the frame and no pictures: %s", csp)
	}
	if csp := harnessCSP("", "n"); !strings.Contains(csp, "img-src 'none'") || !strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "media-src 'self' blob:") {
		t.Fatalf("the playback policy is unchanged: %s", csp)
	}
	origin := "http://127.0.0.1:1234"
	cases := []struct {
		kind, url, resource string
		want                bool
	}{
		{"image", origin + "/", "Document", true},
		{"image", origin + "/media", "Image", true},
		{"image", origin + "/media", "Document", false},
		{"image", origin + "/media", "Media", false},
		{"document", origin + "/media", "Document", true},
		{"document", origin + "/media", "Other", true},
		{"document", origin + "/media", "Image", false},
		{"document", "http://127.0.0.1:9999/media", "Document", false},
		{"image", origin + "/media?x=1", "Image", false},
		{"image", origin + "/hls.js", "Script", false},
		{"document", pdfViewerOrigin + "index.css", "Stylesheet", true},
		{"image", pdfViewerOrigin + "index.css", "Stylesheet", false},
		{"document", "chrome-extension://someotherextensionid/index.html", "Document", false},
	}
	if !pdfViewerTarget("iframe", "", "") || !pdfViewerTarget("iframe", pdfViewerOrigin+"index.html", "") || pdfViewerTarget("page", "", "") || pdfViewerTarget("iframe", "https://example.org/", "") || pdfViewerTarget("iframe", "", "opener") {
		t.Fatal("only the viewer frame, with no opener, is the browser drawing the document")
	}
	for _, tc := range cases {
		if got := renderHarnessRequestAllowed(tc.kind, origin, tc.url, tc.resource); got != tc.want {
			t.Errorf("renderHarnessRequestAllowed(%q, %q, %q) = %v, want %v", tc.kind, tc.url, tc.resource, got, tc.want)
		}
	}
	pngBytes := testPicture(20, 10, 0, 0)
	if typeName, ok := imageResponseType("application/octet-stream", pngBytes); !ok || typeName != "image/png" {
		t.Fatalf("a picture is admitted by its bytes whatever the header: %q %v", typeName, ok)
	}
	if _, ok := imageResponseType("image/png", []byte("<html><body>no</body></html>")); ok {
		t.Fatal("a page is never a picture")
	}
	if _, ok := imageResponseType("image/svg+xml", []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>")); ok {
		t.Fatal("an SVG, which may carry script, is not rendered")
	}
	if _, ok := imageResponseType("image/png", []byte("%PDF-1.4\n")); ok {
		t.Fatal("a PDF is not a picture")
	}
	if typeName, ok := documentResponseType("application/octet-stream", []byte("%PDF-1.5\n%\xe2\xe3")); !ok || typeName != "application/pdf" {
		t.Fatalf("a PDF is admitted by its bytes: %q %v", typeName, ok)
	}
	if _, ok := documentResponseType("application/pdf", pngBytes); ok {
		t.Fatal("a picture is not a document")
	}
	harness := &moviePlaybackHarness{kind: "image"}
	if _, ok := harness.responseType("video/mp4", []byte("\x00\x00\x00\x18ftypmp42")); ok {
		t.Fatal("the image harness relays no video")
	}
	harness.kind = ""
	if _, ok := harness.responseType("video/mp4", []byte("\x00\x00\x00\x18ftypmp42")); !ok {
		t.Fatal("the playback harness relays video as before")
	}

	decoded := VerifiedSource{CandidateSource: CandidateSource{AccessType: "free", DirectStreamURL: "https://upload.wikimedia.org/a.png", Thumbnail: "https://upload.wikimedia.org/thumb/a.png"}, ImageCheck: &ImageCheck{Status: "decoded", Bytes: 9 << 20}}
	if target, ok := renderablePicture(decoded); !ok || target.DirectStreamURL != decoded.Thumbnail {
		t.Fatalf("a decoded picture is rendered from its thumbnail: %v %q", ok, target.DirectStreamURL)
	}
	decoded.Thumbnail = ""
	if _, ok := renderablePicture(decoded); ok {
		t.Fatal("a large original without a thumbnail is not rendered")
	}
	decoded.ImageCheck.Bytes = 300 << 10
	if target, ok := renderablePicture(decoded); !ok || target.DirectStreamURL != decoded.DirectStreamURL {
		t.Fatalf("a small original is rendered as it is: %v", ok)
	}
	decoded.ImageCheck.Status = "unsupported"
	if _, ok := renderablePicture(decoded); ok {
		t.Fatal("only decoded pictures are rendered")
	}
	parsed := VerifiedSource{CandidateSource: CandidateSource{AccessType: "free", DirectStreamURL: "https://archive.org/download/x/x.pdf"}, DocumentCheck: &DocumentCheck{Status: "parsed", Format: "pdf"}}
	if _, ok := renderableDocument(parsed); !ok {
		t.Fatal("a parsed free PDF is rendered")
	}
	parsed.DocumentCheck.Format = "txt"
	if _, ok := renderableDocument(parsed); ok {
		t.Fatal("only PDFs are framed")
	}
	ebook := VerifiedSource{CandidateSource: CandidateSource{AccessType: "free", DirectStreamURL: "https://archive.org/download/x/x.epub"}, DocumentCheck: &DocumentCheck{Status: "unsupported", Format: "epub"}}
	withoutConverter := hostConverter
	hostConverter = func() *convert.Converter { return &convert.Converter{} }
	if _, ok := renderableDocument(ebook); ok {
		t.Fatal("an e-book is not framed on a host without a converter")
	}
	stub := &convert.Converter{Commands: []convert.Command{{Name: "ebook-convert", Path: "ebook-convert", Formats: []string{"epub"}}}, CacheDir: t.TempDir(), CacheLimit: 1 << 20, InputLimit: 1 << 20, Timeout: 5 * time.Second, Run: func(_ context.Context, cmd *exec.Cmd) error {
		return os.WriteFile(filepath.Join(cmd.Dir, "input.pdf"), []byte("%PDF-1.4\n%%EOF\n"), 0o600)
	}}
	hostConverter = func() *convert.Converter { return stub }
	defer func() { hostConverter = withoutConverter }()
	if _, ok := renderableDocument(ebook); !ok {
		t.Fatal("an e-book is framed when this host converts it")
	}
	client := &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/x.epub") {
			return searchFixtureResponse(200, "application/epub+zip", "PK\x03\x04 a fake epub"), nil
		}
		return searchFixtureResponse(404, "text/plain", "gone"), nil
	})}
	converted, detail := convertForRender(context.Background(), client, stub, ebook.DirectStreamURL, "epub")
	if converted.Path == "" || !strings.Contains(detail, "EPUB converted to PDF by ebook-convert in") {
		t.Fatalf("the e-book is fetched and converted for the render: %+v %q", converted, detail)
	}
	if again, detail := convertForRender(context.Background(), client, stub, ebook.DirectStreamURL, "epub"); again.Path != converted.Path || !strings.Contains(detail, "cached") {
		t.Fatalf("the second render finds the cached PDF: %+v %q", again, detail)
	}
	if _, detail := convertForRender(context.Background(), client, stub, "https://archive.org/download/x/missing.epub", "epub"); !strings.Contains(detail, "could not be fetched") {
		t.Fatalf("a file the host does not have is said: %q", detail)
	}
	if _, detail := convertForRender(context.Background(), client, stub, ebook.DirectStreamURL, "djvu"); !strings.Contains(detail, "No converter on this host takes DJVU") {
		t.Fatalf("a format this host cannot convert is said: %q", detail)
	}
	if _, detail := convertForRender(context.Background(), client, &convert.Converter{}, ebook.DirectStreamURL, "epub"); !strings.Contains(detail, "No converter on this host turns EPUB") {
		t.Fatalf("a host without converters is said: %q", detail)
	}
	if share, w, h, err := visibleShare(screenshotPNG(0)); err != nil || share != 0 || w != 320 || h != 180 {
		t.Fatalf("a blank frame has nothing drawn: %v %d %d %v", share, w, h, err)
	}
	if share, _, _, _ := visibleShare(screenshotPNG(0.1)); share < 0.04 || share > 0.06 {
		t.Fatalf("a frame with text rows is drawn in proportion: %v", share)
	}
	if _, _, _, err := visibleShare([]byte("not a png")); err == nil {
		t.Fatal("a screenshot that is not a PNG is an error")
	}
}

func TestRenderSampleJudgesPicturesAndDocuments(t *testing.T) {
	origin := "http://127.0.0.1:1234"
	cells := renderCells(0.3)
	probeHash := lens.ImageFingerprint(cellsImage(cells))
	picture := VerifiedSource{CandidateSource: CandidateSource{Kind: lens.TypeImage, URL: "https://commons.wikimedia.org/wiki/File:X.png", DirectStreamURL: "https://upload.wikimedia.org/thumb/x.png"}, ImageCheck: &ImageCheck{Status: "decoded", Fingerprint: &probeHash}}

	browser := &fakeRenderBrowser{origin: origin, mediaResource: "Image", mediaType: "image/png", mediaStatus: 200, state: func(n int) *renderSnapshot {
		if n < 2 {
			return &renderSnapshot{State: "loading"}
		}
		return &renderSnapshot{State: "rendered", Width: 640, Height: 480, Cells: cells}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := runRenderSample(ctx, browser, picture, origin, "image", &RenderCheck{Method: MethodImageRender})
	if result.Status != "rendered" || result.Width != 640 || result.Height != 480 || result.HTTPStatus != 200 || result.MimeType != "image/png" || result.Milliseconds <= 0 {
		t.Fatalf("a decoded, drawn picture is rendered: %+v", result)
	}
	if result.Fingerprint == nil || result.HashAgrees != "yes" || result.HashDistance != 0 || !strings.Contains(result.Detail, "hash as the probe said") {
		t.Fatalf("the drawn pixels hash as the probe read the file: %+v", result)
	}
	for _, call := range browser.calls {
		if call == "Page.captureScreenshot" {
			t.Fatal("a picture needs no screenshot")
		}
	}

	// The drawn pixels of another picture disagree with the probe.
	browser = &fakeRenderBrowser{origin: origin, mediaResource: "Image", mediaType: "image/png", mediaStatus: 200, state: func(int) *renderSnapshot {
		return &renderSnapshot{State: "rendered", Width: 64, Height: 48, Cells: renderCells(2.5)}
	}}
	result = runRenderSample(ctx, browser, picture, origin, "image", &RenderCheck{Method: MethodImageRender})
	if result.Status != "rendered" || result.HashAgrees != "no" || result.HashDistance < 0.3 {
		t.Fatalf("a picture that draws differently is rendered but said to differ: %+v", result)
	}

	// The relay refused the bytes, so the element errored.
	browser = &fakeRenderBrowser{origin: origin, mediaResource: "Image", mediaType: "text/plain", mediaStatus: 502, state: func(int) *renderSnapshot {
		return &renderSnapshot{State: "error", Error: "media_error"}
	}}
	result = runRenderSample(ctx, browser, picture, origin, "image", &RenderCheck{Method: MethodImageRender})
	if result.Status != "not_rendered" || result.HTTPStatus != 502 || !strings.Contains(result.Detail, "refused") {
		t.Fatalf("a refused file is not rendered and says why: %+v", result)
	}

	document := VerifiedSource{CandidateSource: CandidateSource{Kind: lens.TypeDocument, URL: "https://archive.org/details/x", DirectStreamURL: "https://archive.org/download/x/x.pdf"}, DocumentCheck: &DocumentCheck{Status: "parsed", Format: "pdf"}}
	loaded := func(n int) *renderSnapshot {
		if n < 2 {
			return &renderSnapshot{State: "loading"}
		}
		return &renderSnapshot{State: "loaded", Width: 640, Height: 360}
	}
	// Each document render waits for the frame to settle, so each gets its
	// own deadline.
	renderDocument := func(b *fakeRenderBrowser) *RenderCheck {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return runRenderSample(c, b, document, origin, "document", &RenderCheck{Method: MethodDocumentRender})
	}
	result = renderDocument(&fakeRenderBrowser{origin: origin, mediaResource: "Document", mediaType: "application/pdf", mediaStatus: 206, screenshot: screenshotPNG(0.1), state: loaded})
	if result.Status != "rendered" || !result.FrameLoaded || result.VisibleShare < 0.04 || result.MimeType != "application/pdf" || result.HTTPStatus != 206 || !strings.Contains(result.Detail, "drew it") {
		t.Fatalf("a framed PDF with a drawn page is rendered: %+v", result)
	}
	result = renderDocument(&fakeRenderBrowser{origin: origin, mediaResource: "Document", mediaType: "application/pdf", mediaStatus: 206, screenshot: screenshotPNG(0), state: loaded})
	if result.Status != "unconfirmed" || !result.FrameLoaded || result.VisibleShare != 0 || !strings.Contains(result.Detail, "blank") {
		t.Fatalf("a framed PDF whose frame stayed blank is unconfirmed: %+v", result)
	}
	result = renderDocument(&fakeRenderBrowser{origin: origin, mediaResource: "Document", mediaType: "text/html", mediaStatus: 200, screenshot: screenshotPNG(0.1), state: loaded})
	if result.Status != "unconfirmed" || !strings.Contains(result.Detail, "not a PDF document") {
		t.Fatalf("a frame that loaded something else is unconfirmed: %+v", result)
	}

	// Nothing loads within the deadline.
	browser = &fakeRenderBrowser{origin: origin, mediaResource: "Image", mediaType: "image/png", mediaStatus: 200, state: func(int) *renderSnapshot { return &renderSnapshot{State: "loading"} }}
	short, cancelShort := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancelShort()
	result = runRenderSample(short, browser, picture, origin, "image", &RenderCheck{Method: MethodImageRender})
	if result.Status != "not_rendered" || !strings.Contains(result.Detail, "did not load and draw") {
		t.Fatalf("a picture that never draws is not rendered: %+v", result)
	}
}

func TestSearchesRenderUpToTwoFilesInTheSandbox(t *testing.T) {
	s, _ := imageFixtureScout(t)
	var rendered []string
	s.renderProbe = func(_ context.Context, source VerifiedSource, kind, region, country string) *RenderCheck {
		rendered = append(rendered, source.DirectStreamURL)
		if kind != "image" || region != "ALL" {
			t.Errorf("a picture render names its kind and region: %q %q", kind, region)
		}
		if strings.Contains(source.DirectStreamURL, "320px-Moon") {
			return &RenderCheck{Status: "rendered", Method: MethodImageRender, Width: 128, Height: 96, Milliseconds: 900, HTTPStatus: 200, MimeType: "image/png", HashAgrees: "yes", Detail: "drawn"}
		}
		return &RenderCheck{Status: "not_rendered", Method: MethodImageRender, HTTPStatus: 502, Detail: "refused"}
	}
	report := s.SearchWithOptions(context.Background(), "Moon landing", "ALL", SearchOptions{Kind: lens.TypeImage})
	if len(rendered) != 2 || report.AvailabilityChecks == nil || report.AvailabilityChecks.RenderCheckedCount != 2 || report.AvailabilityChecks.RenderedCount != 1 {
		t.Fatalf("two decoded pictures are rendered from their thumbnails: %v %+v", rendered, report.AvailabilityChecks)
	}
	for _, url := range rendered {
		if !strings.Contains(url, "/thumb/") {
			t.Fatalf("the render takes the thumbnail: %s", url)
		}
	}
	if first := report.Sources[0]; first.ID != "wm-image-1" || first.RenderCheck == nil || first.RenderCheck.Status != "rendered" {
		t.Fatalf("the rendered picture leads: %s %+v", first.ID, first.RenderCheck)
	}
	if !strings.Contains(report.AvailabilityChecks.Detail, "sandbox") || strings.Contains(report.AvailabilityChecks.Detail, "No sandbox render runs yet") {
		t.Fatalf("the coverage speaks of the render: %s", report.AvailabilityChecks.Detail)
	}

	d, _ := documentFixtureScout(t)
	rendered = nil
	d.renderProbe = func(_ context.Context, source VerifiedSource, kind, region, country string) *RenderCheck {
		rendered = append(rendered, source.DirectStreamURL)
		if kind != "document" {
			t.Errorf("a document render names its kind: %q", kind)
		}
		if strings.HasSuffix(source.DirectStreamURL, "alice-scan.pdf") {
			return &RenderCheck{Status: "rendered", Method: MethodDocumentRender, FrameLoaded: true, VisibleShare: 0.07, Milliseconds: 2100, HTTPStatus: 206, MimeType: "application/pdf", Detail: "drawn"}
		}
		return &RenderCheck{Status: "unconfirmed", Method: MethodDocumentRender, FrameLoaded: true, HTTPStatus: 206, MimeType: "application/pdf", Detail: "blank"}
	}
	report = d.SearchWithOptions(context.Background(), "Alice in Wonderland by Lewis Carroll", "ALL", SearchOptions{Kind: lens.TypeDocument})
	if len(rendered) != 2 || report.AvailabilityChecks.RenderCheckedCount != 2 || report.AvailabilityChecks.RenderedCount != 1 {
		t.Fatalf("two parsed PDFs are framed: %v %+v", rendered, report.AvailabilityChecks)
	}
	for _, url := range rendered {
		if !strings.HasSuffix(url, ".pdf") {
			t.Fatalf("only PDFs are framed: %s", url)
		}
	}
	if first := report.Sources[0]; first.ID != "ia-text-alice-scan" || first.RenderCheck == nil || first.RenderCheck.Status != "rendered" {
		t.Fatalf("the rendered document leads: %s %+v", first.ID, first.RenderCheck)
	}
}
