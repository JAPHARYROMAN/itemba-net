package vault

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
