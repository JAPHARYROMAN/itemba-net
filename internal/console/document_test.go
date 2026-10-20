package console

import (
	"bytes"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// TestDocumentProbeBecomesIdentifiedEvidenceWithItsFingerprint proves the
// bridge for documents: a parsed file is identified evidence by the
// document_parse method with its page count, the text fingerprint goes to
// the fingerprint library, and the merged list ranks the document by its
// title and author with the host's match as the floor.
func TestDocumentProbeBecomesIdentifiedEvidenceWithItsFingerprint(t *testing.T) {
	words := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		words = append(words, []string{"river", "mill", "ledger", "bridge", "ferryman", "lantern", "willow", "bell"}[(i*7+i/3)%8])
	}
	fingerprint := lens.TextFingerprint(strings.Join(words, " "))
	source := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{
			ID: "ia-text-alicesadventur00carr", Kind: lens.TypeDocument, Title: "Alice's adventures in wonderland", Creator: "Carroll, Lewis, 1832-1898", Pages: 232,
			URL: "https://archive.org/details/alicesadventur00carr", DirectStreamURL: "https://archive.org/download/alicesadventur00carr/alicesadventur00carr.pdf", TextURL: "https://archive.org/download/alicesadventur00carr/alicesadventur00carr_djvu.txt",
			Provider: "Internet Archive", SourceType: "Archive text", AccessType: "free", ReleaseYear: 1865,
		},
		Verified: true, VerificationStatus: "media_reachable", StreamFormat: "PDF document", DetectedManifests: []string{}, CheckedAt: "2026-10-20T06:00:00Z",
		DocumentCheck:     &scout.DocumentCheck{Status: "parsed", Method: "document_parse", CheckedAt: "2026-10-20T06:00:00Z", Format: "pdf", Pages: 232, PagesConfirmed: true, Bytes: 13276855, HTTPStatus: 206, TextChars: 24000, TextSource: "ocr_text", Fingerprint: &fingerprint, FingerprintStatus: "computed", Detail: "parsed"},
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Method: "http_media_probe"},
	}
	report := scout.SearchReport{Query: "Alice in Wonderland by Lewis Carroll", Kind: lens.TypeDocument, Region: "ALL", Sources: []scout.VerifiedSource{source}, VerifiedSources: []scout.VerifiedSource{source}, AvailabilityChecks: &scout.AvailabilityCheckCoverage{Country: "TZ"}}

	observations := observationsFromReport(report)
	if len(observations) != 1 || observations[0].Kind != lens.TypeDocument || observations[0].Creator != "Carroll, Lewis, 1832-1898" || observations[0].Pages != 232 {
		t.Fatalf("the observation carries the kind, the author and the page count: %+v", observations)
	}
	if observations[0].Fingerprint == nil || !bytes.Equal(observations[0].Fingerprint.Fingerprint.Data, fingerprint.Data) || observations[0].Fingerprint.Fingerprint.Type != lens.FingerprintTextSimhash {
		t.Fatalf("the text fingerprint goes to the fingerprint library: %+v", observations[0].Fingerprint)
	}
	var parsed, reachable bool
	for _, e := range observations[0].Evidence {
		switch e.Method {
		case "document_parse":
			parsed = e.Level == lens.LevelIdentified && e.Status == "parsed" && e.Artefacts["pages"] == 232 && e.Artefacts["pagesConfirmed"] == true && e.Artefacts["format"] == "pdf" && e.Artefacts["textSource"] == "ocr_text" && e.Artefacts["fingerprintStatus"] == "computed" && e.Egress == "TZ"
		case "http_media_probe":
			reachable = e.Level == lens.LevelIdentified
		}
	}
	if !parsed || !reachable {
		t.Fatalf("a parsed document is identified evidence with its page count: %+v", observations[0].Evidence)
	}

	q := lens.ParseAs("Alice in Wonderland by Lewis Carroll", lens.TypeDocument)
	merged := mergeLocations(q, nil, report, nil)
	if len(merged.Locations) != 1 || merged.Locations[0].Level != lens.LevelIdentified || merged.Locations[0].Relevance < lens.HostMatchRelevance || merged.Locations[0].Creator != "Carroll, Lewis, 1832-1898" {
		t.Fatalf("the merged list ranks the document at identified with the host's match at least: %+v", merged.Locations)
	}
	q = lens.ParseAs("Alice in Wonderland by Someone Else", lens.TypeDocument)
	if other := mergeLocations(q, nil, report, nil); other.Locations[0].Relevance >= merged.Locations[0].Relevance {
		t.Fatalf("another author ranks the document lower: %v vs %v", other.Locations[0].Relevance, merged.Locations[0].Relevance)
	}
}
