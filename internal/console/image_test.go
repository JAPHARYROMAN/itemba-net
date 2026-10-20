package console

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// TestImageProbeBecomesIdentifiedEvidenceWithItsHash proves the bridge for
// pictures: a decoded file is identified evidence by the image_decode
// method with its dimensions, the hash goes to the fingerprint library, and
// the merged list ranks the picture by the host's match at least.
func TestImageProbeBecomesIdentifiedEvidenceWithItsHash(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x * 6) % 256)})
		}
	}
	hash := lens.ImageFingerprint(img)
	source := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{
			ID: "wm-image-1", Kind: lens.TypeImage, Title: "Land on the Moon 7 21 1969-repair", Creator: "Jack Weir (1928-2005)", License: "Public domain",
			URL: "https://commons.wikimedia.org/wiki/File:Land_on_the_Moon.jpg", DirectStreamURL: "https://upload.wikimedia.org/wikipedia/commons/9/9a/Land_on_the_Moon.jpg",
			Provider: "Wikimedia Commons", SourceType: "Open picture", AccessType: "free", ReleaseYear: 1969, Resolution: "2356x2795",
		},
		Verified: true, VerificationStatus: "media_reachable", StreamFormat: "JPEG image", DetectedManifests: []string{}, CheckedAt: "2026-10-20T06:00:00Z",
		ImageCheck:        &scout.ImageCheck{Status: "decoded", Method: "image_decode", CheckedAt: "2026-10-20T06:00:00Z", Width: 2356, Height: 2795, Format: "jpeg", Bytes: 1304009, HTTPStatus: 206, Fingerprint: &hash, FingerprintStatus: "computed", Detail: "decoded"},
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Method: "http_media_probe"},
	}
	report := scout.SearchReport{Query: "Moon landing", Kind: lens.TypeImage, Region: "ALL", Sources: []scout.VerifiedSource{source}, VerifiedSources: []scout.VerifiedSource{source}, AvailabilityChecks: &scout.AvailabilityCheckCoverage{Country: "TZ"}}

	observations := observationsFromReport(report)
	if len(observations) != 1 || observations[0].Kind != lens.TypeImage || observations[0].Creator != "Jack Weir (1928-2005)" {
		t.Fatalf("the observation carries the kind and the maker: %+v", observations)
	}
	if observations[0].Fingerprint == nil || !bytes.Equal(observations[0].Fingerprint.Fingerprint.Data, hash.Data) || observations[0].Fingerprint.MediaSeconds != 0 {
		t.Fatalf("the hash goes to the fingerprint library with no length: %+v", observations[0].Fingerprint)
	}
	var decoded, reachable bool
	for _, e := range observations[0].Evidence {
		switch e.Method {
		case "image_decode":
			decoded = e.Level == lens.LevelIdentified && e.Status == "decoded" && e.Artefacts["width"] == 2356 && e.Artefacts["format"] == "jpeg" && e.Artefacts["fingerprintStatus"] == "computed" && e.Egress == "TZ"
		case "http_media_probe":
			reachable = e.Level == lens.LevelIdentified
		}
	}
	if !decoded || !reachable {
		t.Fatalf("a decoded picture is identified evidence with its dimensions: %+v", observations[0].Evidence)
	}

	q := lens.ParseAs("Moon landing", lens.TypeImage)
	merged := mergeLocations(q, nil, report, nil)
	if len(merged.Locations) != 1 || merged.Locations[0].Level != lens.LevelIdentified || merged.Locations[0].Relevance < lens.HostMatchRelevance || merged.Locations[0].Creator != "Jack Weir (1928-2005)" {
		t.Fatalf("the merged list ranks the picture at identified with the host's match at least: %+v", merged.Locations)
	}
}
