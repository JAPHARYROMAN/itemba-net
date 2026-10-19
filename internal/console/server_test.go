package console

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

// slack is the allowance over a bound before a test calls the server late.
const slack = 3 * time.Second

// newTestConsole builds a console over offline subsystems. The search is
// replaced so no provider is contacted; nil keeps the scout's own.
func newTestConsole(t *testing.T, search SearchFunc) (*Server, *shield.Shield) {
	t.Helper()
	sh := shield.New()
	sc := scout.New(sh, "http://127.0.0.1:0")
	t.Cleanup(sc.Close)
	vlt := vault.NewManager(sh)
	t.Cleanup(vlt.Destroy)
	srv := New(Dependencies{Shield: sh, Scout: sc, Vault: vlt, Metrics: metrics.New(), Search: search})
	return srv, sh
}

// serveTestConsole listens on a loopback port with the production bounds.
func serveTestConsole(t *testing.T, srv *Server) (string, *http.Server) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := srv.HTTPServer(listener.Addr().String())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String(), server
}

func dialTestConsole(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, slack)
}

// readUntilClosed reads conn until the server closes it and reports how long
// that took and what arrived. A connection still open after bound+slack is
// the error; a reset counts as closed.
func readUntilClosed(conn net.Conn, bound time.Duration) (time.Duration, []byte, error) {
	started := time.Now()
	_ = conn.SetReadDeadline(started.Add(bound + slack))
	data, err := io.ReadAll(conn)
	elapsed := time.Since(started)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return elapsed, data, fmt.Errorf("connection still open after %s", elapsed.Round(time.Millisecond))
	}
	return elapsed, data, nil
}

// waitForThreat records a threat and reports whether the websocket delivers
// it before readErr reports the connection closed or the slack runs out.
func waitForThreat(sh *shield.Shield, messages <-chan []byte, readErr <-chan error) error {
	sh.RecordEvent(shield.ThreatEvent{Layer: shield.Layer1DNS, Category: "test", Target: "https://ads.example/pixel"})
	deadline := time.After(slack)
	for {
		select {
		case msg := <-messages:
			if bytes.Contains(msg, []byte(`"type":"threat"`)) {
				return nil
			}
		case err := <-readErr:
			return fmt.Errorf("closed while waiting for the threat: %w", err)
		case <-deadline:
			return errors.New("the threat did not reach the websocket")
		}
	}
}

// readWebSocket drains ws in the background, which also answers the server's
// pings, and reports every message and the final error.
func readWebSocket(ws *websocket.Conn) (<-chan []byte, <-chan error) {
	messages := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case messages <- msg:
			default:
			}
		}
	}()
	return messages, readErr
}

// TestHTTPServerBoundsSlowClientsWithoutCuttingLongWorkOrTheWebSocket proves
// the console's connection bounds at their real values: a client that never
// finishes its headers, never finishes its body or holds an idle keep-alive
// connection is closed within the bound, while a request whose handler runs
// longer than every bound still completes and a websocket held open past
// them still delivers. The clients run at once, so the test takes about as
// long as the longest bound.
func TestHTTPServerBoundsSlowClientsWithoutCuttingLongWorkOrTheWebSocket(t *testing.T) {
	t.Parallel()
	longest := max(ReadHeaderTimeout, ReadTimeout, IdleTimeout)
	// The search outlives every server bound to prove none reaches a handler.
	searchFor := longest + time.Second
	var interrupted atomic.Bool
	search := func(ctx context.Context, query, region string, _ scout.SearchOptions) scout.SearchReport {
		select {
		case <-time.After(searchFor):
		case <-ctx.Done():
			interrupted.Store(true)
		}
		return scout.SearchReport{Query: query, Region: region}
	}
	srv, sh := newTestConsole(t, search)
	addr, server := serveTestConsole(t, srv)

	if server.ReadHeaderTimeout != ReadHeaderTimeout || server.ReadTimeout != ReadTimeout || server.IdleTimeout != IdleTimeout {
		t.Fatalf("server bounds %s/%s/%s, want %s/%s/%s", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout, ReadHeaderTimeout, ReadTimeout, IdleTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatal("WriteTimeout must stay unset: it would cut the search stream and the media relay")
	}

	errs := make(chan error, 8)
	var wg sync.WaitGroup
	run := func(name string, client func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client(); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

	run("unfinished headers", func() error {
		conn, err := dialTestConsole(addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "GET /api/status HTTP/1.1\r\nHost: itemba\r\n"); err != nil {
			return err
		}
		elapsed, _, err := readUntilClosed(conn, ReadHeaderTimeout)
		if err != nil {
			return err
		}
		if elapsed < ReadHeaderTimeout/2 {
			return fmt.Errorf("closed after %s, before the header timeout could have passed", elapsed.Round(time.Millisecond))
		}
		return nil
	})

	run("unfinished body", func() error {
		conn, err := dialTestConsole(addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		request := "POST /api/scout/search HTTP/1.1\r\nHost: itemba\r\nContent-Type: application/json\r\nContent-Length: 64\r\n\r\n{\"query\":"
		if _, err := io.WriteString(conn, request); err != nil {
			return err
		}
		elapsed, _, err := readUntilClosed(conn, ReadTimeout)
		if err != nil {
			return err
		}
		if elapsed < ReadTimeout/2 {
			return fmt.Errorf("closed after %s, before the read timeout could have passed", elapsed.Round(time.Millisecond))
		}
		return nil
	})

	run("idle keep-alive", func() error {
		conn, err := dialTestConsole(addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "GET /api/status HTTP/1.1\r\nHost: itemba\r\n\r\n"); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(slack))
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("status route returned %d", response.StatusCode)
		}
		if response.Close {
			return errors.New("a complete request must leave the connection alive, else the idle bound is never exercised")
		}
		elapsed, _, err := readUntilClosed(conn, IdleTimeout)
		if err != nil {
			return err
		}
		if elapsed < IdleTimeout/2 {
			return fmt.Errorf("closed after %s, before the idle timeout could have passed", elapsed.Round(time.Millisecond))
		}
		return nil
	})

	run("long request", func() error {
		client := &http.Client{Timeout: searchFor + slack}
		started := time.Now()
		response, err := client.Post("http://"+addr+"/api/scout/search", "application/json", strings.NewReader(`{"query":"Sintel","region":"ALL"}`))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d: %s", response.StatusCode, body)
		}
		if elapsed := time.Since(started); elapsed < searchFor {
			return fmt.Errorf("answered after %s, before the %s search finished", elapsed.Round(time.Millisecond), searchFor)
		}
		if !bytes.Contains(body, []byte(`"query":"Sintel"`)) {
			return fmt.Errorf("unexpected report: %s", body)
		}
		if interrupted.Load() {
			return errors.New("the server bounds cancelled the search's context")
		}
		return nil
	})

	run("websocket", func() error {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/api/ws", nil)
		if err != nil {
			return err
		}
		defer ws.Close()
		messages, readErr := readWebSocket(ws)
		// Hold the connection past every server bound before using it.
		select {
		case err := <-readErr:
			return fmt.Errorf("closed early: %w", err)
		case <-time.After(longest + time.Second):
		}
		return waitForThreat(sh, messages, readErr)
	})

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestWebSocketDropsAPeerThatStopsAnsweringAndKeepsOneThatDoes shortens the
// websocket's pong wait and proves a peer that reads nothing, so answers no
// ping, is closed within it, while a peer that answers stays connected well
// past it and still receives.
func TestWebSocketDropsAPeerThatStopsAnsweringAndKeepsOneThatDoes(t *testing.T) {
	t.Parallel()
	srv, sh := newTestConsole(t, nil)
	srv.wsPongWait = 600 * time.Millisecond
	addr, _ := serveTestConsole(t, srv)

	silent, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/api/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	// Raw bytes from the connection see the server's pings without the
	// websocket layer answering them, like a peer whose machine has gone.
	elapsed, raw, err := readUntilClosed(silent.NetConn(), srv.wsPongWait)
	if err != nil {
		t.Fatalf("silent websocket peer: %v", err)
	}
	if elapsed < srv.wsPongWait/2 {
		t.Fatalf("silent websocket peer closed after %s, before the pong wait could have passed", elapsed.Round(time.Millisecond))
	}
	if !bytes.Contains(raw, []byte{0x89, 0x00}) {
		t.Fatalf("the server must ping a silent peer before dropping it; got % x", raw)
	}

	live, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/api/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	messages, readErr := readWebSocket(live)
	select {
	case err := <-readErr:
		t.Fatalf("answering websocket peer closed early: %v", err)
	case <-time.After(3 * srv.wsPongWait):
	}
	if err := waitForThreat(sh, messages, readErr); err != nil {
		t.Fatalf("answering websocket peer: %v", err)
	}
}
