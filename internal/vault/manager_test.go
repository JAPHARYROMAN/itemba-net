package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/itemba-net/core/internal/cdp"
	"github.com/itemba-net/core/internal/shield"
)

type navigationFixture struct {
	calls     int
	result    string
	callError error
}

func (b *navigationFixture) Call(_ context.Context, method string, _ any, result any) error {
	b.calls++
	if method != "Page.navigate" {
		return errors.New("unexpected browser command")
	}
	if b.callError != nil {
		return b.callError
	}
	return json.Unmarshal([]byte(b.result), result)
}

func TestInvalidTargetsNeverReachBrowser(t *testing.T) {
	for _, target := range []string{"", "example.com/watch", "//example.com/watch", "file:///tmp/video", "javascript:alert(1)", "data:text/html,hello", "https:///watch", "http://"} {
		t.Run(target, func(t *testing.T) {
			browser := &navigationFixture{result: `{}`}
			if err := navigateBrowser(context.Background(), browser, target); err == nil || browser.calls != 0 {
				t.Fatalf("invalid target reached browser: %v, calls %d", err, browser.calls)
			}
		})
	}
}

func TestNavigationRejectsProtocolErrorText(t *testing.T) {
	browser := &navigationFixture{result: `{"frameId":"frame","errorText":"net::ERR_BLOCKED_BY_CLIENT"}`}
	err := navigateBrowser(context.Background(), browser, "https://example.com/watch")
	if err == nil || !strings.Contains(err.Error(), "ERR_BLOCKED_BY_CLIENT") {
		t.Fatalf("browser navigation failure was reported as success: %v", err)
	}
}

func TestNavigationSuccessAndCancellation(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:8080/testbench/clean-cinema", "https://example.com/watch", "https://[::1]/watch"} {
		browser := &navigationFixture{result: `{"frameId":"frame"}`}
		if err := navigateBrowser(context.Background(), browser, target); err != nil || browser.calls != 1 {
			t.Fatalf("valid URL rejected: %s: %v", target, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	browser := &navigationFixture{result: `{}`}
	if err := navigateBrowser(ctx, browser, "https://example.com/"); !errors.Is(err, context.Canceled) || browser.calls != 0 {
		t.Fatalf("canceled navigation reached browser: %v, calls %d", err, browser.calls)
	}
}

func TestUnprotectedSessionRejectsNavigationAndControls(t *testing.T) {
	session := &Session{Browser: &cdp.BrowserInstance{}}
	ctx := context.Background()
	for _, operation := range []func() error{
		func() error { return session.Navigate(ctx, "https://example.com/", false) },
		func() error { return session.GoBack(ctx) },
		func() error { return session.GoForward(ctx) },
		func() error { return session.Reload(ctx) },
		func() error { _, err := session.IsolateTheater(ctx); return err },
		func() error { _, err := session.NegotiateWebRTC("offer"); return err },
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "protection is not ready") {
			t.Fatalf("unprotected session accepted an operation: %v", err)
		}
	}
	// This must return before attempting to write to the unconnected browser.
	session.DispatchInput(InputEvent{Type: "mousedown"})
}

func TestLaunchValidationDoesNotPublishSession(t *testing.T) {
	manager := NewManager(shield.New())
	if _, err := manager.LaunchOrNavigate(context.Background(), "file:///tmp/video", false, false); err == nil {
		t.Fatal("invalid launch target accepted")
	}
	if manager.ActiveSession() != nil || manager.Status().Active || manager.Status().AdBlockingEnabled {
		t.Fatal("failed launch published an active or protected session")
	}
}

// fakeCDP is an in-process DevTools page target. It answers every command with
// an empty result, except the methods it was told to leave unanswered.
type fakeCDP struct {
	server  *httptest.Server
	stalled map[string]bool
	mu      sync.Mutex
	methods []string
}

func newFakeCDP(t *testing.T, stalled ...string) *fakeCDP {
	f := &fakeCDP{stalled: make(map[string]bool)}
	for _, method := range stalled {
		f.stalled[method] = true
	}
	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var msg struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			f.mu.Lock()
			f.methods = append(f.methods, msg.Method)
			f.mu.Unlock()
			if f.stalled[msg.Method] {
				continue
			}
			if err := conn.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{}}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCDP) connect() (*cdp.BrowserInstance, error) {
	return cdp.Connect(context.Background(), "ws"+strings.TrimPrefix(f.server.URL, "http"))
}

func (f *fakeCDP) sawMethod(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, seen := range f.methods {
		if seen == method {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, limit time.Duration, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLaunchPublishesSessionWhenTheBrowserAnswers(t *testing.T) {
	fake := newFakeCDP(t)
	manager := NewManager(shield.New())
	manager.launch = func(context.Context, cdp.LaunchOptions) (*cdp.BrowserInstance, error) {
		return fake.connect()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := manager.LaunchOrNavigate(ctx, "https://example.com/watch", false, false)
	if err != nil {
		t.Fatalf("launch against an answering browser failed: %v", err)
	}
	if !status.Active || !status.AdBlockingEnabled || status.SessionID != "vlt-1" || status.CurrentURL != "https://example.com/watch" || status.Mode != "remote-attached" {
		t.Fatalf("unexpected status after launch: %+v", status)
	}
	for _, method := range []string{"Page.enable", "Fetch.enable", "Page.startScreencast", "Page.navigate"} {
		if !fake.sawMethod(method) {
			t.Fatalf("the browser never received %s", method)
		}
	}
	manager.Destroy()
	if manager.ActiveSession() != nil || manager.Status().Active {
		t.Fatal("destroy left a session active")
	}
}

func TestLaunchFailsFastWhenTheBrowserOutlivesTheDeadline(t *testing.T) {
	fake := newFakeCDP(t)
	var straggler atomic.Pointer[cdp.BrowserInstance]
	manager := NewManager(shield.New())
	manager.launch = func(context.Context, cdp.LaunchOptions) (*cdp.BrowserInstance, error) {
		// A browser that ignores the deadline and comes up long after it.
		time.Sleep(1500 * time.Millisecond)
		browser, err := fake.connect()
		if err == nil {
			straggler.Store(browser)
		}
		return browser, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	status, err := manager.LaunchOrNavigate(ctx, "https://example.com/watch", false, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("launch returned after %v, long past its 200ms deadline", elapsed)
	}
	if status.Active || manager.ActiveSession() != nil || manager.Status().Active {
		t.Fatal("a launch that missed its deadline published a session")
	}

	// The browser that arrives after the deadline is shut down, never adopted.
	waitFor(t, 5*time.Second, func() bool {
		browser := straggler.Load()
		return browser != nil && browser.IsClosed()
	}, "the browser that arrived after the deadline was not closed")
	if manager.ActiveSession() != nil {
		t.Fatal("the late browser was published as a session")
	}

	// A retry must not wait on the straggler.
	manager.launch = func(context.Context, cdp.LaunchOptions) (*cdp.BrowserInstance, error) {
		return fake.connect()
	}
	retryCtx, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	status, err = manager.LaunchOrNavigate(retryCtx, "https://example.com/watch", false, false)
	if err != nil || !status.Active || status.SessionID != "vlt-2" {
		t.Fatalf("retry after a missed deadline failed: %v, status %+v", err, status)
	}
	manager.Destroy()
}

func TestLaunchFailsFastWhenShieldSetupStalls(t *testing.T) {
	fake := newFakeCDP(t, "Page.enable")
	var browser atomic.Pointer[cdp.BrowserInstance]
	manager := NewManager(shield.New())
	manager.launch = func(context.Context, cdp.LaunchOptions) (*cdp.BrowserInstance, error) {
		b, err := fake.connect()
		if err == nil {
			browser.Store(b)
		}
		return b, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := manager.LaunchOrNavigate(ctx, "https://example.com/watch", false, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("launch returned after %v, long past its 200ms deadline", elapsed)
	}
	if manager.ActiveSession() != nil || manager.Status().Active {
		t.Fatal("a session whose protection never came up was published")
	}
	waitFor(t, 5*time.Second, func() bool {
		b := browser.Load()
		return b != nil && b.IsClosed()
	}, "the browser whose protection stalled was not closed")
}

func TestLaunchWithAnEndedContextStartsNoBrowser(t *testing.T) {
	manager := NewManager(shield.New())
	var launches atomic.Int32
	manager.launch = func(context.Context, cdp.LaunchOptions) (*cdp.BrowserInstance, error) {
		launches.Add(1)
		return nil, errors.New("must not be reached")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.LaunchOrNavigate(ctx, "https://example.com/watch", false, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if launches.Load() != 0 || manager.ActiveSession() != nil {
		t.Fatal("a cancelled launch started a browser")
	}
}
