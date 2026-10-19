package crawler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// The fixture is the Archive's own answer to an audio search on 19 October
// 2026, trimmed to the fields the driver reads: a string creator, an array
// creator, a numeric year, and a doc with no creator at all.
func TestArchiveAudioDriverListsRecordingsWithTheirPerformers(t *testing.T) {
	drivers := shippedDrivers(t)
	d := drivers["internet-archive-audio"]
	if d == nil {
		t.Fatal("the internet-archive-audio driver is not shipped")
	}
	if d.Kind != lens.TypeAudio || drivers["internet-archive"].Kind != lens.TypeVideo || drivers["apple-movies"].Kind != lens.TypeVideo {
		t.Fatalf("an audio driver declares its kind and every other driver defaults to video: audio %q, archive %q", d.Kind, drivers["internet-archive"].Kind)
	}
	if d.Host() != "archive.org" || d.HasMarkets() {
		t.Fatalf("the audio driver searches archive.org without storefront markets: %s %v", d.Host(), d.Markets())
	}
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "archive.org" || req.URL.Path != "/advancedsearch.php" || q.Get("q") != `title:("Kind of Blue") AND mediatype:audio` || q.Get("fl[]") != "identifier,title,creator,year,date,collection" || q.Get("rows") != "30" || q.Get("output") != "json" {
			t.Errorf("unexpected archive audio query: %s", req.URL)
		}
		return jsonResponse(200, `{"response":{"numFound":4,"docs":[
			{"collection":["radioopensource","podcasts"],"creator":"Open Source Media","date":"2014-06-18T00:00:00Z","identifier":"miles-davis-open-source","title":"Miles Davis' Kind of Blue","year":2014},
			{"collection":["etree"],"creator":["Miles Davis","Miles Davis Sextet"],"date":"1959-08-17T00:00:00Z","identifier":"kind-of-blue-1959","title":"Kind of Blue"},
			{"collection":["radiofreecrockett"],"date":"2021-03-28T00:00:00Z","identifier":"kindofblue_202103","title":"Kind of Blue","year":2021},
			{"identifier":"kind of blue with spaces","title":"Kind of Blue","creator":"Nobody"}
		]}}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Kind of Blue", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 3 {
		t.Fatalf("expected the three valid docs, got %+v", listings)
	}
	first := listings[0]
	if first.Kind != lens.TypeAudio || first.ID != "ia-audio-miles-davis-open-source" || first.URL != "https://archive.org/details/miles-davis-open-source" || first.Title != "Miles Davis' Kind of Blue" || first.Creator != "Open Source Media" || first.Year != 2014 {
		t.Fatalf("first doc: %+v", first)
	}
	if first.SourceType != "Archive audio" || first.Provider != "Internet Archive" || first.AccessType != "unknown" || !first.ListingOnly || first.Duration != "" || first.Thumbnail != "https://archive.org/services/img/miles-davis-open-source" || len(first.Tags) != 2 {
		t.Fatalf("first doc provider fields: %+v", first)
	}
	if second := listings[1]; second.Creator != "Miles Davis" || second.Year != 1959 {
		t.Fatalf("an array creator reads as its first entry and a year falls back to the date: %+v", second)
	}
	if third := listings[2]; third.Creator != "" || third.Year != 2021 {
		t.Fatalf("a doc without a creator lists with none: %+v", third)
	}
	if status := d.StatusName(""); status != "Internet Archive audio" {
		t.Fatalf("status name: %q", status)
	}
}

func TestDriverKindsAreValidatedAndDefaultToVideo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := "name: %s\nprovider: Fixture\n%shosts: [fixture.example]\nsearch:\n  url: https://fixture.example/search\n  params: { q: \"{query}\" }\n  items: items\n  fields:\n    title: { path: title }\n    url: { path: url }\n"
	d, err := LoadFile(write("plain.yaml", strings.Replace(strings.Replace(base, "%s", "plain", 1), "%s", "", 1)))
	if err != nil || d.Kind != lens.TypeVideo {
		t.Fatalf("a driver without a kind lists video: %+v %v", d, err)
	}
	d, err = LoadFile(write("audio.yaml", strings.Replace(strings.Replace(base, "%s", "audio", 1), "%s", "kind: Audio\n", 1)))
	if err != nil || d.Kind != lens.TypeAudio {
		t.Fatalf("a kind is read case-insensitively: %+v %v", d, err)
	}
	if _, err := LoadFile(write("hologram.yaml", strings.Replace(strings.Replace(base, "%s", "hologram", 1), "%s", "kind: hologram\n", 1))); err == nil || !strings.Contains(err.Error(), "hologram") {
		t.Fatalf("an unknown kind is refused by name: %v", err)
	}
}
