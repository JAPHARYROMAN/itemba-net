package crawler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// Observer receives crawl outcomes; metrics implements it.
type Observer interface {
	ObserveCrawl(kind, outcome string)
	ObserveListings(driver string, count int)
}

// Options configure a Crawler. Zero values take the defaults noted.
type Options struct {
	Store     *atlas.Store
	Drivers   []*Driver
	Transport http.RoundTripper
	Observer  Observer
	// Interval between cycles when nothing is due. Default 30 seconds.
	Interval time.Duration
	// Batch is how many frontier rows one cycle leases. Default 16.
	Batch int
	// Workers process leased rows concurrently. Default 4.
	Workers int
	// SeedEvery is how often the frontier is reseeded from Atlas. Default 5 minutes.
	SeedEvery time.Duration
	// SearchRefresh is how long a completed search rests. Default 24 hours.
	SearchRefresh time.Duration
	// PageRefresh is how old evidence may be before a page is fetched again. Default 72 hours.
	PageRefresh time.Duration
	// SeedLimit bounds objects and stale locations seeded per round. Default 200.
	SeedLimit int
}

// Crawler runs the continuous crawl: seed the frontier from what Atlas
// knows, lease what is due, search hosts through their drivers, refresh
// stale locations, and write everything back as evidence.
type Crawler struct {
	opts      Options
	drivers   map[string]*Driver
	fetcher   *Fetcher
	scheduler *Scheduler

	mu       sync.Mutex
	running  bool
	cycles   int
	lastSeed time.Time
	last     CycleReport
	lastErr  string
}

// CycleReport describes one pass over the frontier.
type CycleReport struct {
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	Seeded     int       `json:"seeded"`
	Leased     int       `json:"leased"`
	Searches   int       `json:"searches"`
	Pages      int       `json:"pages"`
	Listings   int       `json:"listings"`
	Errors     int       `json:"errors"`
	Backoffs   int       `json:"backoffs"`
}

// Status is what the console shows about the crawl.
type Status struct {
	Enabled  bool                 `json:"enabled"`
	Running  bool                 `json:"running"`
	Drivers  []string             `json:"drivers"`
	Cycles   int                  `json:"cycles"`
	Last     *CycleReport         `json:"last,omitempty"`
	LastSeed time.Time            `json:"lastSeed,omitempty"`
	Frontier atlas.FrontierCounts `json:"frontier"`
	Backoffs map[string]time.Time `json:"backoffs,omitempty"`
	Error    string               `json:"error,omitempty"`
}

// New creates a crawler. It needs a store and at least one driver.
func New(opts Options) (*Crawler, error) {
	if opts.Store == nil {
		return nil, errors.New("crawler: a database is required; set ITEMBA_DATABASE_URL")
	}
	if len(opts.Drivers) == 0 {
		return nil, errors.New("crawler: at least one driver is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Batch <= 0 {
		opts.Batch = 16
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.SeedEvery <= 0 {
		opts.SeedEvery = 5 * time.Minute
	}
	if opts.SearchRefresh <= 0 {
		opts.SearchRefresh = 24 * time.Hour
	}
	if opts.PageRefresh <= 0 {
		opts.PageRefresh = 72 * time.Hour
	}
	if opts.SeedLimit <= 0 {
		opts.SeedLimit = 200
	}
	c := &Crawler{opts: opts, drivers: map[string]*Driver{}, fetcher: NewFetcher(opts.Transport), scheduler: NewScheduler()}
	for _, d := range opts.Drivers {
		c.drivers[d.Name] = d
		for _, host := range d.Hosts {
			c.scheduler.Configure(host, d.Rate.PerMinute, d.Rate.Concurrency)
		}
		c.scheduler.Configure(d.Host(), d.Rate.PerMinute, d.Rate.Concurrency)
	}
	return c, nil
}

// Run cycles until ctx ends.
func (c *Crawler) Run(ctx context.Context) {
	c.setRunning(true)
	defer c.setRunning(false)
	for {
		report, err := c.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("[crawler] cycle: %v", err)
		}
		wait := c.opts.Interval
		if report.Leased >= c.opts.Batch {
			// The frontier had more than one batch; keep going.
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Crawler) setRunning(running bool) {
	c.mu.Lock()
	c.running = running
	c.mu.Unlock()
}

// RunOnce seeds when due, leases one batch and processes it.
func (c *Crawler) RunOnce(ctx context.Context) (CycleReport, error) {
	report := CycleReport{StartedAt: time.Now()}
	c.mu.Lock()
	seedDue := time.Since(c.lastSeed) >= c.opts.SeedEvery
	c.mu.Unlock()
	if seedDue {
		seeded, err := c.Seed(ctx)
		report.Seeded = seeded
		if err != nil {
			c.finish(report, err)
			return report, err
		}
	}
	entries, err := c.opts.Store.LeaseDue(ctx, c.opts.Batch, 2*time.Minute)
	if err != nil {
		c.finish(report, err)
		return report, err
	}
	report.Leased = len(entries)

	var mu sync.Mutex
	var wg sync.WaitGroup
	queue := make(chan atlas.FrontierEntry)
	for i := 0; i < c.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range queue {
				outcome := c.process(ctx, entry)
				mu.Lock()
				switch entry.Kind {
				case atlas.FrontierSearch:
					report.Searches++
				case atlas.FrontierPage:
					report.Pages++
				}
				report.Listings += outcome.listings
				if outcome.err != nil {
					if errors.Is(outcome.err, ErrHostBackoff) {
						report.Backoffs++
					} else {
						report.Errors++
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, entry := range entries {
		select {
		case queue <- entry:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	c.finish(report, ctx.Err())
	return report, ctx.Err()
}

func (c *Crawler) finish(report CycleReport, err error) {
	report.DurationMs = time.Since(report.StartedAt).Milliseconds()
	c.mu.Lock()
	c.cycles++
	c.last = report
	c.lastErr = ""
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

// Seed enqueues a search per driver and market for the objects Atlas knows,
// and a page fetch for every location whose evidence has gone stale.
func (c *Crawler) Seed(ctx context.Context) (int, error) {
	c.mu.Lock()
	c.lastSeed = time.Now()
	c.mu.Unlock()

	objects, err := c.opts.Store.ObjectsForCrawl(ctx, c.opts.SeedLimit)
	if err != nil {
		return 0, err
	}
	var entries []atlas.FrontierEntry
	for _, object := range objects {
		if object.Type != lens.TypeVideo || object.Title == "" {
			continue
		}
		for _, d := range c.opts.Drivers {
			if d.Spec == nil {
				continue // a driver that only checks pages has no search to seed
			}
			for _, market := range d.Markets() {
				entries = append(entries, atlas.FrontierEntry{
					Kind: atlas.FrontierSearch, Driver: d.Name, ObjectID: object.ID,
					Host: d.Host(), URL: d.SearchURL(object.Title, market), Market: market, Priority: 10,
				})
			}
		}
	}
	stale, err := c.opts.Store.StaleLocations(ctx, time.Now().Add(-c.opts.PageRefresh), c.opts.SeedLimit)
	if err != nil {
		return 0, err
	}
	for _, location := range stale {
		entries = append(entries, atlas.FrontierEntry{
			Kind: atlas.FrontierPage, ObjectID: location.ObjectID, LocationID: location.ID,
			Host: location.Hostname, URL: location.URL, Priority: 5,
		})
	}
	return c.opts.Store.Enqueue(ctx, entries)
}

type outcome struct {
	listings int
	err      error
}

func (c *Crawler) process(ctx context.Context, entry atlas.FrontierEntry) outcome {
	switch entry.Kind {
	case atlas.FrontierSearch:
		return c.processSearch(ctx, entry)
	case atlas.FrontierPage:
		return c.processPage(ctx, entry)
	}
	return outcome{err: fmt.Errorf("crawler: unknown frontier kind %q", entry.Kind)}
}

func (c *Crawler) processSearch(ctx context.Context, entry atlas.FrontierEntry) outcome {
	d, ok := c.drivers[entry.Driver]
	if !ok {
		c.complete(ctx, entry, "error: driver "+entry.Driver+" is not loaded", 24*time.Hour)
		c.observe("search", "no_driver")
		return outcome{err: fmt.Errorf("crawler: driver %q is not loaded", entry.Driver)}
	}
	object, err := c.opts.Store.ObjectByID(ctx, entry.ObjectID)
	if err != nil {
		c.complete(ctx, entry, "error: object missing", 7*24*time.Hour)
		c.observe("search", "no_object")
		return outcome{err: err}
	}
	release, err := c.scheduler.Acquire(ctx, entry.Host)
	if err != nil {
		return outcome{err: err}
	}
	defer release()

	listings, err := d.Search(ctx, c.fetcher, object.Title, entry.Market)
	if err != nil {
		if errors.Is(err, ErrHostBackoff) {
			c.complete(ctx, entry, "backoff", 30*time.Minute)
			c.observe("search", "backoff")
		} else {
			c.complete(ctx, entry, "error: "+err.Error(), retryAfter(entry.Attempts))
			c.observe("search", "error")
		}
		return outcome{err: err}
	}
	summary, err := c.opts.Store.RecordListings(ctx, object, listings, "driver:"+d.Name)
	if err != nil {
		c.complete(ctx, entry, "error: "+err.Error(), retryAfter(entry.Attempts))
		c.observe("search", "record_error")
		return outcome{err: err}
	}
	c.complete(ctx, entry, fmt.Sprintf("ok: %d listings", summary.Locations), c.opts.SearchRefresh)
	c.observe("search", "ok")
	if c.opts.Observer != nil {
		c.opts.Observer.ObserveListings(d.Name, summary.Locations)
	}
	return outcome{listings: summary.Locations}
}

// processPage fetches a known location once and records whether it still
// responds: Reachable on success, Listed with the status otherwise. It is
// the tier-one refresh; identification and playback belong to Probe.
func (c *Crawler) processPage(ctx context.Context, entry atlas.FrontierEntry) outcome {
	release, err := c.scheduler.Acquire(ctx, entry.Host)
	if err != nil {
		return outcome{err: err}
	}
	defer release()

	response, err := c.fetcher.Get(ctx, entry.URL, nil)
	if err != nil {
		if errors.Is(err, ErrHostBackoff) {
			c.complete(ctx, entry, "backoff", 30*time.Minute)
			c.observe("page", "backoff")
			return outcome{err: err}
		}
		_ = c.opts.Store.RecordEvidence(ctx, atlas.Evidence{LocationID: entry.LocationID, Level: lens.LevelListed, Method: "http_page_fetch", Status: "error", Detail: err.Error(), ObservedAt: time.Now()})
		c.complete(ctx, entry, "error: "+err.Error(), retryAfter(entry.Attempts))
		c.observe("page", "error")
		return outcome{err: err}
	}
	level, status := lens.LevelListed, fmt.Sprintf("http_%d", response.Status)
	if response.Status >= 200 && response.Status < 300 {
		level = lens.LevelReachable
	}
	if err := c.opts.Store.RecordEvidence(ctx, atlas.Evidence{
		LocationID: entry.LocationID, Level: level, Method: "http_page_fetch", Status: status,
		Detail: response.ContentType, ObservedAt: time.Now(),
		Artefacts: map[string]any{"httpStatus": response.Status, "finalUrl": response.FinalURL, "bytes": len(response.Body)},
	}); err != nil {
		c.complete(ctx, entry, "error: "+err.Error(), retryAfter(entry.Attempts))
		c.observe("page", "record_error")
		return outcome{err: err}
	}
	c.complete(ctx, entry, status, c.opts.PageRefresh)
	c.observe("page", status)
	return outcome{}
}

func (c *Crawler) complete(ctx context.Context, entry atlas.FrontierEntry, status string, after time.Duration) {
	if err := c.opts.Store.Complete(context.WithoutCancel(ctx), entry.ID, status, time.Now().Add(after)); err != nil {
		log.Printf("[crawler] complete %d: %v", entry.ID, err)
	}
}

func (c *Crawler) observe(kind, outcome string) {
	if c.opts.Observer != nil {
		c.opts.Observer.ObserveCrawl(kind, outcome)
	}
}

// retryAfter backs a failing entry off by attempts, from one hour to a day.
func retryAfter(attempts int) time.Duration {
	delay := time.Hour << uint(min(attempts, 4))
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}

// Status reports the crawl for the console.
func (c *Crawler) Status(ctx context.Context) Status {
	c.mu.Lock()
	status := Status{Enabled: true, Running: c.running, Cycles: c.cycles, LastSeed: c.lastSeed, Error: c.lastErr}
	if c.cycles > 0 {
		last := c.last
		status.Last = &last
	}
	c.mu.Unlock()
	for _, d := range c.opts.Drivers {
		status.Drivers = append(status.Drivers, d.Name)
	}
	counts, err := c.opts.Store.FrontierCounts(ctx)
	if err != nil && status.Error == "" {
		status.Error = err.Error()
	}
	status.Frontier = counts
	if backoffs := c.fetcher.Backoffs(); len(backoffs) > 0 {
		status.Backoffs = backoffs
	}
	return status
}
