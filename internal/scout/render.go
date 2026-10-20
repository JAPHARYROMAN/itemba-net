package scout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/convert"
	"github.com/itemba-net/core/internal/lens"
)

// hostConverter finds the host's document converters once; tests put a
// converter of their own in its place.
var hostConverter = sync.OnceValue(func() *convert.Converter { return convert.Detect("") })

// convertInputLimit bounds the bytes fetched for a conversion.
const convertInputLimit = 48 << 20

// The sandbox render: the rendered level of the ladder for pictures and
// documents, which only video and audio samples reached before. The same
// throwaway Chromium and the same local harness that sample a movie serve
// a picture to an image element, or a PDF to a frame, through Shield, with
// every request intercepted and nothing but the harness's own page and
// its one file allowed. A picture is rendered when the element decoded it
// and its pixels could be drawn: the drawn pixels are hashed as the probe
// hashed the file, so the render also says whether what the sandbox saw
// is what the probe read. A document is rendered when the frame loaded a
// PDF document and a screenshot of the sandbox shows a drawn page rather
// than a blank frame, measured as the share of pixels off the frame's
// dominant colour. Neither promises the whole file: a render is a bounded
// look at the beginning, as a sample is.

const (
	// maxRenderSamples is how many files one search renders.
	maxRenderSamples = 2
	// renderDeadline bounds one picture render, from launching the browser
	// to the verdict, and documentRenderDeadline one document render, which
	// fetches a megabyte of the PDF and its tail through the relay before
	// the viewer can draw; renderSettle is how long a loaded document frame
	// is given to draw before the screenshot.
	renderDeadline         = 12 * time.Second
	documentRenderDeadline = 30 * time.Second
	renderSettle           = 1500 * time.Millisecond
	// renderVisibleShare is the share of screenshot pixels off the dominant
	// colour at or above which a document frame counts as drawn: a page of
	// text is a few percent, a blank frame or a bare viewer none.
	renderVisibleShare = 0.02
	// MethodImageRender and MethodDocumentRender name the checks.
	MethodImageRender    = "sandbox_image_render"
	MethodDocumentRender = "sandbox_document_render"
)

// RenderCheck records a bounded render of a picture or a document in the
// isolated sandbox. Status is rendered, not_rendered (the sandbox could
// not load or draw the file), unconfirmed (it loaded but nothing proves it
// drew), unsupported or skipped. Width and Height are the picture's
// natural size or the frame's; Milliseconds how long the render took from
// opening the harness page; HTTPStatus and MimeType what the harness
// relayed; FrameLoaded whether a document frame fired its load event;
// VisibleShare the share of the screenshot off its dominant colour;
// Fingerprint the hash of the pixels a picture render drew, with
// HashDistance to the probe's hash and HashAgrees (yes, no, or empty when
// there was nothing to compare with).
type RenderCheck struct {
	Status          string            `json:"status"`
	Reason          string            `json:"reason,omitempty"`
	Method          string            `json:"method"`
	Country         string            `json:"country,omitempty"`
	RequestedRegion string            `json:"requestedRegion"`
	CheckedAt       string            `json:"checkedAt"`
	Detail          string            `json:"detail"`
	EvidenceURL     string            `json:"evidenceUrl,omitempty"`
	Width           int               `json:"width,omitempty"`
	Height          int               `json:"height,omitempty"`
	Milliseconds    int64             `json:"milliseconds,omitempty"`
	HTTPStatus      int               `json:"httpStatus,omitempty"`
	MimeType        string            `json:"mimeType,omitempty"`
	FrameLoaded     bool              `json:"frameLoaded,omitempty"`
	VisibleShare    float64           `json:"visibleShare,omitempty"`
	Fingerprint     *lens.Fingerprint `json:"fingerprint,omitempty"`
	HashDistance    float64           `json:"hashDistance,omitempty"`
	HashAgrees      string            `json:"hashAgrees,omitempty"`
	// Converted says how an e-book or office file became the PDF that was
	// framed: the format, the converter and the time it took.
	Converted string `json:"converted,omitempty"`
}

// renderSnapshot is the harness page's render state as the page keeps it
// in window.__itembaRender: loading, rendered (a picture decoded and
// drawn, with its cells), loaded (a document frame fired load) or error.
type renderSnapshot struct {
	State  string `json:"state"`
	Error  string `json:"error"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Cells  []int  `json:"cells"`
}

const renderSnapshotJS = `(() => window.__itembaRender || null)()`

// renderDeadlineFor is the deadline of one render of a kind, and
// harnessLifetime how long the harness serving it lives: a sample's window
// for a playback sample or a picture, longer for a document.
func renderDeadlineFor(kind string) time.Duration {
	if kind == "document" {
		return documentRenderDeadline
	}
	return renderDeadline
}

func harnessLifetime(kind string) time.Duration {
	if kind == "document" {
		return documentRenderDeadline + 2*time.Second
	}
	return moviePlaybackLifetime
}

// renderMethod names the check for a render kind.
func renderMethod(kind string) string {
	if kind == "document" {
		return MethodDocumentRender
	}
	return MethodImageRender
}

// harnessCSP is the policy the harness serves its pages under: a playback
// page may load media and its own script; an image page may load its one
// picture; a document page may frame its one document.
func harnessCSP(kind, nonce string) string {
	policy := "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; connect-src 'self'; media-src 'self' blob:; img-src 'none'; style-src 'none'; object-src 'none'; worker-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	switch kind {
	case "image":
		policy = strings.Replace(policy, "img-src 'none'", "img-src 'self'", 1)
	case "document":
		policy = strings.Replace(policy, "object-src 'none'", "object-src 'self'; frame-src 'self'; child-src 'self'", 1)
	}
	return policy
}

// pdfViewerOrigin is the origin of the built-in PDF viewer Chromium puts
// in a frame that loads a PDF: a browser component, not a site. A document
// render lets it fetch its own resources and run in its own target.
const pdfViewerOrigin = "chrome-extension://mhjfbmdgcfjbbpaeojofohoefgiehjai/"

// pdfViewerTarget reports a child target that is the PDF viewer frame of a
// document render: a frame with no opener, created for the viewer.
func pdfViewerTarget(targetType, targetURL, openerID string) bool {
	return targetType == "iframe" && openerID == "" && (targetURL == "" || strings.HasPrefix(targetURL, pdfViewerOrigin))
}

// renderHarnessRequestAllowed is the request allowlist of a render: the
// harness page itself, and its one file as the element the page uses
// requests it. A document frame is drawn by the built-in viewer, which
// loads its own resources from its own origin and may fetch the file again
// in pieces.
func renderHarnessRequestAllowed(kind, origin, raw, resourceType string) bool {
	if kind == "document" && strings.HasPrefix(raw, pdfViewerOrigin) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Scheme+"://"+u.Host != origin {
		return false
	}
	switch u.Path {
	case "/":
		return resourceType == "Document"
	case "/media":
		switch kind {
		case "image":
			return resourceType == "Image"
		case "document":
			return resourceType == "Document" || resourceType == "Other" || resourceType == "XHR" || resourceType == "Fetch"
		}
	}
	return false
}

// imageResponseType admits a picture to the image harness: the bytes must
// sniff as a raster image, whatever the header says, and never as a page.
func imageResponseType(header string, prefix []byte) (string, bool) {
	typeName := strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	text := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(prefix), string([]byte{0xEF, 0xBB, 0xBF}))))
	if strings.Contains(typeName, "html") || strings.Contains(typeName, "javascript") || strings.HasPrefix(text, "<!doctype") || strings.HasPrefix(text, "<html") || strings.HasPrefix(text, "<?xml") || strings.HasPrefix(text, "<svg") {
		return "", false
	}
	sniffed := http.DetectContentType(prefix)
	if strings.HasPrefix(sniffed, "image/") && sniffed != "image/svg+xml" {
		return sniffed, true
	}
	return "", false
}

// documentResponseType admits a document to the document harness: the
// bytes must be a PDF.
func documentResponseType(header string, prefix []byte) (string, bool) {
	if isPDF(prefix) {
		return "application/pdf", true
	}
	return "", false
}

// responseType is the relay's admission rule for the harness's kind.
func (h *moviePlaybackHarness) responseType(header string, prefix []byte) (string, bool) {
	switch h.kind {
	case "image":
		return imageResponseType(header, prefix)
	case "document":
		return documentResponseType(header, prefix)
	}
	return playbackResponseType(header, prefix)
}

// imageRenderHTML is the harness page for a picture: one image element fed
// by the relay, and a script that, once the element decoded it, draws it
// onto a canvas of the picture hash's grid and keeps the grey cells in
// window.__itembaRender for the sampler, which hashes them in Go.
func imageRenderHTML(nonce string) []byte {
	page := `<!doctype html><html><head><meta charset="utf-8"><title>ITEMBA render sample</title></head><body><img id="sample" src="/media" decoding="async" alt=""><script nonce="{{nonce}}">
(() => {
const img=document.getElementById('sample');const COLUMNS={{columns}},ROWS={{rows}};
window.__itembaRender={state:'loading'};
const done=(state,extra)=>{window.__itembaRender=Object.assign({state:state,width:img.naturalWidth||0,height:img.naturalHeight||0},extra||{});};
img.addEventListener('error',()=>done('error',{error:'media_error'}));
img.addEventListener('load',()=>{
 const finish=()=>{try{const canvas=document.createElement('canvas');canvas.width=COLUMNS;canvas.height=ROWS;const c=canvas.getContext('2d',{willReadFrequently:true});
  c.drawImage(img,0,0,COLUMNS,ROWS);const px=c.getImageData(0,0,COLUMNS,ROWS).data;const grey=[];
  for(let i=0;i<COLUMNS*ROWS;i++){const o=4*i;grey.push(Math.round(0.299*px[o]+0.587*px[o+1]+0.114*px[o+2]));}
  done('rendered',{cells:grey});}catch(e){done('rendered',{error:(e&&e.message)||'failed'});}};
 (typeof img.decode==='function'?img.decode():Promise.resolve()).then(finish,()=>done('error',{error:'decode_failed'}));});
})();</script></body></html>`
	return []byte(strings.NewReplacer("{{nonce}}", nonce, "{{columns}}", strconv.Itoa(lens.ImageHashColumns+1), "{{rows}}", strconv.Itoa(lens.ImageHashRows)).Replace(page))
}

// documentRenderHTML is the harness page for a document: one frame fed by
// the relay, the way the console's reader frames a PDF, and a script that
// records the frame's load. What the frame drew is judged by a screenshot,
// since a PDF document's content is not the page's to read.
func documentRenderHTML(nonce string, width, height int) []byte {
	page := `<!doctype html><html><head><meta charset="utf-8"><title>ITEMBA render sample</title></head><body><iframe id="sample" src="/media" width="{{width}}" height="{{height}}" frameborder="0"></iframe><script nonce="{{nonce}}">
(() => {
const frame=document.getElementById('sample');
window.__itembaRender={state:'loading'};
frame.addEventListener('load',()=>{window.__itembaRender={state:'loaded',width:frame.clientWidth||0,height:frame.clientHeight||0};});
frame.addEventListener('error',()=>{window.__itembaRender={state:'error',error:'frame_error'};});
})();</script></body></html>`
	return []byte(strings.NewReplacer("{{nonce}}", nonce, "{{width}}", strconv.Itoa(width), "{{height}}", strconv.Itoa(height)).Replace(page))
}

// visibleShare measures a screenshot: the share of its pixels whose grey
// level is more than 24 levels off the most common one, which a blank
// frame leaves at zero and a drawn page of text raises to a few percent.
func visibleShare(screenshot []byte) (share float64, width, height int, err error) {
	img, err := png.Decode(bytes.NewReader(screenshot))
	if err != nil {
		return 0, 0, 0, err
	}
	bounds := img.Bounds()
	width, height = bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 {
		return 0, width, height, nil
	}
	var histogram [256]int
	greys := make([]uint8, 0, width*height)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			grey := uint8((0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257)
			histogram[grey]++
			greys = append(greys, grey)
		}
	}
	dominant := 0
	for level, count := range histogram {
		if count > histogram[dominant] {
			dominant = level
		}
	}
	off := 0
	for _, grey := range greys {
		if d := int(grey) - dominant; d > 24 || d < -24 {
			off++
		}
	}
	return float64(off) / float64(len(greys)), width, height, nil
}

// cellsImage builds the grid image a render page's grey cells describe.
func cellsImage(cells []int) image.Image {
	if len(cells) != lens.VideoFrameCells {
		return nil
	}
	img := image.NewGray(image.Rect(0, 0, lens.ImageHashColumns+1, lens.ImageHashRows))
	for i, v := range cells {
		img.Pix[i] = uint8(min(max(v, 0), 255))
	}
	return img
}

// runRenderSample drives one render in an isolated browser already
// launched: every request is intercepted and only the harness's own page
// and its one file pass; the page's state is read until the picture is
// drawn or the document frame loaded, or the deadline ends it.
func runRenderSample(ctx context.Context, browser playbackBrowser, source VerifiedSource, origin, kind string, result *RenderCheck) *RenderCheck {
	var isolationFailed atomic.Bool
	var mediaMu sync.Mutex
	mediaStatus, mediaType, mediaResource := 0, "", ""
	browser.OnEvent(func(method string, data json.RawMessage) {
		switch method {
		case "Fetch.requestPaused":
			var p struct {
				RequestID string `json:"requestId"`
				Type      string `json:"resourceType"`
				Request   struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			if json.Unmarshal(data, &p) != nil || p.RequestID == "" {
				isolationFailed.Store(true)
				return
			}
			call, params := "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"}
			if renderHarnessRequestAllowed(kind, origin, p.Request.URL, p.Type) {
				call, params = "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}
			}
			if browser.CallAsync(call, params) != nil {
				isolationFailed.Store(true)
			}
		case "Network.responseReceived":
			var p struct {
				Type     string `json:"type"`
				Response struct {
					URL      string `json:"url"`
					Status   int    `json:"status"`
					MimeType string `json:"mimeType"`
				} `json:"response"`
			}
			if json.Unmarshal(data, &p) == nil && p.Response.URL == origin+"/media" {
				mediaMu.Lock()
				if mediaStatus == 0 || p.Type == "Document" || p.Type == "Image" {
					mediaStatus, mediaType, mediaResource = p.Response.Status, strings.ToLower(strings.Split(p.Response.MimeType, ";")[0]), p.Type
				}
				mediaMu.Unlock()
			}
		case "Page.windowOpen":
			isolationFailed.Store(true)
		case "Target.attachedToTarget", "Target.targetCreated":
			var p struct {
				Target struct {
					ID       string `json:"targetId"`
					OpenerID string `json:"openerId"`
					Type     string `json:"type"`
					URL      string `json:"url"`
				} `json:"targetInfo"`
			}
			if json.Unmarshal(data, &p) == nil && p.Target.ID != "" && (method == "Target.attachedToTarget" || p.Target.OpenerID != "") {
				if kind == "document" && pdfViewerTarget(p.Target.Type, p.Target.URL, p.Target.OpenerID) {
					// The PDF viewer frame is the browser drawing the document;
					// it is left to run.
					return
				}
				isolationFailed.Store(true)
				_ = browser.CallAsync("Target.closeTarget", map[string]any{"targetId": p.Target.ID})
			}
		}
	})
	// A picture render pauses and closes any child target, as a sample does;
	// a document render must let the viewer frame run.
	commands := []struct {
		method string
		params any
	}{
		{"Page.enable", map[string]any{}},
		{"Runtime.enable", map[string]any{}},
		{"Network.enable", map[string]any{}},
		{"Network.setCacheDisabled", map[string]any{"cacheDisabled": true}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": kind != "document", "flatten": true}},
		{"Target.setDiscoverTargets", map[string]any{"discover": true}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": freePlaybackIntegrityJS}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}},
	}
	for _, command := range commands {
		if browser.Call(ctx, command.method, command.params, nil) != nil {
			result.Status, result.Detail = "unconfirmed", "The browser could not enforce the isolated render."
			return result
		}
	}
	started := time.Now()
	if browser.Call(ctx, "Page.navigate", map[string]any{"url": origin + "/"}, nil) != nil {
		result.Status, result.Detail = "unconfirmed", "The isolated render harness did not open."
		return result
	}
	noun := "picture"
	if kind == "document" {
		noun = "document"
	}
	media := func() (int, string, string) {
		mediaMu.Lock()
		defer mediaMu.Unlock()
		return mediaStatus, mediaType, mediaResource
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result.Status = "not_rendered"
			result.HTTPStatus, result.MimeType, _ = media()
			result.Detail = "The " + noun + " did not load and draw within the bounded render; it is not rendered."
			return result
		case <-ticker.C:
			if isolationFailed.Load() {
				result.Status, result.Detail = "unconfirmed", "The isolated render could not be completed safely."
				return result
			}
			var response struct {
				Result struct {
					Value *renderSnapshot `json:"value"`
				} `json:"result"`
				Exception json.RawMessage `json:"exceptionDetails"`
			}
			if browser.Call(ctx, "Runtime.evaluate", map[string]any{"expression": renderSnapshotJS, "returnByValue": true}, &response) != nil {
				result.Status, result.Detail = "unconfirmed", "The browser stopped responding during the render."
				return result
			}
			if len(response.Exception) > 0 && string(response.Exception) != "null" {
				result.Status, result.Detail = "unconfirmed", "The render page's state could not be read."
				return result
			}
			if response.Result.Value == nil {
				continue
			}
			state := *response.Result.Value
			status, mimeType, resource := media()
			result.HTTPStatus, result.MimeType = status, mimeType
			switch {
			case state.State == "error":
				result.Status = "not_rendered"
				result.Detail = "The sandbox could not load the " + noun + " (" + firstNonEmptyString(state.Error, "no reason") + "); it is not rendered."
				if status >= 400 {
					result.Detail = fmt.Sprintf("The harness refused or could not relay the %s's bytes (HTTP %d): the host did not answer in time, or the bytes were not a %s it may render; it is not rendered.", noun, status, noun)
				}
				return result
			case kind == "image" && state.State == "rendered":
				result.Milliseconds = time.Since(started).Milliseconds()
				result.Width, result.Height = state.Width, state.Height
				if state.Width <= 0 || state.Height <= 0 {
					result.Status, result.Detail = "unconfirmed", "The image element loaded but reports no size; nothing proves it drew."
					return result
				}
				result.Status = "rendered"
				result.Detail = fmt.Sprintf("The sandbox decoded and drew the picture at %d×%d in %.1f s.", state.Width, state.Height, float64(result.Milliseconds)/1000)
				if img := cellsImage(state.Cells); img != nil {
					hash := lens.ImageFingerprint(img)
					if hash.Valid() {
						result.Fingerprint = &hash
						if source.ImageCheck != nil && source.ImageCheck.Fingerprint.Valid() {
							if c := lens.CompareFingerprints(hash, *source.ImageCheck.Fingerprint); c.Known {
								result.HashDistance = c.Distance
								result.HashAgrees = "no"
								result.Detail += fmt.Sprintf(" The drawn pixels hash differently from the file the probe read (distance %.2f).", c.Distance)
								if c.Same() {
									result.HashAgrees = "yes"
									result.Detail = fmt.Sprintf("The sandbox decoded and drew the picture at %d×%d in %.1f s, and the drawn pixels hash as the probe said (distance %.2f).", state.Width, state.Height, float64(result.Milliseconds)/1000, c.Distance)
								}
							}
						}
					}
				} else if state.Error != "" {
					result.Detail += " Its pixels could not be read back (" + state.Error + ")."
				}
				return result
			case kind == "document" && state.State == "loaded":
				result.FrameLoaded = true
				result.Width, result.Height = state.Width, state.Height
				// The frame loaded; give the viewer a moment to draw, then
				// look at the sandbox.
				select {
				case <-ctx.Done():
				case <-time.After(renderSettle):
				}
				var shot struct {
					Data string `json:"data"`
				}
				if browser.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"}, &shot) != nil || shot.Data == "" {
					result.Status, result.Detail = "unconfirmed", "The document frame loaded but the sandbox could not be screenshotted; nothing proves it drew."
					return result
				}
				raw, err := base64.StdEncoding.DecodeString(shot.Data)
				if err != nil {
					result.Status, result.Detail = "unconfirmed", "The document frame loaded but the screenshot could not be read; nothing proves it drew."
					return result
				}
				share, _, _, err := visibleShare(raw)
				if err != nil {
					result.Status, result.Detail = "unconfirmed", "The document frame loaded but the screenshot could not be decoded; nothing proves it drew."
					return result
				}
				result.Milliseconds = time.Since(started).Milliseconds()
				result.VisibleShare = float64(int(share*1000)) / 1000
				status, mimeType, resource = media()
				result.HTTPStatus, result.MimeType = status, mimeType
				documentLoaded := resource == "Document" && mimeType == "application/pdf" && (status == 200 || status == 206)
				switch {
				case documentLoaded && share >= renderVisibleShare:
					result.Status = "rendered"
					result.Detail = fmt.Sprintf("The sandbox framed the PDF and drew it: %d%% of the frame is off the background after %.1f s.", int(share*100+0.5), float64(result.Milliseconds)/1000)
				case documentLoaded:
					result.Status = "unconfirmed"
					result.Detail = fmt.Sprintf("The sandbox framed the PDF but the frame stayed blank (%.1f%% of it drawn); nothing proves the page rendered.", share*100)
				case status >= 400:
					result.Status = "not_rendered"
					result.Detail = fmt.Sprintf("The harness refused or could not relay the document's bytes (HTTP %d): the host did not answer in time, or the bytes were not a PDF; it is not rendered.", status)
				default:
					result.Status = "unconfirmed"
					result.Detail = "The frame loaded something that is not a PDF document (" + firstNonEmptyString(mimeType, "unknown type") + "); nothing proves the document rendered."
				}
				return result
			}
		}
	}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// renderInSandbox renders one source: the harness relays its file through
// Shield, a throwaway browser opens the harness page, and the run judges
// what it drew. The source's DirectStreamURL is the file rendered.
func (s *Scout) renderInSandbox(ctx context.Context, source VerifiedSource, kind, region, country string) *RenderCheck {
	result := &RenderCheck{
		Status: "unconfirmed", Method: renderMethod(kind), Country: movieCountry(country), RequestedRegion: strings.ToUpper(strings.TrimSpace(region)),
		CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.DirectStreamURL, Detail: "The sandbox render could not be confirmed.",
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	// An e-book or office file is converted to a PDF on this host first,
	// and the harness serves that PDF as the document.
	localFile := ""
	if kind == "document" {
		if format := convert.FormatOf(source.DirectStreamURL); format != "" && format != "pdf" {
			converted, detail := convertForRender(ctx, client, hostConverter(), source.DirectStreamURL, format)
			if converted.Path == "" {
				result.Status, result.Detail = "unsupported", detail
				return result
			}
			localFile = converted.Path
			result.Converted = detail
		}
	}
	origin, closeHarness, outcome, err := startProtectedHarness(ctx, source, client, s.shield, kind, localFile)
	if err != nil {
		result.Detail = "The isolated render harness could not be prepared."
		return result
	}
	defer closeHarness()
	checkCtx, cancel := context.WithTimeout(ctx, renderDeadlineFor(kind))
	defer cancel()
	browser, err := launchFreePlaybackBrowser(checkCtx)
	if err != nil {
		result.Detail = "An isolated browser could not be started for the render."
		return result
	}
	defer browser.Close()
	runRenderSample(checkCtx, browser, source, origin, kind, result)
	if reason := outcome(); reason != "" {
		result.Status, result.Reason = "unconfirmed", reason
		result.Detail = "A render resource was blocked by ad protection. Protection remained enabled; the file was not rendered."
	}
	return result
}

// renderSources renders up to maxRenderSamples of the results that
// eligible admits, in their order, each as the source eligible returns
// (which may point the harness at a thumbnail), and stores the check on the
// result. It returns how many were attempted and how many rendered.
func (s *Scout) renderSources(ctx context.Context, results []VerifiedSource, kind, region, country string, eligible func(VerifiedSource) (VerifiedSource, bool)) (attempted, rendered int) {
	probe := s.renderProbe
	if probe == nil {
		probe = s.renderInSandbox
	}
	var chosen []int
	targets := map[int]VerifiedSource{}
	for i := range results {
		if len(chosen) == maxRenderSamples {
			break
		}
		if target, ok := eligible(results[i]); ok {
			chosen = append(chosen, i)
			targets[i] = target
		}
	}
	if len(chosen) == 0 || ctx.Err() != nil {
		return 0, 0
	}
	checkCtx, cancel := context.WithTimeout(ctx, renderDeadlineFor(kind)+2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, idx := range chosen {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.playbackSlots != nil {
				select {
				case s.playbackSlots <- struct{}{}:
					defer func() { <-s.playbackSlots }()
				case <-checkCtx.Done():
					return
				}
			}
			if checkCtx.Err() != nil {
				return
			}
			check := probe(checkCtx, targets[i], kind, region, country)
			if check == nil {
				return
			}
			mu.Lock()
			results[i].RenderCheck = check
			attempted++
			if check.Status == "rendered" {
				rendered++
			}
			mu.Unlock()
		}(idx)
	}
	wg.Wait()
	return attempted, rendered
}

// renderablePicture admits a decoded free picture to the render, pointing
// the harness at the thumbnail when the host gave one, since the original
// may be far larger than the harness relays; a small original is rendered
// as it is.
func renderablePicture(source VerifiedSource) (VerifiedSource, bool) {
	if source.ImageCheck == nil || source.ImageCheck.Status != "decoded" || source.AccessType != "free" || source.DirectStreamURL == "" {
		return source, false
	}
	target := source
	if source.Thumbnail != "" {
		if _, ok := safeAvailabilityURL(source.Thumbnail); ok {
			target.DirectStreamURL = source.Thumbnail
			return target, true
		}
	}
	if source.ImageCheck.Bytes > 0 && source.ImageCheck.Bytes <= moviePlaybackResponseLimit {
		return target, true
	}
	return source, false
}

// renderableDocument admits a parsed free PDF to the render, and a free
// e-book or office file the probe could not read when this host has a
// converter for it, which turns it into the PDF the sandbox frames.
func renderableDocument(source VerifiedSource) (VerifiedSource, bool) {
	if source.DocumentCheck == nil || source.AccessType != "free" || source.DirectStreamURL == "" {
		return source, false
	}
	if source.DocumentCheck.Status == "parsed" && source.DocumentCheck.Format == "pdf" {
		return source, true
	}
	if source.DocumentCheck.Status == "unsupported" {
		if _, ok := hostConverter().Supports(convert.FormatOf(source.DirectStreamURL)); ok {
			return source, true
		}
	}
	return source, false
}

// convertForRender fetches an e-book or office file within the conversion
// limit and converts it to a PDF on this host. It returns the result and
// a line saying how it went, which is the detail when it did not.
func convertForRender(ctx context.Context, client *http.Client, converter *convert.Converter, raw, format string) (convert.Result, string) {
	upper := strings.ToUpper(format)
	if _, ok := converter.Supports(format); !ok {
		if !converter.Available() {
			return convert.Result{}, "No converter on this host turns " + upper + " into a PDF (Calibre's ebook-convert, DjVuLibre's ddjvu or LibreOffice's soffice would); the file is not rendered."
		}
		return convert.Result{}, "No converter on this host takes " + upper + "; the file is not rendered."
	}
	data, status, _, err := fetchImageBytes(ctx, client, raw, convertInputLimit, true)
	if err != nil {
		if status == http.StatusTooManyRequests {
			return convert.Result{}, "The host rate-limited the fetch (HTTP 429), so the " + upper + " was not converted this time."
		}
		return convert.Result{}, "The " + upper + " could not be fetched for conversion: " + err.Error() + "."
	}
	result, err := converter.Convert(ctx, format, data)
	if err != nil {
		return convert.Result{}, "The " + upper + " could not be converted to a PDF: " + strings.TrimPrefix(err.Error(), "convert: ") + "."
	}
	if result.Cached {
		return result, fmt.Sprintf("%s converted to PDF by %s earlier (cached).", upper, result.Converter)
	}
	return result, fmt.Sprintf("%s converted to PDF by %s in %.1f s.", upper, result.Converter, result.Seconds)
}

// renderStatusRank orders rendered sources first.
func renderStatusRank(source VerifiedSource) int {
	if source.RenderCheck != nil && source.RenderCheck.Status == "rendered" {
		return 1
	}
	return 0
}
