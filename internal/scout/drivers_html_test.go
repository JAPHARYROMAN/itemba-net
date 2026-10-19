package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The pipeline's half of the HTML catalog conversions: the drivers list every
// well-formed card, and these rules keep only the requested feature.

func TestPublicDomainBrowseKeepsOnlyTheRequestedFeature(t *testing.T) {
	var calls atomic.Int32
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `<nav><a href="/movie/his-girl-friday">Unrelated navigation</a></nav><div class="view-content"><div class="details"><div class="date"><a href="/year?v=1940">1940</a></div><h1><a href="/movie/his-girl-friday">His Girl Friday</a></h1></div><div class="details"><a href="/year?v=1950">1950</a><h1><a href="/movie/wrong-edition">His Girl Friday</a></h1></div><h1><a href="/movie/his-girl-friday-trailer">His Girl Friday Trailer</a></h1><h1><a href="/movie/collection">His Girl Friday Collection</a></h1></div><a href="/feature_movies?page=1">2</a><a href="/feature_movies?page=2">3</a><a href="/feature_movies?page=3">4</a>`
		r := searchFixtureResponse(200, "text/html", body)
		r.Request = req
		return r, nil
	})
	got, status := s.queryDriver(context.Background(), s.driverFetcher(), testDriver(t, "public-domain-movies"), "His Girl Friday 1940", "His Girl Friday", "")
	if status.Status != "ok" || calls.Load() != 3 || len(got) != 1 {
		t.Fatalf("catalog request/candidate bounds lost: %d %d %+v", len(got), calls.Load(), status)
	}
	if got[0].Title != "His Girl Friday" || got[0].ReleaseYear != 1940 || !got[0].ListingOnly || got[0].URL != "https://publicdomainmovie.net/movie/his-girl-friday" {
		t.Fatalf("wrong observed catalog: %+v", got)
	}
}

func TestFeatureCatalogDriversKeepTheExactFeatureAndCarryTheRequestedYear(t *testing.T) {
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.String() {
		case "https://www.arcoiris.tv/search/s/Night%20of%20the%20Living%20Dead/type/all/":
			body = `<h2><a href="/scheda/en/766/">Night of the Living Dead</a></h2><h2><a href="/scheda/en/999/">Night of the Living Dead Trailer</a></h2><div hidden><h2><a href="/scheda/en/123/">Night of the Living Dead</a></h2></div>`
		case "https://publicdomainmovie.org/?s=Night+of+the+Living+Dead":
			body = `<h3><a href="/night-of-the-living-dead-1968/">Night of the Living Dead (1968) – Watch free</a></h3><h3><a href="/night-of-the-living-dead-1990/">Night of the Living Dead (1990)</a></h3><h3><a href="https://publicdomainmovie.org.evil.net/night-of-the-living-dead-1968/">Night of the Living Dead (1968)</a></h3><footer><h3><a href="/night-of-the-living-dead-1968-hidden/">Night of the Living Dead (1968)</a></h3></footer>`
		default:
			t.Fatalf("unexpected discovery URL: %s", req.URL)
		}
		r := searchFixtureResponse(200, "text/html", body)
		r.Request = req
		return r, nil
	})
	for _, name := range []string{"arcoiris-tv", "publicdomainmovie-org"} {
		items, status := s.queryDriver(context.Background(), s.driverFetcher(), testDriver(t, name), "Night of the Living Dead 1968", "Night of the Living Dead", "")
		if status.Status != "ok" || len(items) != 1 {
			t.Fatalf("%s: expected one exact listing: %+v %+v", name, items, status)
		}
		item := items[0]
		if item.DirectStreamURL != "" || item.AccessType != "unknown" || !item.ListingOnly || item.ReleaseYear != 1968 || item.Title != "Night of the Living Dead" {
			t.Fatalf("%s: catalog results must await media verification with the requested year: %+v", name, item)
		}
		if kind, _, ok := freeResolutionTarget(item); !ok || (kind != "arcoiris" && kind != "public_domain_movie_org") {
			t.Fatalf("%s: the listing must enter the feature resolver: %q %v", name, kind, ok)
		}
	}
}

func TestFawesomeDriverSearchDropsTrailersAndUnrelatedTitles(t *testing.T) {
	type item struct {
		NodeID     int64   `json:"node_id"`
		Title      string  `json:"title"`
		WebURL     string  `json:"web_url"`
		Runtime    float64 `json:"runtime"`
		ItemType   string  `json:"item_type"`
		FeedType   string  `json:"feed_type"`
		ActionType string  `json:"actiontype"`
		BlockItem  string  `json:"blockItem"`
	}
	items := []item{
		{10876543, "Beyond Known Examples", "movies/10876543/beyond-known-examples", 5400, "movie", "video", "video", "false"},
		{10876544, "Beyond Known Examples Trailer", "movies/10876544/beyond-known-examples-trailer", 5400, "movie", "video", "video", "false"},
		{10876545, "Entirely Unrelated", "movies/10876545/entirely-unrelated", 5400, "movie", "video", "video", "false"},
	}
	payload, _ := json.Marshal(map[string]any{"status": "ok", "type": "result", "results": items})
	s := driverScout(func(req *http.Request) (*http.Response, error) {
		var r *http.Response
		switch req.URL.Path {
		case "/":
			r = searchFixtureResponse(200, "text/html", `<html><script src="/home/new/v987/assets/js/iptv-main.js?ver=2345"></script></html>`)
		case "/home/new/v987/assets/js/iptv-main.js":
			r = searchFixtureResponse(200, "application/javascript", fawesomeDiscoveryBundleFixture)
		case "/home/new/v987/api/getSecurityToken.php":
			r = searchFixtureResponse(200, "application/json", `{"securityToken":"`+fawesomeDiscoveryGuestFixture+`"}`)
		case "/home/new/v987/api/recipes.php":
			r = searchFixtureResponse(200, "application/json", string(payload))
		default:
			t.Fatalf("unobserved public guest endpoint: %s", req.URL)
		}
		r.Request = req
		return r, nil
	})
	got, status := s.queryDriver(context.Background(), s.driverFetcher(), testDriver(t, "fawesome"), "Beyond Known Examples", "Beyond Known Examples", "")
	if status.Status != "ok" || len(got) != 1 || got[0].URL != "https://fawesome.tv/movies/10876543/beyond-known-examples" {
		t.Fatalf("only the requested film survives relevance: %+v %+v", got, status)
	}
	if got[0].Provider != "Fawesome" || got[0].AccessType != "free" || got[0].AccessBasis != "provider_model" || !got[0].ListingOnly || !fawesomeMovieTarget(got[0]) {
		t.Fatalf("the listing must enter the Fawesome playback check as before: %+v", got[0])
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), fawesomeDiscoveryGuestFixture) {
		t.Fatal("guest token escaped into candidates")
	}
}
