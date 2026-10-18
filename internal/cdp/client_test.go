package cdp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBrowserEnv marks a child process of the test binary as a stand-in for a
// browser that never opens its DevTools port.
const fakeBrowserEnv = "ITEMBA_CDP_FAKE_BROWSER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeBrowserEnv) != "" {
		// Behave like Chromium on a host too cold to answer: stay alive until killed.
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLaunchStopsAtTheDeadlineAndReapsTheBrowser(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_BIN", self)
	t.Setenv(fakeBrowserEnv, "1")
	id := fmt.Sprintf("cdp-test-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	inst, err := Launch(ctx, LaunchOptions{ID: id})
	elapsed := time.Since(started)
	if err == nil {
		inst.Close()
		t.Fatal("launch succeeded against a browser with no DevTools endpoint")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("launch failed for another reason than its deadline: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("launch returned %v after it started, long past its 300ms deadline", elapsed)
	}

	// The profile is removed only once the killed process has been reaped, so
	// its disappearance proves the browser did not outlive the failed launch.
	pattern := filepath.Join(os.TempDir(), "itemba-vault-"+id+"-*")
	deadline := time.Now().Add(10 * time.Second)
	for {
		matches, _ := filepath.Glob(pattern)
		if len(matches) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ephemeral profile %v survived the failed launch", matches)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConnectDebuggerStopsAtTheDeadlineWhileTheEndpointHangs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A browser that accepted the connection but never answers.
		<-r.Context().Done()
	}))
	defer server.Close()
	inst := instanceFor(t, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := inst.connectDebugger(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1200*time.Millisecond {
		t.Fatalf("connectDebugger kept waiting %v on a 300ms deadline", elapsed)
	}
}

func TestConnectDebuggerGivesUpWhenNoPageTargetAppears(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Alive, but without a page target yet.
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	inst := instanceFor(t, server.URL)

	previous := pageTargetPollBudget
	pageTargetPollBudget = 300 * time.Millisecond
	defer func() { pageTargetPollBudget = previous }()

	started := time.Now()
	err := inst.connectDebugger(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("expected the poll budget to end the wait, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1200*time.Millisecond {
		t.Fatalf("connectDebugger polled %v on a 300ms budget", elapsed)
	}
}

func instanceFor(t *testing.T, serverURL string) *BrowserInstance {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return &BrowserInstance{ID: "test", DebugHost: parsed.Hostname(), DebugPort: port, closeCh: make(chan struct{})}
}
