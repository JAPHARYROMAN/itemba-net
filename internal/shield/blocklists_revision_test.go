package shield

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestEngineRevisionTracksRuleMutationsWithoutChangingMatching(t *testing.T) {
	engine := NewEngine()
	if engine.Revision() == 0 {
		t.Fatal("initialized rules require a revision")
	}
	for _, rule := range []string{"revision-allowed.example", "||second-revision.example^", "0.0.0.0 third-revision.example", "/revision-media/main.mp4", "||cdn.example/revision/segment.ts", "page.example##.revision-ad", "@@||revision-allowed.example^"} {
		before := engine.Revision()
		engine.AddCustomRule(rule)
		if got := engine.Revision(); got != before+1 {
			t.Fatalf("rule mutation %q: got revision%d want%d", rule, got, before+1)
		}
	}
	if engine.Inspect("https://revision-allowed.example/main.mp4", "Media").Blocked {
		t.Fatal("revision tracking changed exception priority")
	}
	if !engine.Inspect("https://second-revision.example/main.mp4", "Media").Blocked {
		t.Fatal("revision tracking changed domain protection")
	}
	if !engine.Inspect("https://media.example/revision-media/main.mp4", "Media").Blocked {
		t.Fatal("revision tracking changed custom media protection")
	}
	before := engine.Revision()
	engine.Stats()
	engine.HostResolverRules()
	engine.Inspect("https://content.example/main.mp4", "Media")
	if engine.Revision() != before {
		t.Fatal("read-only protection or telemetry advanced revision")
	}
}

func TestEngineRevisionDistinguishesNoOpsFromChangesWithEqualRuleCounts(t *testing.T) {
	engine := NewEngine()
	engine.AddCustomRule("same-count-revision.example")
	before, domains := engine.Revision(), engine.Stats().DNSSinkholeDomains
	engine.AddCustomRule("SAME-COUNT-REVISION.EXAMPLE")
	if engine.Revision() != before {
		t.Fatal("identical normalized map rule advanced revision")
	}
	// A replacement can change compiled rule evidence without changing counts.
	engine.AddCustomRule("0.0.0.0 same-count-revision.example")
	if engine.Revision() != before+1 || engine.Stats().DNSSinkholeDomains != domains {
		t.Fatal("same-count rule replacement did not advance revision")
	}
	if match := engine.Inspect("https://same-count-revision.example/video.mp4", "Media"); !match.Blocked || match.ListSource != "Custom Hosts Sinkhole" {
		t.Fatal("same-count replacement changed existing filter behavior")
	}
	engine.AddCustomRule("@@||exception-revision.example^")
	before = engine.Revision()
	for _, rule := range []string{"@@||EXCEPTION-REVISION.EXAMPLE^", "", "  ", "! comment", "# comment", "127.0.0.1 localhost", "site.example##  "} {
		engine.AddCustomRule(rule)
	}
	if engine.Revision() != before {
		t.Fatal("ignored rules or duplicate exception advanced revision")
	}
}

type revisionFixtureTransport func(*http.Request) (*http.Response, error)

func (fn revisionFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestEngineRevisionTracksRemoteListChangesButNotNoOpOrFailedSync(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	engine := NewEngine()
	list := "0.0.0.0 first-list-revision.example\n127.0.0.1 second-list-revision.example\n0.0.0.0 first-list-revision.example\n"
	failed := false
	http.DefaultTransport = revisionFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if failed {
			return nil, errors.New("fixture transport unavailable")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(list)), Request: req}, nil
	})
	before := engine.Revision()
	added, err := engine.SyncRemoteList(context.Background())
	if err != nil || added != 2 || engine.Revision() != before+1 {
		t.Fatalf("changed list did not advance one atomic batch revision: added%d revision%d err%v", added, engine.Revision(), err)
	}
	if !engine.Inspect("https://first-list-revision.example/ad.mp4", "Media").Blocked {
		t.Fatal("remote list no longer compiled its existing protection")
	}
	before = engine.Revision()
	if added, err := engine.SyncRemoteList(context.Background()); err != nil || added != 0 || engine.Revision() != before {
		t.Fatal("unchanged list/timestamp-only sync advanced protection revision")
	}
	list = "! no new compiled rules\n0.0.0.0 localhost\n"
	if added, err := engine.SyncRemoteList(context.Background()); err != nil || added != 0 || engine.Revision() != before {
		t.Fatal("empty list changed protection revision")
	}
	failed = true
	if added, err := engine.SyncRemoteList(context.Background()); err == nil || added != 0 || engine.Revision() != before {
		t.Fatal("failed list fetch changed protection revision")
	}
}

func TestEngineRevisionReadersRemainMonotonicDuringConcurrentRuleUpdates(t *testing.T) {
	engine := NewEngine()
	start := engine.Revision()
	const writers = 24
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) { defer wg.Done(); engine.AddCustomRule(fmt.Sprintf("revision-%d.example", index)) }(index)
	}
	previous := start
	for index := 0; index < 100; index++ {
		current := engine.Revision()
		if current < previous {
			t.Fatal("concurrent revision read moved backwards")
		}
		previous = current
	}
	wg.Wait()
	if engine.Revision() != start+writers {
		t.Fatal("concurrent rule changes lost a protection revision")
	}
}
