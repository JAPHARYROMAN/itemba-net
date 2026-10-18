package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLiveGlobalMovieCharts(t *testing.T) {
	if os.Getenv("ITEMBA_LIVE_MOVIE_DISCOVERY") != "1" {
		t.Skip("set ITEMBA_LIVE_MOVIE_DISCOVERY=1 for bounded official chart integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Scout{httpClient: &http.Client{Timeout: 5500 * time.Millisecond}}
	report := s.DiscoverMovies(ctx, "popular", "ALL")
	if report.Region != "ALL" || len(report.Providers) != 8 || len(report.Items) == 0 {
		t.Fatalf("official global movie charts unavailable: %+v", report.Providers)
	}
	recent := s.DiscoverMovies(ctx, "recent", "ALL")
	if !recent.Cached || len(recent.Items) == 0 {
		t.Fatal("no recent chart titles or cache reuse from official feeds")
	}
	if len(recent.Items) > 6 {
		recent.Items = recent.Items[:6]
	}
	proof, _ := json.Marshal(recent)
	t.Logf("official recent chart proof: %s", proof)
}
