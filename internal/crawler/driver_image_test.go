package crawler

import (
	"context"
	"net/http"
	"testing"

	"github.com/itemba-net/core/internal/lens"
)

// The fixture is the Commons API's own answer to a picture search on
// 20 October 2026, trimmed to the fields the driver reads: a page with every
// field, one whose credit is a link and whose licence is a short name, and
// one without a thumbnail.
func TestCommonsImageDriverListsPicturesWithTheirFilesAndCredits(t *testing.T) {
	drivers := shippedDrivers(t)
	d := drivers["wikimedia-commons-images"]
	if d == nil {
		t.Fatal("the wikimedia-commons-images driver is not shipped")
	}
	if d.Kind != lens.TypeImage || drivers["wikimedia-commons"].Kind != lens.TypeVideo || d.Host() != "commons.wikimedia.org" || d.HasMarkets() {
		t.Fatalf("the picture driver declares its kind beside the video driver: %q %q", d.Kind, drivers["wikimedia-commons"].Kind)
	}
	fetcher := NewFetcher(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		if req.URL.Host != "commons.wikimedia.org" || req.URL.Path != "/w/api.php" || q.Get("gsrsearch") != "filetype:bitmap Moon landing" || q.Get("gsrnamespace") != "6" || q.Get("iiprop") != "url|size|mime|extmetadata" || q.Get("iiurlwidth") != "640" || req.Header.Get("User-Agent") != "ITEMBA-NET/2 (personal research crawler)" {
			t.Errorf("unexpected commons query: %s %q", req.URL, req.Header.Get("User-Agent"))
		}
		return jsonResponse(200, `{"batchcomplete":"","query":{"pages":{
			"3264202":{"pageid":3264202,"ns":6,"title":"File:Land on the Moon 7 21 1969-repair.jpg","index":1,"imageinfo":[{"size":1304009,"width":2356,"height":2795,
				"thumburl":"https://thumb.wikimedia.org/wikipedia/commons/thumb/9/9a/Land_on_the_Moon.jpg/960px-Land_on_the_Moon.jpg?utm_source=commons.wikimedia.org&utm_campaign=imageinfo&utm_content=thumbnail",
				"url":"https://upload.wikimedia.org/wikipedia/commons/9/9a/Land_on_the_Moon_7_21_1969-repair.jpg?utm_source=commons.wikimedia.org&utm_campaign=imageinfo&utm_content=original",
				"descriptionurl":"https://commons.wikimedia.org/wiki/File:Land_on_the_Moon_7_21_1969-repair.jpg",
				"extmetadata":{"ImageDescription":{"value":"This is a picture of my mother holding <i><a href=\"https://en.wikipedia.org/wiki/The_Washington_Post\">The Washington Post</a></i> on Monday, July 21, 1969.","source":"commons-desc-page"},"DateTimeOriginal":{"value":"Taken on 21 July 1969","source":"commons-desc-page"},"Artist":{"value":"Jack Weir (1928-2005)","source":"commons-desc-page"},"LicenseShortName":{"value":"Public domain","source":"commons-desc-page","hidden":""}},"mime":"image/jpeg"}]},
			"20928389":{"pageid":20928389,"ns":6,"title":"File:Lunar Landing Research Vehicle (LLRV) in flight.jpg","index":3,"imageinfo":[{"size":4716458,"width":5100,"height":4000,
				"thumburl":"https://thumb.wikimedia.org/wikipedia/commons/thumb/e/e6/LLRV.jpg/960px-LLRV.jpg?utm_source=commons.wikimedia.org",
				"url":"https://upload.wikimedia.org/wikipedia/commons/e/e6/Lunar_Landing_Research_Vehicle_%28LLRV%29_in_flight.jpg?utm_source=commons.wikimedia.org",
				"descriptionurl":"https://commons.wikimedia.org/wiki/File:Lunar_Landing_Research_Vehicle_(LLRV)_in_flight.jpg",
				"extmetadata":{"Artist":{"value":"<a href=\"https://www.nasa.gov\">NASA</a>","source":"commons-desc-page"},"LicenseShortName":{"value":"Public domain","source":"commons-desc-page"}},"mime":"image/jpeg"}]},
			"99":{"pageid":99,"ns":6,"title":"File:Moon map.png","index":2,"imageinfo":[{"size":100,"width":800,"height":600,
				"url":"https://upload.wikimedia.org/wikipedia/commons/1/11/Moon_map.png",
				"descriptionurl":"https://commons.wikimedia.org/wiki/File:Moon_map.png","extmetadata":{},"mime":"image/png"}]}
		}}}`), nil
	}))
	listings, err := d.Search(context.Background(), fetcher, "Moon landing", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listings) != 3 {
		t.Fatalf("expected the three pages, got %+v", listings)
	}
	byID := map[string]int{}
	for i, listing := range listings {
		byID[listing.ID] = i
	}
	first := listings[byID["wm-image-3264202"]]
	if first.Kind != lens.TypeImage || first.Title != "Land on the Moon 7 21 1969-repair" || first.URL != "https://commons.wikimedia.org/wiki/File:Land_on_the_Moon_7_21_1969-repair.jpg" {
		t.Fatalf("first page: %+v", first)
	}
	if first.MediaURL != "https://upload.wikimedia.org/wikipedia/commons/9/9a/Land_on_the_Moon_7_21_1969-repair.jpg" || first.Thumbnail != "https://thumb.wikimedia.org/wikipedia/commons/thumb/9/9a/Land_on_the_Moon.jpg/960px-Land_on_the_Moon.jpg" {
		t.Fatalf("the file and thumbnail URLs lose their tracking parameters: %q %q", first.MediaURL, first.Thumbnail)
	}
	if first.Resolution != "2356x2795" || first.Creator != "Jack Weir (1928-2005)" || first.License != "Public domain" || first.Year != 1969 || first.ListingOnly {
		t.Fatalf("dimensions, credit, licence and year: %+v", first)
	}
	if first.Description != "This is a picture of my mother holding The Washington Post on Monday, July 21, 1969." || first.SourceType != "Open picture" || first.AccessType != "free" {
		t.Fatalf("the description is the rendered text and the constants hold: %+v", first)
	}
	if second := listings[byID["wm-image-20928389"]]; second.Creator != "NASA" || second.Title != "Lunar Landing Research Vehicle (LLRV) in flight" || second.MediaURL != "https://upload.wikimedia.org/wikipedia/commons/e/e6/Lunar_Landing_Research_Vehicle_%28LLRV%29_in_flight.jpg" {
		t.Fatalf("a credit that is a link reads as its text: %+v", second)
	}
	if third := listings[byID["wm-image-99"]]; third.Thumbnail != "" || third.Creator != "" || third.License != "" || third.Year != 0 || third.Description != "Picture listing from Wikimedia Commons." {
		t.Fatalf("missing metadata leaves fields empty and the description defaulted: %+v", third)
	}
}
