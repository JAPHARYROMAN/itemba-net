package scout

// A live diagnostic of the document render, kept for the next time a
// browser or a host changes: it starts the harness for an Archive PDF,
// reads the relay once, then runs the render with every browser event
// logged. Set ITEMBA_RENDER_LIVE to run it.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/lens"
)

type loggingBrowser struct {
	playbackBrowser
	t *testing.T
}

func (b *loggingBrowser) OnEvent(h cdp.EventHandler) {
	b.playbackBrowser.OnEvent(func(method string, data json.RawMessage) {
		if method != "Network.dataReceived" && method != "Network.loadingFinished" && method != "Runtime.consoleAPICalled" {
			s := string(data)
			if len(s) > 400 {
				s = s[:400]
			}
			b.t.Logf("event %s %s", method, s)
		}
		h(method, data)
	})
}

func TestLiveDocumentRenderInTheSandbox(t *testing.T) {
	if os.Getenv("ITEMBA_RENDER_LIVE") == "" {
		t.Skip("set ITEMBA_RENDER_LIVE")
	}
	source := VerifiedSource{CandidateSource: CandidateSource{Kind: lens.TypeDocument, URL: "https://archive.org/details/aliceinwonderlan00carriala", DirectStreamURL: "https://archive.org/download/aliceinwonderlan00carriala/aliceinwonderlan00carriala.pdf", AccessType: "free"}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	origin, closeHarness, outcome, err := startProtectedHarness(ctx, source, nil, nil, "document", "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeHarness()
	started := time.Now()
	resp, err := http.Get(origin + "/media")
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("relay answered %d %s %d bytes in %s; outcome %q; headers %v", resp.StatusCode, resp.Header.Get("Content-Type"), len(body), time.Since(started), outcome(), resp.Header)
	browser, err := launchFreePlaybackBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	result := runRenderSample(ctx, &loggingBrowser{playbackBrowser: browser, t: t}, source, origin, "document", &RenderCheck{Method: MethodDocumentRender})
	t.Logf("result %+v", result)
}

// TestLiveEPUBRenderInTheSandbox converts an Archive EPUB with the
// converter this host has and renders the converted PDF in the sandbox:
// the whole document render path for an e-book. Set ITEMBA_RENDER_LIVE to
// run it; it skips on a host without a converter.
func TestLiveEPUBRenderInTheSandbox(t *testing.T) {
	if os.Getenv("ITEMBA_RENDER_LIVE") == "" {
		t.Skip("set ITEMBA_RENDER_LIVE")
	}
	if _, ok := hostConverter().Supports("epub"); !ok {
		t.Skip("no converter for EPUB on this host")
	}
	s := New(nil, "http://127.0.0.1:0")
	defer s.Close()
	source := VerifiedSource{CandidateSource: CandidateSource{Kind: lens.TypeDocument, URL: "https://archive.org/details/alicesadventur00carr", DirectStreamURL: "https://archive.org/download/alicesadventur00carr/alicesadventur00carr.epub", AccessType: "free"}, DocumentCheck: &DocumentCheck{Status: "unsupported", Format: "epub"}}
	if _, ok := renderableDocument(source); !ok {
		t.Fatal("an EPUB is renderable on a host with a converter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	started := time.Now()
	result := s.renderInSandbox(ctx, source, "document", "ALL", "TZ")
	t.Logf("result in %s: %+v", time.Since(started), result)
	if result.Status != "rendered" || result.Converted == "" {
		t.Fatalf("the converted EPUB renders in the sandbox: %+v", result)
	}
}
