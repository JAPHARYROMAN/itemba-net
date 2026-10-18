package shield

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/cdp"
)

type fixtureBrowser struct {
	failAt   int
	commands []string
	handler  cdp.EventHandler
	onFetch  func()
	async    []string
}

func (b *fixtureBrowser) Call(_ context.Context, method string, _, _ any) error {
	b.commands = append(b.commands, method)
	if len(b.commands) == b.failAt {
		return errors.New("fixture command rejected")
	}
	if method == "Fetch.enable" && b.onFetch != nil {
		b.onFetch()
	}
	return nil
}

func (b *fixtureBrowser) CallAsync(method string, _ any) error {
	b.async = append(b.async, method)
	return nil
}

func (b *fixtureBrowser) OnEvent(handler cdp.EventHandler) { b.handler = handler }

func TestAttachRejectsEveryRequiredSetupFailure(t *testing.T) {
	for failAt := 1; failAt <= 7; failAt++ {
		t.Run(fmt.Sprintf("command_%d", failAt), func(t *testing.T) {
			browser := &fixtureBrowser{failAt: failAt}
			err := New().Attach(context.Background(), browser, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "fixture command rejected") {
				t.Fatalf("setup failure must propagate, got %v", err)
			}
			if len(browser.commands) != failAt {
				t.Fatalf("setup continued after failure: %v", browser.commands)
			}
		})
	}
}

func TestAttachHandlesFirstRequestWhileInterceptionEnables(t *testing.T) {
	sh := New()
	browser := &fixtureBrowser{}
	browser.onFetch = func() {
		if browser.handler == nil {
			t.Fatal("interception enabled before its event listener")
		}
		browser.handler("Fetch.requestPaused", json.RawMessage(`{"requestId":"first","resourceType":"Script","request":{"url":"https://doubleclick.net/ad.js"}}`))
	}
	if err := sh.Attach(context.Background(), browser, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(browser.async) != 1 || browser.async[0] != "Fetch.failRequest" {
		t.Fatalf("first advertising request was not blocked: %v", browser.async)
	}
	if sh.Snapshot().TotalBlocked != 1 {
		t.Fatal("blocked request missing from telemetry")
	}
}

func TestAttachCanceledContextDoesNotConfigureBrowser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	browser := &fixtureBrowser{}
	err := New().Attach(ctx, browser, nil, nil)
	if !errors.Is(err, context.Canceled) || len(browser.commands) != 0 {
		t.Fatalf("canceled setup sent browser commands: %v, %v", err, browser.commands)
	}
}

func TestMediaExtensionsCannotOverrideDomainSinkholes(t *testing.T) {
	for _, tc := range []struct {
		name, customRule, target string
	}{
		{"default domain video", "", "https://adsrvr.org/feature.mp4"},
		{"default subdomain manifest", "", "https://video.adsrvr.org/feature.m3u8"},
		{"custom domain video", "||sponsor.example^", "https://sponsor.example/feature.mp4"},
		{"custom subdomain segment", "||sponsor.example^", "https://media.sponsor.example/feature/segment.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine()
			engine.AddCustomRule(tc.customRule)
			match := engine.Inspect(tc.target, "Media")
			if !match.Blocked || match.Layer != Layer1DNS {
				t.Fatalf("media bypassed explicit domain block: %+v", match)
			}
		})
	}
}

func TestCustomURLRulesApplyBeforeMediaContentExemption(t *testing.T) {
	for _, tc := range []struct{ rule, blocked string }{
		{"/sponsor/feature.mp4", "https://media.example/sponsor/feature.mp4"},
		{"||media.example/sponsor/playlist.m3u8", "https://media.example/sponsor/playlist.m3u8"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			engine := NewEngine()
			engine.AddCustomRule(tc.rule)
			match := engine.Inspect(tc.blocked, "Media")
			if !match.Blocked || match.Layer != Layer2CDP || !strings.HasPrefix(match.ListSource, "Custom ") {
				t.Fatalf("custom media URL rule was ignored: %+v", match)
			}
			if match := engine.Inspect("https://media.example/movies/full-feature.mp4", "Media"); match.Blocked {
				t.Fatalf("unrelated movie was blocked by a custom URL rule: %+v", match)
			}
		})
	}
}

func TestMediaContentExemptionRetainsDefaultHeuristicAndExplicitAdBehavior(t *testing.T) {
	engine := NewEngine()
	for _, target := range []string{"https://media.example/movies/full-feature.mp4", "https://media.example/telemetry/pixel/full-feature.mp4"} {
		if match := engine.Inspect(target, "Media"); match.Blocked {
			t.Fatalf("ordinary content exemption changed for %q: %+v", target, match)
		}
	}
	if match := engine.Inspect("https://media.example/ads/preroll.mp4", "Media"); !match.Blocked || match.Layer != Layer2CDP {
		t.Fatalf("explicit preroll URL escaped filtering: %+v", match)
	}
}

func TestMediaDomainExceptionRetainsPriorityOverDomainAndCustomURLBlocks(t *testing.T) {
	engine := NewEngine()
	engine.AddCustomRule("||sponsor.example^")
	engine.AddCustomRule("/sponsor/feature.mp4")
	target := "https://media.sponsor.example/sponsor/feature.mp4"
	if !engine.Inspect(target, "Media").Blocked {
		t.Fatal("fixture must be blocked before the explicit exception")
	}
	engine.AddCustomRule("@@||sponsor.example^")
	if match := engine.Inspect(target, "Media"); match.Blocked {
		t.Fatalf("explicit exception no longer overrides media blocks: %+v", match)
	}
}
