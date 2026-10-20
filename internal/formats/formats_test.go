package formats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatrixIsConsistent(t *testing.T) {
	seenExt, seenFormat := map[string]string{}, map[string]bool{}
	for _, entry := range All() {
		if entry.Kind == "" || entry.Format == "" || entry.Label == "" || len(entry.Extensions) == 0 {
			t.Fatalf("every row names its kind, format, label and extensions: %+v", entry)
		}
		if seenFormat[entry.Kind+"/"+entry.Format] {
			t.Fatalf("a format appears once per kind: %s/%s", entry.Kind, entry.Format)
		}
		seenFormat[entry.Kind+"/"+entry.Format] = true
		for _, ext := range entry.Extensions {
			if ext != strings.ToLower(ext) || strings.HasPrefix(ext, ".") {
				t.Fatalf("extensions are lower-case without a dot: %q", ext)
			}
			if owner, dup := seenExt[ext]; dup {
				t.Fatalf("an extension names one row: %q in %s and %s/%s", ext, owner, entry.Kind, entry.Format)
			}
			seenExt[ext] = entry.Kind + "/" + entry.Format
		}
		if entry.Converter != "" && entry.Kind != "document" {
			t.Fatalf("only documents are converted: %+v", entry)
		}
		if entry.Converter != "" && entry.Probe != "" {
			t.Fatalf("a converted format has no probe of its own: %+v", entry)
		}
		if entry.Fingerprint != "" && entry.Probe == "" {
			t.Fatalf("a fingerprint comes from a probe or a sample: %+v", entry)
		}
	}
	if kinds := Kinds(); strings.Join(kinds, ",") != "video,audio,image,document" {
		t.Fatalf("the kinds in order: %v", kinds)
	}
}

func TestLookupsAndAvailability(t *testing.T) {
	if e, ok := ByExtension(".JPG"); !ok || e.Format != "jpeg" || e.Relay != RelayViewer || e.Fingerprint != "image-dhash-v1" {
		t.Fatalf("an extension finds its row whatever its case or dot: %+v %v", e, ok)
	}
	if e, ok := ByFormat("document", "epub"); !ok || e.Converter != ConverterEbook || e.Relay != RelayReader {
		t.Fatalf("a format finds its row: %+v %v", e, ok)
	}
	if _, ok := ByExtension("exe"); ok {
		t.Fatal("an unknown extension is outside the matrix")
	}
	for ext, want := range map[string]bool{"mp4": true, "m3u8": true, "mpd": false, "ts": false, "mp3": true, "svg": false, "pdf": true, "txt": false, "epub": true, "docx": true, "html": false, "exe": false} {
		if Presentable(ext) != want {
			t.Errorf("Presentable(%q) = %v", ext, !want)
		}
	}
	players := Extensions("video", RelayPlayer)
	if strings.Join(players, ",") != "m3u8,m4v,mkv,mov,mp4,ogv,webm" {
		t.Fatalf("the video extensions the player takes: %v", players)
	}
	if all := Extensions("video", ""); len(all) != len(players)+2 {
		t.Fatalf("every video extension includes the two the player never takes: %v", all)
	}
	readers := Extensions("document", RelayReader)
	for _, ext := range []string{"pdf", "epub", "djvu", "docx"} {
		found := false
		for _, r := range readers {
			if r == ext {
				found = true
			}
		}
		if !found {
			t.Fatalf("the reader takes %s: %v", ext, readers)
		}
	}

	bare := Availability(Host{})
	withTools := Availability(Host{Converters: []string{ConverterEbook}, HLS: true, Browser: true})
	byFormat := func(rows []Available, kind, format string) Available {
		for _, row := range rows {
			if row.Kind == kind && row.Format == format {
				return row
			}
		}
		t.Fatalf("no row %s/%s", kind, format)
		return Available{}
	}
	if row := byFormat(bare, "document", "epub"); row.RelayAvailable || row.SandboxAvailable || !strings.Contains(row.Because, "needs ebook-convert") {
		t.Fatalf("an EPUB on a host without Calibre is not readable or renderable, and says why: %+v", row)
	}
	if row := byFormat(withTools, "document", "epub"); !row.RelayAvailable || !row.SandboxAvailable || row.Because != "" {
		t.Fatalf("an EPUB on a host with Calibre and a browser is both: %+v", row)
	}
	if row := byFormat(withTools, "document", "djvu"); row.RelayAvailable || !strings.Contains(row.Because, "needs ddjvu") {
		t.Fatalf("DjVu still needs its own tool: %+v", row)
	}
	if row := byFormat(bare, "video", "hls"); row.RelayAvailable || !strings.Contains(row.Because, "HLS library") {
		t.Fatalf("HLS needs the built library: %+v", row)
	}
	if row := byFormat(bare, "image", "jpeg"); !row.RelayAvailable || row.SandboxAvailable || !strings.Contains(row.Because, "no browser") {
		t.Fatalf("a picture is viewable without a browser but not renderable: %+v", row)
	}
	if row := byFormat(withTools, "video", "dash"); row.RelayAvailable || row.SandboxAvailable || row.Because != "" {
		t.Fatalf("DASH is outside the matrix on any host, with no host reason: %+v", row)
	}
}

// TestDashboardFormatsAreGenerated holds the dashboard's copy of the
// matrix to this one: web/src/media/formats.generated.ts is written from
// All() when ITEMBA_UPDATE_FORMATS is set and must otherwise match.
func TestDashboardFormatsAreGenerated(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "media", "formats.generated.ts")
	want := generatedTS(t)
	if os.Getenv("ITEMBA_UPDATE_FORMATS") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the dashboard's matrix is missing; run this test with ITEMBA_UPDATE_FORMATS=1: %v", err)
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatal("the dashboard's matrix is stale; run this test with ITEMBA_UPDATE_FORMATS=1")
	}
}

func generatedTS(t *testing.T) string {
	t.Helper()
	data, err := json.MarshalIndent(All(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return "// Generated from internal/formats by `ITEMBA_UPDATE_FORMATS=1 go test ./internal/formats/`.\n" +
		"// The format matrix: what the console presents per path. Do not edit; edit the Go table.\n" +
		"export interface FormatEntry {\n  kind: 'video' | 'audio' | 'image' | 'document';\n  format: string;\n  label: string;\n  extensions: string[];\n  mimeTypes?: string[];\n  relay?: 'player' | 'viewer' | 'reader';\n  probe?: string;\n  sandbox?: 'sample' | 'render';\n  converter?: string;\n  fingerprint?: string;\n  note?: string;\n}\n\n" +
		"export const FORMATS: FormatEntry[] = " + string(data) + ";\n"
}
