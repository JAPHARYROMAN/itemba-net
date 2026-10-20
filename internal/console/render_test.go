package console

import (
	"testing"

	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// TestSandboxRenderBecomesRenderedEvidence proves the bridge for renders: a
// picture the sandbox drew is rendered evidence by its method with what was
// drawn, a document frame that stayed blank keeps the reachable level, and
// the merged list raises a rendered picture to the ladder's rendered step.
func TestSandboxRenderBecomesRenderedEvidence(t *testing.T) {
	picture := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{ID: "wm-image-1", Kind: lens.TypeImage, Title: "Starry Night", Creator: "Vincent van Gogh", URL: "https://commons.wikimedia.org/wiki/File:Starry_Night.jpg", DirectStreamURL: "https://upload.wikimedia.org/wikipedia/commons/e/ea/Starry_Night.jpg", Thumbnail: "https://upload.wikimedia.org/thumb/640px-Starry_Night.jpg", Provider: "Wikimedia Commons", SourceType: "Open picture", AccessType: "free"},
		Verified:        true, VerificationStatus: "media_reachable", DetectedManifests: []string{}, CheckedAt: "2026-10-20T06:00:00Z",
		ImageCheck:        &scout.ImageCheck{Status: "decoded", Method: "image_decode", CheckedAt: "2026-10-20T06:00:00Z", Width: 1280, Height: 1014, Format: "jpeg", HTTPStatus: 206, Detail: "decoded"},
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Method: "http_media_probe"},
		RenderCheck:       &scout.RenderCheck{Status: "rendered", Method: scout.MethodImageRender, Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:05Z", EvidenceURL: "https://upload.wikimedia.org/thumb/640px-Starry_Night.jpg", Width: 640, Height: 507, Milliseconds: 1200, HTTPStatus: 200, MimeType: "image/jpeg", HashAgrees: "yes", HashDistance: 0.02, Detail: "drawn"},
	}
	document := scout.VerifiedSource{
		CandidateSource: scout.CandidateSource{ID: "ia-text-alice", Kind: lens.TypeDocument, Title: "Alice in Wonderland", Creator: "Lewis Carroll", URL: "https://archive.org/details/alice", DirectStreamURL: "https://archive.org/download/alice/alice.pdf", Provider: "Internet Archive", SourceType: "Archive text", AccessType: "free"},
		Verified:        true, VerificationStatus: "media_reachable", DetectedManifests: []string{}, CheckedAt: "2026-10-20T06:00:00Z",
		DocumentCheck:     &scout.DocumentCheck{Status: "parsed", Method: "document_parse", CheckedAt: "2026-10-20T06:00:00Z", Format: "pdf", Pages: 142, PagesConfirmed: true, HTTPStatus: 206, Detail: "parsed"},
		AvailabilityCheck: &scout.AvailabilityCheck{Status: "media_reachable", Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:00Z", Method: "http_media_probe"},
		RenderCheck:       &scout.RenderCheck{Status: "unconfirmed", Method: scout.MethodDocumentRender, Country: "TZ", RequestedRegion: "ALL", CheckedAt: "2026-10-20T06:00:05Z", FrameLoaded: true, HTTPStatus: 206, MimeType: "application/pdf", VisibleShare: 0.004, Detail: "blank"},
	}
	report := scout.SearchReport{Query: "Starry Night by Van Gogh", Kind: lens.TypeImage, Region: "ALL", Sources: []scout.VerifiedSource{picture, document}, VerifiedSources: []scout.VerifiedSource{picture, document}, AvailabilityChecks: &scout.AvailabilityCheckCoverage{Country: "TZ"}}

	observations := observationsFromReport(report)
	if len(observations) != 2 {
		t.Fatalf("two observations: %+v", observations)
	}
	var pictureRendered, documentReachable bool
	for _, e := range observations[0].Evidence {
		if e.Method == scout.MethodImageRender {
			pictureRendered = e.Level == lens.LevelRendered && e.Status == "rendered" && e.Artefacts["width"] == 640 && e.Artefacts["hashAgrees"] == "yes" && e.Artefacts["mimeType"] == "image/jpeg" && e.Egress == "TZ"
		}
	}
	for _, e := range observations[1].Evidence {
		if e.Method == scout.MethodDocumentRender {
			documentReachable = e.Level == lens.LevelReachable && e.Status == "unconfirmed" && e.Artefacts["frameLoaded"] == true
		}
	}
	if !pictureRendered || !documentReachable {
		t.Fatalf("a drawn picture is rendered evidence and a blank frame stays reachable: %+v %+v", observations[0].Evidence, observations[1].Evidence)
	}

	q := lens.ParseAs("Starry Night by Van Gogh", lens.TypeImage)
	merged := mergeLocations(q, nil, report, nil)
	levels := map[string]int{}
	for _, m := range merged.Locations {
		levels[m.URL] = m.Level
	}
	if levels[picture.URL] != lens.LevelRendered || levels[document.URL] != lens.LevelIdentified {
		t.Fatalf("the merged list raises the drawn picture to rendered and leaves the blank frame at identified: %+v", levels)
	}
}
