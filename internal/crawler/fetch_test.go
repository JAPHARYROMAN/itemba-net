package crawler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetcherRotatesIdentitiesLimitsBodiesAndBacksOff(t *testing.T) {
	var agents []string
	var status int32 = 200
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		agents = append(agents, req.Header.Get("User-Agent"))
		if req.Header.Get("Accept-Language") == "" || req.Header.Get("X-Extra") != "yes" {
			t.Errorf("headers: %v", req.Header)
		}
		return &http.Response{StatusCode: int(status), Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 3<<20))), Request: req}, nil
	}))
	ctx := context.Background()
	first, err := fetcher.Get(ctx, "https://h.example/page", map[string]string{"X-Extra": "yes"})
	if err != nil || first.Status != 200 || int64(len(first.Body)) != defaultMaxBody || first.ContentType != "text/html" || first.FinalURL != "https://h.example/page" {
		t.Fatalf("first fetch: %+v %v", first, err)
	}
	second, err := fetcher.Get(ctx, "https://h.example/page", map[string]string{"X-Extra": "yes"})
	if err != nil || second.Status != 200 {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0] == agents[1] {
		t.Fatalf("identity profiles must rotate: %v", agents)
	}

	status = 429
	if _, err := fetcher.Get(ctx, "https://h.example/page", map[string]string{"X-Extra": "yes"}); err != nil {
		t.Fatalf("a refusal is still a response: %v", err)
	}
	_, err = fetcher.Get(ctx, "https://h.example/other", map[string]string{"X-Extra": "yes"})
	if !errors.Is(err, ErrHostBackoff) {
		t.Fatalf("the host must be in backoff after a 429, got %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("no request may leave while a host is in backoff, saw %d", len(agents))
	}
	until, ok := fetcher.Backoffs()["h.example"]
	if !ok || time.Until(until) <= 0 || time.Until(until) > backoffBaseDelay+time.Second {
		t.Fatalf("first backoff should be about a minute: %v %v", until, ok)
	}
	if _, err := fetcher.Get(ctx, "https://other.example/page", map[string]string{"X-Extra": "yes"}); err != nil {
		t.Fatalf("other hosts are unaffected: %v", err)
	}
	if _, err := fetcher.Get(ctx, "not a url", nil); err == nil {
		t.Fatal("an unfetchable URL is an error")
	}
}
