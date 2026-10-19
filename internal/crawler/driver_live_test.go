package crawler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveDriverSearch runs one shipped driver against its real host. It is
// opt-in: set ITEMBA_LIVE_DRIVER to the driver name and, optionally,
// ITEMBA_LIVE_DRIVER_QUERY and ITEMBA_LIVE_DRIVER_MARKET. It logs what the
// host listed and the exact failure when the driver cannot read it, which is
// the fastest way to see a host change.
func TestLiveDriverSearch(t *testing.T) {
	name := os.Getenv("ITEMBA_LIVE_DRIVER")
	if name == "" {
		t.Skip("set ITEMBA_LIVE_DRIVER to a shipped driver name for one bounded live search")
	}
	d, err := LoadFile(filepath.Join("..", "..", "drivers", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	query := os.Getenv("ITEMBA_LIVE_DRIVER_QUERY")
	if query == "" {
		query = "Night of the Living Dead"
	}
	market := os.Getenv("ITEMBA_LIVE_DRIVER_MARKET")
	if market == "" && d.HasMarkets() {
		market = d.Markets()[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.Timeout()+5*time.Second)
	defer cancel()
	started := time.Now()
	listings, err := d.Search(ctx, NewFetcher(nil), query, market)
	t.Logf("%s searched %q in %s", name, query, time.Since(started).Round(time.Millisecond))
	if err != nil {
		t.Fatalf("live search failed: %v", err)
	}
	for _, l := range listings {
		safe := struct {
			Title, URL, Access, Duration string
			Year                         int
			Listing                      bool
		}{l.Title, l.URL, l.AccessType, l.Duration, l.Year, l.ListingOnly}
		encoded, _ := json.Marshal(safe)
		t.Logf("listing: %s", encoded)
	}
	if len(listings) == 0 {
		t.Log("the host listed nothing for this query")
	}
}
