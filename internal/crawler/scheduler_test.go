package crawler

import (
	"context"
	"testing"
	"time"
)

func TestSchedulerEnforcesRateAndConcurrencyPerHost(t *testing.T) {
	s := NewScheduler()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.Configure("h.example", 2, 1)

	ok, _ := s.tryAcquire("h.example", now)
	if !ok {
		t.Fatal("first request must pass")
	}
	if ok, wait := s.tryAcquire("h.example", now); ok || wait != 200*time.Millisecond {
		t.Fatalf("concurrency of one must block the second in-flight request: %v %v", ok, wait)
	}
	s.release("h.example")
	ok, _ = s.tryAcquire("h.example", now)
	if !ok {
		t.Fatal("the second token must pass once the slot is free")
	}
	s.release("h.example")
	ok, wait := s.tryAcquire("h.example", now)
	if ok || wait <= 0 || wait > 30*time.Second {
		t.Fatalf("two per minute means the third waits about half a minute, got %v %v", ok, wait)
	}
	now = now.Add(31 * time.Second)
	if ok, _ := s.tryAcquire("h.example", now); !ok {
		t.Fatal("tokens must refill with time")
	}
	s.release("h.example")
	if ok, _ := s.tryAcquire("other.example", now); !ok {
		t.Fatal("hosts are independent")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, "h.example"); err == nil {
		t.Fatal("Acquire must give up when the context ends before a token is available")
	}
}
