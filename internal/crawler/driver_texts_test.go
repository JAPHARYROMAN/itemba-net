package crawler

import (
	"context"
	"net/http"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// The fixture is the Archive's own answer to a texts search on 20 October
// 2026, trimmed to the fields the driver reads: a scanned book with every
// field, a transcription whose creator is a plain string and whose date
// stands in for a year, and an item with neither.
func TestArchiveTextsDriverListsDocumentsWithTheirPagesAndAuthors(t *testing.T) {
	drivers := shippedDrivers(t)
	d := drivers["internet-archive-texts"]
	if d == nil {
		t.Fatal("the internet-archive-texts driver is not shipped")
	}
	if d.Kind != lens.TypeDocument || drivers["internet-archive-audio"].Kind != lens.TypeAudio || d.Host() != "archive.org" || d.HasMarkets() || d.StatusName("") != "Internet Archive texts" {
		t.Fatalf("the texts driver declares its kind beside the audio driver: %q %q", d.Kind, d.StatusName(""))
	}
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "archive.org" || req.URL.Path != "/advancedsearch.php" || q.Get("q") != `title:("Alice's Adventures in Wonderland") AND mediatype:texts` || q.Get("fl[]") != "identifier,title,creator,year,date,collection,imagecount,publisher" || q.Get("output") != "json" {
			t.Errorf("unexpected archive query: %s", req.URL)
		}
		return jsonResponse(200, `{"response":{"numFound":3,"start":0,"docs":[
			{"identifier":"alicesadventur00carr","title":"Alice's adventures in wonderland","creator":["Carroll, Lewis, 1832-1898","Tenniel, John, 1820-1914"],"year":"1865","date":"1865-01-01T00:00:00Z","collection":["americana","library_of_congress"],"imagecount":232,"publisher":"Macmillan and Co."},
			{"identifier":"alice-transcription","title":"Alice's Adventures in Wonderland","creator":"Lewis Carroll","date":"1991-03-01T00:00:00Z","collection":["gutenberg"]},
			{"identifier":"alice-notes","title":"Notes on Alice's Adventures in Wonderland"}
		]}}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Alice's Adventures in Wonderland", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 3 {
		t.Fatalf("expected the three items, got %+v", listings)
	}
	first := listings[0]
	if first.ID != "ia-text-alicesadventur00carr" || first.Kind != lens.TypeDocument || first.Title != "Alice's adventures in wonderland" || first.URL != "https://archive.org/details/alicesadventur00carr" {
		t.Fatalf("first item: %+v", first)
	}
	if first.Creator != "Carroll, Lewis, 1832-1898" || first.Year != 1865 || first.Pages != 232 || !first.ListingOnly || first.MediaURL != "" {
		t.Fatalf("the primary credit, the year and the stated page count; no file until the metadata is read: %+v", first)
	}
	if first.Thumbnail != "https://archive.org/services/img/alicesadventur00carr" || len(first.Tags) != 2 || first.Tags[1] != "library_of_congress" || first.SourceType != "Archive text" || first.AccessType != "unknown" {
		t.Fatalf("thumbnail, collections and constants: %+v", first)
	}
	if second := listings[1]; second.Creator != "Lewis Carroll" || second.Year != 1991 || second.Pages != 0 || second.Description != "Text listing from Internet Archive. Access and rights depend on the item." {
		t.Fatalf("a lone credit is a string, a date stands in for the year, no count is zero: %+v", second)
	}
	if third := listings[2]; third.Creator != "" || third.Year != 0 || third.Pages != 0 || third.Thumbnail == "" {
		t.Fatalf("missing metadata leaves fields empty: %+v", third)
	}
}
