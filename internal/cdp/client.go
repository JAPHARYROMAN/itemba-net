package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// TargetInfo represents a debugging target returned by /json/list or /json/new.
type TargetInfo struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type rpcMessage struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("CDP error (%d): %s", e.Code, e.Message)
}

// EventHandler is invoked asynchronously when a CDP event arrives.
type EventHandler func(method string, params json.RawMessage)

// BrowserInstance manages an isolated Chromium process (or lightweight target tab) and its CDP WebSocket connection.
type BrowserInstance struct {
	ID          string
	TargetID    string
	DebugPort   int
	DebugHost   string
	UserDataDir string
	ContainerID string
	Mode        string // "local-isolated", "docker-container", or "pooled-target"

	cmd         *exec.Cmd
	conn        *websocket.Conn
	writeMu     sync.Mutex
	nextID      atomic.Int64
	pending     sync.Map // map[int64]chan rpcMessage
	handlers    []EventHandler
	handMu      sync.RWMutex
	closed      atomic.Bool
	closeCh     chan struct{}
	isTabTarget bool
}

// LaunchOptions configures how an isolated Chromium instance is started.
type LaunchOptions struct {
	ID           string
	Width        int
	Height       int
	MuteAudio    bool
	HostResolver string
	UseDocker    bool
	DockerImage  string
	ExtraArgs    []string
}

// FindBrowserBinary locates an installed Chromium, Google Chrome, or Microsoft Edge binary.
func FindBrowserBinary() (string, error) {
	if envBin := os.Getenv("CHROME_BIN"); envBin != "" {
		if _, err := os.Stat(envBin); err == nil {
			return envBin, nil
		}
	}

	var candidates []string
	switch runtime.GOOS {
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		progFiles := os.Getenv("ProgramFiles")
		progFiles86 := os.Getenv("ProgramFiles(x86)")
		candidates = []string{
			filepath.Join(progFiles, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(progFiles86, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(progFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(progFiles86, "Microsoft", "Edge", "Application", "msedge.exe"),
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	default:
		candidates = []string{
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/microsoft-edge",
		}
	}

	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}

	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no Chromium/Chrome/Edge binary found; set CHROME_BIN environment variable")
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Launch starts either a Dockerized ephemeral vault container or a local isolated Chromium instance.
func Launch(ctx context.Context, opts LaunchOptions) (*BrowserInstance, error) {
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}

	port, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate debug port: %w", err)
	}

	inst := &BrowserInstance{
		ID:        opts.ID,
		DebugPort: port,
		DebugHost: "127.0.0.1",
		closeCh:   make(chan struct{}),
	}

	if opts.UseDocker {
		err := inst.launchDocker(ctx, opts)
		if err == nil {
			return inst, nil
		}
	}

	if err := inst.launchLocal(ctx, opts); err != nil {
		return nil, err
	}
	return inst, nil
}

// NewTabTarget opens an isolated page target inside an already-running BrowserInstance (takes <10ms, zero process overhead).
func (b *BrowserInstance) NewTabTarget(ctx context.Context) (*BrowserInstance, error) {
	if b.closed.Load() {
		return nil, errors.New("parent browser instance is closed")
	}

	client := &http.Client{Timeout: 3 * time.Second}
	newURL := fmt.Sprintf("http://%s:%d/json/new?%s", b.DebugHost, b.DebugPort, url.QueryEscape("about:blank"))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, newURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		// Fallback to GET for older Chromium builds
		resp, err = client.Get(newURL)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	var target TargetInfo
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return nil, err
	}
	if target.WebSocketDebuggerURL == "" {
		return nil, errors.New("empty websocket debugger url for new tab target")
	}

	tab := &BrowserInstance{
		ID:          b.ID + "-tab-" + target.ID,
		TargetID:    target.ID,
		DebugPort:   b.DebugPort,
		DebugHost:   b.DebugHost,
		Mode:        "pooled-target",
		closeCh:     make(chan struct{}),
		isTabTarget: true,
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 4 * time.Second,
		ReadBufferSize:   2 * 1024 * 1024,
		WriteBufferSize:  512 * 1024,
	}
	conn, _, err := dialer.DialContext(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		_ = tab.closeRemoteTarget()
		return nil, err
	}
	conn.SetReadLimit(16 * 1024 * 1024)
	tab.conn = conn
	go tab.readLoop()
	return tab, nil
}

func (b *BrowserInstance) closeRemoteTarget() error {
	if b.TargetID == "" {
		return nil
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	closeURL := fmt.Sprintf("http://%s:%d/json/close/%s", b.DebugHost, b.DebugPort, b.TargetID)
	resp, err := client.Get(closeURL)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
	return err
}

func (b *BrowserInstance) launchDocker(ctx context.Context, opts LaunchOptions) error {
	image := opts.DockerImage
	if image == "" {
		image = "itemba-vault:latest"
	}

	checkCmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("docker image %s not present: %w", image, err)
	}

	containerName := fmt.Sprintf("itemba-vault-%s", b.ID)
	args := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--shm-size=1g",
		"-p", fmt.Sprintf("127.0.0.1:%d:9222", b.DebugPort),
		image,
	}

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run failed: %s: %w", string(out), err)
	}

	b.ContainerID = strings.TrimSpace(string(out))
	if len(b.ContainerID) > 12 {
		b.ContainerID = b.ContainerID[:12]
	}
	b.Mode = "docker-container"

	if err := b.connectDebugger(ctx); err != nil {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
		return err
	}
	return nil
}

func (b *BrowserInstance) launchLocal(ctx context.Context, opts LaunchOptions) error {
	bin, err := FindBrowserBinary()
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("itemba-vault-%s-*", opts.ID))
	if err != nil {
		return fmt.Errorf("failed to create ephemeral profile dir: %w", err)
	}
	b.UserDataDir = tmpDir
	b.Mode = "local-isolated"

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", b.DebugPort),
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--user-data-dir=%s", tmpDir),
		"--headless=new",
		fmt.Sprintf("--window-size=%d,%d", opts.Width, opts.Height),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-client-side-phishing-detection",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-popup-blocking",
		"--autoplay-policy=no-user-gesture-required",
		"--hide-scrollbars",
		"--remote-allow-origins=*",
	}

	if opts.MuteAudio {
		args = append(args, "--mute-audio")
	}
	if opts.HostResolver != "" {
		args = append(args, fmt.Sprintf("--host-resolver-rules=%s", opts.HostResolver))
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, "about:blank")

	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to start browser binary %s: %w", bin, err)
	}
	b.cmd = cmd

	if err := b.connectDebugger(ctx); err != nil {
		b.Close()
		return err
	}
	return nil
}

func (b *BrowserInstance) connectDebugger(ctx context.Context) error {
	deadline := time.Now().Add(12 * time.Second)
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	listURL := fmt.Sprintf("http://%s:%d/json/list", b.DebugHost, b.DebugPort)

	var wsURL string
	var targetID string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(listURL)
		if err == nil {
			var targets []TargetInfo
			_ = json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()

			for _, t := range targets {
				if t.Type == "page" && t.WebSocketDebuggerURL != "" {
					wsURL = t.WebSocketDebuggerURL
					targetID = t.ID
					break
				}
			}
			if wsURL != "" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if wsURL == "" {
		return fmt.Errorf("timed out waiting for CDP page target on port %d", b.DebugPort)
	}
	b.TargetID = targetID

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		ReadBufferSize:   4 * 1024 * 1024,
		WriteBufferSize:  1 * 1024 * 1024,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial CDP websocket %s: %w", wsURL, err)
	}
	conn.SetReadLimit(32 * 1024 * 1024)
	b.conn = conn

	go b.readLoop()
	return nil
}

func (b *BrowserInstance) readLoop() {
	defer func() {
		b.Close()
	}()

	for {
		_, data, err := b.conn.ReadMessage()
		if err != nil {
			return
		}

		var msg rpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		if msg.ID != 0 {
			if ch, ok := b.pending.LoadAndDelete(msg.ID); ok {
				ch.(chan rpcMessage) <- msg
			}
			continue
		}

		if msg.Method != "" {
			b.handMu.RLock()
			handlers := make([]EventHandler, len(b.handlers))
			copy(handlers, b.handlers)
			b.handMu.RUnlock()

			for _, h := range handlers {
				h(msg.Method, msg.Params)
			}
		}
	}
}

// OnEvent registers an event listener for CDP events.
func (b *BrowserInstance) OnEvent(h EventHandler) {
	b.handMu.Lock()
	defer b.handMu.Unlock()
	b.handlers = append(b.handlers, h)
}

// Call invokes a CDP method and waits for its response.
func (b *BrowserInstance) Call(ctx context.Context, method string, params any, result any) error {
	if b.closed.Load() {
		return errors.New("CDP session closed")
	}

	id := b.nextID.Add(1)
	var rawParams json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = raw
	}

	req := rpcMessage{
		ID:     id,
		Method: method,
		Params: rawParams,
	}

	ch := make(chan rpcMessage, 1)
	b.pending.Store(id, ch)
	defer b.pending.Delete(id)

	b.writeMu.Lock()
	err := b.conn.WriteJSON(req)
	b.writeMu.Unlock()
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closeCh:
		return errors.New("CDP session closed")
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// CallAsync fires a CDP command without blocking for the response.
func (b *BrowserInstance) CallAsync(method string, params any) error {
	if b.closed.Load() {
		return errors.New("CDP session closed")
	}

	id := b.nextID.Add(1)
	var rawParams json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = raw
	}

	req := rpcMessage{
		ID:     id,
		Method: method,
		Params: rawParams,
	}

	b.writeMu.Lock()
	err := b.conn.WriteJSON(req)
	b.writeMu.Unlock()
	return err
}

// IsClosed returns true if the browser or tab target has been closed.
func (b *BrowserInstance) IsClosed() bool {
	return b.closed.Load()
}

// Close destroys the browser process, Docker container, or pooled tab target.
func (b *BrowserInstance) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	close(b.closeCh)

	if b.conn != nil {
		_ = b.conn.Close()
	}

	if b.isTabTarget {
		_ = b.closeRemoteTarget()
		return
	}

	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_, _ = b.cmd.Process.Wait()
	}

	if b.ContainerID != "" {
		_ = exec.Command("docker", "rm", "-f", fmt.Sprintf("itemba-vault-%s", b.ID)).Run()
	}

	if b.UserDataDir != "" {
		go func(dir string) {
			for i := 0; i < 5; i++ {
				if os.RemoveAll(dir) == nil {
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
		}(b.UserDataDir)
	}
}
