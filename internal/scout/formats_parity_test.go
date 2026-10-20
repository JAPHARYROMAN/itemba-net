package scout

import (
	"sort"
	"strings"
	"testing"

	"github.com/itemba-net/core/internal/convert"
	"github.com/itemba-net/core/internal/formats"
)

// TestScoutTablesMatchTheFormatMatrix holds the scout's own format tables
// to the matrix, so nothing is sampled, probed or converted that the
// matrix does not name, and the matrix names nothing the scout cannot do.
func TestScoutTablesMatchTheFormatMatrix(t *testing.T) {
	// The audio containers the sample takes are the audio rows with a probe.
	var sampled []string
	for ext := range audioExtensions {
		sampled = append(sampled, strings.TrimPrefix(ext, "."))
	}
	sort.Strings(sampled)
	var probed []string
	for _, entry := range formats.All() {
		if entry.Kind == "audio" && entry.Probe == formats.ProbeMedia {
			probed = append(probed, entry.Extensions...)
		}
	}
	sort.Strings(probed)
	if strings.Join(sampled, ",") != strings.Join(probed, ",") {
		t.Fatalf("the audio containers the sample takes are the matrix's probed audio rows: %v vs %v", sampled, probed)
	}

	// The picture formats the probe decodes are the image rows with a
	// probe, and the viewer takes every image row with a relay path.
	for _, entry := range formats.All() {
		if entry.Kind != "image" {
			continue
		}
		switch entry.Format {
		case "jpeg", "png", "gif":
			if entry.Probe != formats.ProbeImage || entry.Fingerprint == "" {
				t.Fatalf("the probe decodes %s and hashes it: %+v", entry.Format, entry)
			}
		default:
			if entry.Probe != "" {
				t.Fatalf("the probe cannot decode %s: %+v", entry.Format, entry)
			}
		}
	}

	// The document formats a converter takes are the rows that name it.
	for _, entry := range formats.All() {
		if entry.Kind != "document" || entry.Converter == "" {
			continue
		}
		for _, ext := range entry.Extensions {
			if !convert.Convertible(ext) {
				t.Fatalf("the matrix names %s as converted but the converter does not take it", ext)
			}
		}
	}
	for _, ext := range []string{"epub", "mobi", "azw3", "djvu", "docx", "pptx", "xlsx"} {
		entry, ok := formats.ByExtension(ext)
		if !ok || entry.Converter == "" {
			t.Fatalf("every format the converter takes is in the matrix as converted: %s", ext)
		}
	}

	// The harness relays what the matrix says it plays.
	for _, entry := range formats.All() {
		if entry.Kind == "video" && entry.Relay == formats.RelayPlayer && entry.Format != "hls" {
			if _, ok := playbackResponseType(entry.MimeTypes[0], nil); !ok {
				t.Fatalf("the playback relay admits %s (%s)", entry.Format, entry.MimeTypes[0])
			}
		}
	}
	if HLSSupported() != (len(moviePlaybackHLSScript) > 0) {
		t.Fatal("HLSSupported says whether the library was built in")
	}
}
