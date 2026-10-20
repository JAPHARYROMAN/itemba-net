package crawler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The fixtures here are the legacy scout providers' own test bodies, so each
// driver is proven against the provider it replaces.

func TestAllShippedDriversHaveTheirIdentity(t *testing.T) {
	drivers := shippedDrivers(t)
	want := map[string]string{
		"apple-movies": "Apple movie catalog ({market})", "internet-archive": "Internet Archive", "peertube-sepia": "PeerTube",
		"peertube-blender": "Blender open movies", "wikimedia-commons": "Wikimedia Commons", "nasa-plus": "NASA+ free documentaries", "nfb": "NFB / ONF free film catalog",
		"public-domain-movies": "Public Domain Movies browse", "arcoiris-tv": "ArcoirisTV feature catalog", "publicdomainmovie-org": "PublicDomainMovie.org feature catalog", "fawesome": "Fawesome live movie search",
		// The first audio and image drivers of Phase 3.
		"internet-archive-audio": "Internet Archive audio", "wikimedia-commons-images": "Wikimedia Commons pictures",
		// Drivers that only check a provider's pages have no search status.
		"pluto-tv": "", "filmzie": "",
	}
	if len(drivers) != len(want) {
		t.Fatalf("expected %d drivers, got %d", len(want), len(drivers))
	}
	for name, status := range want {
		d, ok := drivers[name]
		if !ok {
			t.Fatalf("driver %s is missing", name)
		}
		if status == "" {
			if d.Spec != nil || d.Check == nil || d.StatusName("") != d.Provider || d.Host() != d.Hosts[0] {
				t.Fatalf("%s must only check pages", name)
			}
			continue
		}
		if d.Spec.StatusName != status {
			t.Fatalf("%s status name %q, want %q", name, d.Spec.StatusName, status)
		}
	}
	if drivers["apple-movies"].StatusName("ZA") != "Apple movie catalog (ZA)" {
		t.Fatal("market status name")
	}
	if drivers["nfb"].Timeout().Seconds() != 4 || drivers["apple-movies"].Timeout().Seconds() != 10 {
		t.Fatal("timeouts")
	}
}

func TestSepiaDriverReadsFederatedListings(t *testing.T) {
	d := shippedDrivers(t)["peertube-sepia"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "sepiasearch.org" || req.URL.Path != "/api/v1/search/videos" || q.Get("search") != "Big Buck Bunny" || q.Get("count") != "3" || q.Get("nsfw") != "false" {
			t.Errorf("unexpected sepia query: %s", req.URL)
		}
		return jsonResponse(200, `{"data":[
			{"uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","name":"Big Buck Bunny","description":"`+strings.Repeat("x", 200)+`","url":"https://peertube.example/w/abc","embedUrl":"https://peertube.example/videos/embed/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","thumbnailUrl":"https://peertube.example/thumb.jpg","duration":596,"account":{"displayName":"Blender","host":"peertube.example"}},
			{"uuid":"ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee","name":"Mirror","url":"https://other.example/w/def","duration":3700,"account":{"host":"other.example"}},
			{"uuid":"no-url","name":"Nothing"}
		]}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Big Buck Bunny", "")
	if err != nil || len(listings) != 2 {
		t.Fatalf("listings: %+v %v", listings, err)
	}
	first := listings[0]
	if first.ID != "pt-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || first.URL != "https://peertube.example/videos/embed/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || first.Provider != "PeerTube (peertube.example)" {
		t.Fatalf("embed page and per-host provider: %+v", first)
	}
	if first.Duration != "9m56s" || first.AccessType != "free" || first.ListingOnly || first.SourceType != "Federated PeerTube" || first.Thumbnail == "" {
		t.Fatalf("sepia fields: %+v", first)
	}
	if !strings.HasSuffix(first.Description, "…") || len([]rune(first.Description)) != 141 {
		t.Fatalf("long descriptions are truncated: %q", first.Description)
	}
	second := listings[1]
	if second.URL != "https://other.example/w/def" || second.Description != "Federated PeerTube stream hosted on other.example." || second.Duration != "1h1m40s" {
		t.Fatalf("url fallback and templated default description: %+v", second)
	}
}

func TestBlenderDriverFiltersPrivateLiveShortAndInvalidVideos(t *testing.T) {
	d := shippedDrivers(t)["peertube-blender"]
	const id = "bf1f3fb5-b119-4f9f-9930-8e20e892b898"
	body := func(mutate func(map[string]any)) string {
		video := map[string]any{
			"uuid": id, "name": "Big Buck Bunny - Third Open Movie by Blender Foundation", "duration": 596,
			"privacy": map[string]any{"id": 1}, "url": "https://video.blender.org/videos/watch/" + id,
			"originallyPublishedAt": "2008-05-30T00:00:00Z", "description": "The open movie.",
		}
		mutate(video)
		encoded, err := json.Marshal(map[string]any{"data": []any{video}})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	search := func(body string) []string {
		fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			q := req.URL.Query()
			if req.URL.Host != "video.blender.org" || req.URL.Path != "/api/v1/search/videos" || q.Get("search") != "Big Buck Bunny" || q.Get("count") != "6" || q.Get("isLocal") != "true" || req.Header.Get("Authorization") != "" {
				t.Errorf("unexpected official catalog request: %s", req.URL)
			}
			return jsonResponse(200, body), nil
		}))
		listings, err := d.Search(context.Background(), fetcher, "Big Buck Bunny", "")
		if err != nil {
			t.Fatal(err)
		}
		var urls []string
		for _, l := range listings {
			urls = append(urls, l.URL)
			if l.Title != "Big Buck Bunny" || l.Year != 2008 || l.Duration != "9m56s" || !l.ListingOnly || l.AccessType != "unknown" || l.ID != "pt-blender-"+id {
				t.Fatalf("blender listing fields: %+v", l)
			}
		}
		return urls
	}
	if urls := search(body(func(map[string]any) {})); len(urls) != 1 || urls[0] != "https://video.blender.org/videos/watch/"+id {
		t.Fatalf("the official movie must be listed at its own page: %v", urls)
	}
	// A foreign url field is ignored: the page is derived from the video id.
	if urls := search(body(func(v map[string]any) { v["url"] = "https://other.video.net/videos/watch/" + id })); len(urls) != 1 {
		t.Fatalf("foreign url: %v", urls)
	}
	for name, mutate := range map[string]func(map[string]any){
		"private":      func(v map[string]any) { v["privacy"] = map[string]any{"id": 3} },
		"live":         func(v map[string]any) { v["isLive"] = true },
		"password":     func(v map[string]any) { v["passwordProtected"] = true },
		"invalid uuid": func(v map[string]any) { v["uuid"] = "../private" },
		"short clip":   func(v map[string]any) { v["duration"] = 90 },
		"no name":      func(v map[string]any) { v["name"] = "" },
	} {
		if urls := search(body(mutate)); len(urls) != 0 {
			t.Fatalf("%s must be filtered, got %v", name, urls)
		}
	}
}

func TestWikimediaDriverReadsPagesKeyedByIdAndMediaURLs(t *testing.T) {
	d := shippedDrivers(t)["wikimedia-commons"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "commons.wikimedia.org" || req.URL.Path != "/w/api.php" || q.Get("gsrsearch") != "filetype:video Sintel" || q.Get("generator") != "search" || q.Get("iiprop") != "url|dimensions" || q.Get("format") != "json" {
			t.Errorf("unexpected commons query: %s", req.URL)
		}
		if !strings.HasPrefix(req.Header.Get("User-Agent"), "ITEMBA-NET/2") {
			t.Errorf("commons needs an identifying user agent, got %q", req.Header.Get("User-Agent"))
		}
		return jsonResponse(200, `{"query":{"pages":{
			"42":{"pageid":42,"title":"File:Sintel.webm","imageinfo":[{"url":"https://upload.wikimedia.org/sintel.webm","descriptionurl":"https://commons.wikimedia.org/wiki/File:Sintel.webm","width":1920,"height":818}]},
			"7":{"pageid":7,"title":"File:Nothing.webm","imageinfo":[]}
		}}}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Sintel", "")
	if err != nil || len(listings) != 1 {
		t.Fatalf("listings: %+v %v", listings, err)
	}
	got := listings[0]
	if got.ID != "wm-42" || got.Title != "Sintel.webm" || got.URL != "https://commons.wikimedia.org/wiki/File:Sintel.webm" || got.MediaURL != "https://upload.wikimedia.org/sintel.webm" {
		t.Fatalf("identity: %+v", got)
	}
	if got.Resolution != "1920x818" || got.AccessType != "free" || got.ListingOnly || got.SourceType != "Open media" {
		t.Fatalf("media fields: %+v", got)
	}
	empty := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"query":{"pages":{}}}`), nil
	}))
	if listings, err := d.Search(context.Background(), empty, "Nothing", ""); err != nil || len(listings) != 0 {
		t.Fatalf("empty pages object: %+v %v", listings, err)
	}
}

const nasaFixtureAPI = `{"id":6604,"type":"video","slug":"planetary-defenders","link":"https://plus.nasa.gov/video/planetary-defenders/","date":"2025-04-16T16:27:46","title":{"rendered":"Planetary Defenders &amp; Friends"},"meta":{"video-url":"https://nasaplus.akamaized.net/output/16899.m3u8","runtime":"4508","youtube-url":"","preview-url":"https://nasaplus.akamaized.net/output/trailer.m3u8"}}`

func TestNASADriverReadsTheBareArrayAndDropsPreviews(t *testing.T) {
	d := shippedDrivers(t)["nasa-plus"]
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "plus.nasa.gov" || req.URL.Path != "/wp-json/wp/v2/video" || q.Get("search") != "Planetary Defenders" || q.Get("per_page") != "20" || q.Get("country") != "" {
			t.Errorf("NASA title or global search was changed: %s", req.URL)
		}
		preview := strings.ReplaceAll(nasaFixtureAPI, `"4508"`, `"139"`)
		page := strings.ReplaceAll(nasaFixtureAPI, `"type":"video"`, `"type":"page"`)
		return jsonResponse(200, "["+nasaFixtureAPI+","+preview+","+page+"]"), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Planetary Defenders", "")
	if err != nil || len(listings) != 1 {
		t.Fatalf("NASA catalog must keep only the full documentary: %+v %v", listings, err)
	}
	got := listings[0]
	if got.ID != "nasa-6604" || got.Title != "Planetary Defenders & Friends" || got.URL != "https://plus.nasa.gov/video/planetary-defenders/" || got.Duration != "4508" || got.AccessType != "unknown" || !got.ListingOnly || got.MediaURL != "" {
		t.Fatalf("NASA listing: %+v", got)
	}
}

func TestNFBDriverUsesThePublicSearchAndFullRuntime(t *testing.T) {
	d := shippedDrivers(t)["nfb"]
	calls := 0
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		q := req.URL.Query()
		if req.URL.Host != "searchapi.nfb.ca" || req.URL.Path != "/api/search/v4/films/" || q.Get("q") != "We Will Stand Up" || q.Get("per_page") != "6" || q.Get("country") != "" || q.Get("include") != "description" {
			t.Errorf("public global NFB search contract changed: %s", req.URL)
		}
		return jsonResponse(200, `{"items":[
			{"title":"nîpawistamâsowin: We Will Stand Up","slug":"nipawistamasowin-we-will-stand-up","year":2019,"time":"PT01:38:36","description":{"en":"A <em>full film</em> description.","fr":null},"is_streamable_for_free":true},
			{"title":"We Will Stand Up (52 minutes)","slug":"we-will-stand-up-52","year":2019,"time":"PT00:52:00","description":{"en":"Shorter version","fr":null},"is_streamable_for_free":false},
			{"title":"We Will Stand Up trailer","slug":"we-will-stand-up-trailer","year":2019,"time":"PT2M","description":"Plain","is_streamable_for_free":true},
			{"title":"Odd slug","slug":"../etc","year":2019,"time":"PT1H40M","is_streamable_for_free":true},
			{"title":"No time","slug":"no-time","year":2018,"description":"Plain description","is_streamable_for_free":true}
		]}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "We Will Stand Up", "")
	if err != nil || calls != 1 || len(listings) != 2 {
		t.Fatalf("catalog included paid, short or malformed entries: calls=%d %+v %v", calls, listings, err)
	}
	got := listings[0]
	if got.ID != "nfb-nipawistamasowin-we-will-stand-up" || got.URL != "https://www.nfb.ca/film/nipawistamasowin-we-will-stand-up/" || got.Duration != "5916" || got.Year != 2019 || got.AccessType != "unknown" || !got.ListingOnly {
		t.Fatalf("NFB listing: %+v", got)
	}
	if got.Description != "A full film description." {
		t.Fatalf("localized HTML description was not read safely: %q", got.Description)
	}
	if listings[1].Description != "Plain description" || listings[1].Duration != "" {
		t.Fatalf("a plain description and an unknown runtime are kept: %+v", listings[1])
	}
}

func TestRuntimeReadsRootAndObjectItemsAndNewTransforms(t *testing.T) {
	document := map[string]any{"b": map[string]any{"x": 1.0}, "a": map[string]any{"x": 2.0}}
	items := itemsAt(document, ".")
	if len(items) != 2 || scalarString(lookup(items[0], "x")) != "2" {
		t.Fatalf("object items read in key order: %+v", items)
	}
	if len(itemsAt([]any{1.0}, ".")) != 1 || itemsAt("scalar", ".") != nil || itemsAt(document, "missing") != nil {
		t.Fatal("root and missing items")
	}
	if transform("duration_from_seconds", "596") != "9m56s" || transform("duration_seconds", "PT01:38:36") != "5916" || transform("duration_seconds", "soon") != "" {
		t.Fatal("duration transforms")
	}
	if transform("html_unescape", "Tom &amp; Jerry") != "Tom & Jerry" || transform("html_text", "<p>A <em>full</em>  film.</p>") != "A full film." {
		t.Fatal("html transforms")
	}
	spec := FieldSpec{Path: "t", StripPrefix: "File:", StripSuffixes: []string{" 60fps 4K"}, Truncate: 5}
	if value, ok := spec.read(item{json: map[string]any{"t": "File:Sintel 60fps 4K"}}); !ok || value != "Sinte…" {
		t.Fatalf("prefix, suffix and truncate: %q %v", value, ok)
	}
}
