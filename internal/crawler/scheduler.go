package crawler

import (
	"context"
	"sync"
	"time"
)

// Scheduler keeps the crawler polite per host: a token bucket for request
// rate and a cap on concurrent requests. Politeness here is self-preservation,
// not a principle; the limits come from each driver.
type Scheduler struct {
	mu    sync.Mutex
	hosts map[string]*bucket
	now   func() time.Time
}

type bucket struct {
	perMinute   float64
	concurrency int
	tokens      float64
	last        time.Time
	inflight    int
}

const (
	defaultPerMinute   = 30
	defaultConcurrency = 2
)

// NewScheduler creates an empty scheduler; unknown hosts get the defaults.
func NewScheduler() *Scheduler {
	return &Scheduler{hosts: map[string]*bucket{}, now: time.Now}
}

// Configure sets a host's limits, usually from its driver.
func (s *Scheduler) Configure(host string, perMinute, concurrency int) {
	if perMinute <= 0 {
		perMinute = defaultPerMinute
	}
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bucketFor(host)
	b.perMinute = float64(perMinute)
	b.concurrency = concurrency
	if b.tokens > float64(perMinute) {
		b.tokens = float64(perMinute)
	}
}

func (s *Scheduler) bucketFor(host string) *bucket {
	b, ok := s.hosts[host]
	if !ok {
		b = &bucket{perMinute: defaultPerMinute, concurrency: defaultConcurrency, tokens: defaultPerMinute, last: s.now()}
		s.hosts[host] = b
	}
	return b
}

// tryAcquire takes a token and a slot if both are available, otherwise it
// says how long to wait before trying again.
func (s *Scheduler) tryAcquire(host string, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bucketFor(host)
	elapsed := now.Sub(b.last).Minutes()
	if elapsed > 0 {
		b.tokens += elapsed * b.perMinute
		if b.tokens > b.perMinute {
			b.tokens = b.perMinute
		}
		b.last = now
	}
	if b.inflight >= b.concurrency {
		return false, 200 * time.Millisecond
	}
	if b.tokens < 1 {
		deficit := 1 - b.tokens
		return false, time.Duration(deficit / b.perMinute * float64(time.Minute))
	}
	b.tokens--
	b.inflight++
	return true, 0
}

func (s *Scheduler) release(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.hosts[host]; ok && b.inflight > 0 {
		b.inflight--
	}
}

// Acquire waits for the host to allow one more request and returns the
// function that releases its slot.
func (s *Scheduler) Acquire(ctx context.Context, host string) (func(), error) {
	for {
		ok, wait := s.tryAcquire(host, s.now())
		if ok {
			released := false
			return func() {
				if !released {
					released = true
					s.release(host)
				}
			}, nil
		}
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
