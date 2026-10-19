package cdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBrowserEnv marks a child process of the test binary as a stand-in for a
// browser that never opens its DevTools port. Its value picks the behaviour.
const fakeBrowserEnv = "ITEMBA_CDP_FAKE_BROWSER"

const (
	// fakeBrowserHangs stays alive until killed, like Chromium on a host too
	// cold to answer.
	fakeBrowserHangs = "hangs"
	// fakeBrowserWrapper starts a grandchild and stays in front of it, like a
	// launcher script that runs the browser without exec'ing it.
	fakeBrowserWrapper = "wrapper"
	// fakeBrowserGrandchild listens on a TCP port and reports its address
	// through the file named by fakeGrandchildAddrEnv.
	fakeBrowserGrandchild = "grandchild"
)

// fakeGrandchildAddrEnv names the file the grandchild writes its listening
// address to.
const fakeGrandchildAddrEnv = "ITEMBA_CDP_FAKE_GRANDCHILD_ADDR"

func TestMain(m *testing.M) {
	mode := os.Getenv(fakeBrowserEnv)
	if mode == "" {
		os.Exit(m.Run())
	}
	if err := runFakeBrowser(mode); err != nil {
		fmt.Fprintln(os.Stderr, "fake browser:", err)
		os.Exit(1)
	}
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}

// runFakeBrowser sets up the stand-in's behaviour; the caller then stays
// alive until killed.
func runFakeBrowser(mode string) error {
	switch mode {
	case fakeBrowserWrapper:
		self, err := os.Executable()
		if err != nil {
			return err
		}
		child := exec.Command(self)
		child.Env = append(os.Environ(), fakeBrowserEnv+"="+fakeBrowserGrandchild)
		return child.Start()
	case fakeBrowserGrandchild:
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		// The listener stays open until the process dies; whether a dial
		// succeeds tells the test whether the grandchild is alive.
		path := os.Getenv(fakeGrandchildAddrEnv)
		if err := os.WriteFile(path+".tmp", []byte(listener.Addr().String()), 0o600); err != nil {
			return err
		}
		return os.Rename(path+".tmp", path)
	}
	return nil
}

func TestLaunchStopsAtTheDeadlineAndReapsTheBrowser(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_BIN", self)
	t.Setenv(fakeBrowserEnv, fakeBrowserHangs)
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
	waitForProfileRemoval(t, id, "the failed launch")
}

func TestCloseTakesDownTheWholeProcessTree(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	addrFile := filepath.Join(t.TempDir(), "grandchild-addr")
	t.Setenv("CHROME_BIN", self)
	t.Setenv(fakeBrowserEnv, fakeBrowserWrapper)
	t.Setenv(fakeGrandchildAddrEnv, addrFile)
	id := fmt.Sprintf("cdp-tree-%d", time.Now().UnixNano())

	// The stand-in never opens its DevTools port, so the launch runs until its
	// context ends; Launch then closes what it started before returning.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launched := make(chan error, 1)
	go func() {
		inst, err := Launch(ctx, LaunchOptions{ID: id})
		if err == nil {
			inst.Close()
		}
		launched <- err
	}()

	addr := waitForGrandchildAddr(t, addrFile)
	if err := dial(addr); err != nil {
		t.Fatalf("the grandchild is not reachable at %s before Close: %v", addr, err)
	}

	cancel()
	select {
	case err := <-launched:
		if err == nil {
			t.Fatal("launch succeeded against a browser with no DevTools endpoint")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("launch did not return after its context was cancelled")
	}

	// The wrapper was killed directly; the grandchild only dies if Close
	// reached the whole tree, and its listener closes with it.
	deadline := time.Now().Add(10 * time.Second)
	for dial(addr) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the grandchild at %s survived Close", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitForProfileRemoval(t, id, "Close")
}

// waitForGrandchildAddr returns the address the stand-in's grandchild listens on.
func waitForGrandchildAddr(t *testing.T, addrFile string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if addr, err := os.ReadFile(addrFile); err == nil {
			return string(addr)
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in's grandchild never reported its address")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// dial reports whether something accepts connections at addr.
func dial(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// waitForProfileRemoval fails the test unless the ephemeral profile of the
// browser with the given id disappears. The profile is removed only once the
// killed process has been reaped, so its disappearance proves the browser did
// not outlive what killed it.
func waitForProfileRemoval(t *testing.T, id, killer string) {
	t.Helper()
	pattern := filepath.Join(os.TempDir(), "itemba-vault-"+id+"-*")
	deadline := time.Now().Add(10 * time.Second)
	for {
		matches, _ := filepath.Glob(pattern)
		if len(matches) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ephemeral profile %v survived %s", matches, killer)
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
