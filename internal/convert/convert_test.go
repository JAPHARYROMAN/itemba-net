package convert

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRun stands in for a converter: it writes a PDF where the command
// would, or whatever the test says.
func fakeRun(output []byte, fail error) func(context.Context, *exec.Cmd) error {
	return func(_ context.Context, cmd *exec.Cmd) error {
		if fail != nil {
			cmd.Stderr.Write([]byte("the converter complained at length about the input file and then gave up"))
			return fail
		}
		return os.WriteFile(filepath.Join(cmd.Dir, "input.pdf"), output, 0o600)
	}
}

func testConverter(t *testing.T, run func(context.Context, *exec.Cmd) error, commands ...Command) *Converter {
	t.Helper()
	return &Converter{Commands: commands, CacheDir: t.TempDir(), CacheLimit: 1 << 20, InputLimit: 1 << 20, Timeout: 5 * time.Second, Run: run}
}

var pdfBytes = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")

func TestConverterRunsTheToolOnceAndCachesThePDF(t *testing.T) {
	var runs int
	c := testConverter(t, func(ctx context.Context, cmd *exec.Cmd) error {
		runs++
		if filepath.Base(cmd.Path) != "ebook-convert" || len(cmd.Args) != 3 || !strings.HasSuffix(cmd.Args[1], "input.epub") || !strings.HasSuffix(cmd.Args[2], "input.pdf") || cmd.Dir == "" {
			t.Errorf("ebook-convert takes the input and the output: %v in %q", cmd.Args, cmd.Dir)
		}
		for _, want := range []string{"CALIBRE_CONFIG_DIRECTORY=", "CALIBRE_TEMP_DIR="} {
			found := false
			for _, e := range cmd.Env {
				if strings.HasPrefix(e, want) && strings.Contains(e, cmd.Dir) {
					found = true
				}
			}
			if !found {
				t.Errorf("Calibre keeps its configuration in the scratch directory: %v", cmd.Env)
			}
		}
		for _, e := range cmd.Env {
			if strings.HasPrefix(strings.ToUpper(e), "HTTP_PROXY=") || strings.HasPrefix(strings.ToUpper(e), "HTTPS_PROXY=") {
				t.Errorf("no proxy reaches the converter: %s", e)
			}
		}
		return fakeRun(pdfBytes, nil)(ctx, cmd)
	}, Command{Name: "ebook-convert", Path: "ebook-convert", Formats: toolFormats["ebook-convert"]})
	input := []byte("PK\x03\x04 a fake epub")
	result, err := c.Convert(context.Background(), ".EPUB", input)
	if err != nil || result.Cached || result.Converter != "ebook-convert" || result.Format != "epub" || result.Bytes != int64(len(pdfBytes)) || result.Key == "" {
		t.Fatalf("the first conversion runs the tool and keeps the PDF: %+v %v", result, err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil || string(data) != string(pdfBytes) {
		t.Fatalf("the cached file is the PDF: %v", err)
	}
	again, err := c.Convert(context.Background(), "epub", input)
	if err != nil || !again.Cached || again.Path != result.Path || runs != 1 {
		t.Fatalf("the same bytes come from the cache without a run: %+v %v runs=%d", again, err, runs)
	}
	other, err := c.Convert(context.Background(), "epub", []byte("PK\x03\x04 another epub"))
	if err != nil || other.Cached || other.Key == result.Key || runs != 2 {
		t.Fatalf("other bytes convert again: %+v %v runs=%d", other, err, runs)
	}
	if entries, _ := os.ReadDir(c.CacheDir); len(entries) != 2 {
		t.Fatalf("the scratch directories are gone and two PDFs remain: %d entries", len(entries))
	}
	// The same bytes through another tool, or the tool installed elsewhere,
	// are converted again: the cache is keyed by the tool as well.
	c.Commands[0].Path = "/elsewhere/ebook-convert"
	moved, err := c.Convert(context.Background(), "epub", input)
	if err != nil || moved.Cached || moved.Key == result.Key || runs != 3 {
		t.Fatalf("a tool installed elsewhere converts afresh: %+v %v runs=%d", moved, err, runs)
	}
}

func TestConverterRefusesWhatItCannotDoAndSaysWhy(t *testing.T) {
	ebook := Command{Name: "ebook-convert", Path: "ebook-convert", Formats: toolFormats["ebook-convert"]}
	c := testConverter(t, fakeRun(pdfBytes, nil), ebook)
	if _, err := c.Convert(context.Background(), "docx", []byte("x")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a format no installed tool takes is unsupported: %v", err)
	}
	if _, err := c.Convert(context.Background(), "exe", []byte("x")); !errors.Is(err, ErrNotConvertible) {
		t.Fatalf("a format no tool takes at all is not convertible: %v", err)
	}
	none := testConverter(t, fakeRun(pdfBytes, nil))
	if _, err := none.Convert(context.Background(), "epub", []byte("x")); !errors.Is(err, ErrNoConverter) {
		t.Fatalf("a host without converters says so: %v", err)
	}
	if none.Available() || len(none.Names()) != 0 || len(none.Formats()) != 0 {
		t.Fatal("nothing is available on a host without converters")
	}
	c.InputLimit = 4
	if _, err := c.Convert(context.Background(), "epub", []byte("too big")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an input over the limit is refused: %v", err)
	}
	c.InputLimit = 1 << 20
	if _, err := c.Convert(context.Background(), "epub", nil); err == nil {
		t.Fatal("nothing to convert is an error")
	}
	failing := testConverter(t, fakeRun(nil, errors.New("exit status 1")), ebook)
	if _, err := failing.Convert(context.Background(), "epub", []byte("x")); err == nil || !strings.Contains(err.Error(), "ebook-convert failed") || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("a failing tool is reported with the end of its complaint: %v", err)
	}
	silent := testConverter(t, func(context.Context, *exec.Cmd) error { return nil }, ebook)
	if _, err := silent.Convert(context.Background(), "epub", []byte("x")); err == nil || !strings.Contains(err.Error(), "produced no PDF") {
		t.Fatalf("a tool that wrote nothing is reported: %v", err)
	}
	wrong := testConverter(t, fakeRun([]byte("<html>not a pdf</html>"), nil), ebook)
	if _, err := wrong.Convert(context.Background(), "epub", []byte("x")); err == nil || !strings.Contains(err.Error(), "not a PDF") {
		t.Fatalf("output that is not a PDF is refused: %v", err)
	}
	if entries, _ := os.ReadDir(wrong.CacheDir); len(entries) != 0 {
		t.Fatalf("a refused conversion leaves nothing in the cache: %d entries", len(entries))
	}
}

func TestConverterPrunesTheCacheOldestFirst(t *testing.T) {
	c := testConverter(t, fakeRun(pdfBytes, nil), Command{Name: "ddjvu", Path: "ddjvu", Formats: toolFormats["ddjvu"]})
	c.CacheLimit = int64(len(pdfBytes)) + 10
	first, err := c.Convert(context.Background(), "djvu", []byte("AT&TFORM first"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(first.Path, old, old)
	second, err := c.Convert(context.Background(), "djvu", []byte("AT&TFORM second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatal("the oldest PDF goes when the cache is over its limit")
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatal("the newest PDF stays")
	}
}

func TestCommandPlansEachToolAndFormatsAreKnown(t *testing.T) {
	work := filepath.Join("tmp", "work")
	input := filepath.Join(work, "input.docx")
	args, output, env := Command{Name: "soffice"}.plan(input, work)
	if output != filepath.Join(work, "input.pdf") || len(env) != 0 {
		t.Fatalf("soffice writes the PDF beside the input: %q %v", output, env)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--headless", "--convert-to pdf", "--outdir " + work, "-env:UserInstallation=file://", input} {
		if !strings.Contains(joined, want) {
			t.Fatalf("soffice arguments: %q lacks %q", joined, want)
		}
	}
	if args, output, _ := (Command{Name: "ddjvu"}).plan(filepath.Join(work, "input.djvu"), work); args[0] != "-format=pdf" || args[2] != output {
		t.Fatalf("ddjvu arguments: %v %q", args, output)
	}
	if fileURL("/tmp/profile") != "file:///tmp/profile" || fileURL(`C:\Users\x\profile`) != "file:///C:/Users/x/profile" {
		t.Fatalf("file URLs: %q %q", fileURL("/tmp/profile"), fileURL(`C:\Users\x\profile`))
	}
	for format, want := range map[string]bool{"epub": true, "MOBI": true, ".djvu": true, "docx": true, "pdf": false, "txt": false, "exe": false} {
		if Convertible(format) != want {
			t.Errorf("Convertible(%q) = %v", format, !want)
		}
	}
	if FormatOf("https://archive.org/download/x/book.EPUB?x=1") != "epub" || FormatOf("/a/b/scan.djvu#page") != "djvu" || FormatOf("/a/b/noext") != "" {
		t.Fatal("the format is the extension of the path, lower-case, without the query")
	}
}

func TestDetectHonoursOverridesAndFindsNothingOtherwise(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	t.Setenv("ITEMBA_EBOOK_CONVERT", self)
	t.Setenv("ITEMBA_DDJVU", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("ITEMBA_SOFFICE", "")
	c := Detect(t.TempDir())
	cmd, ok := c.Supports("epub")
	if !ok || cmd.Path != self || cmd.Name != "ebook-convert" {
		t.Fatalf("an override names the tool: %+v %v", cmd, ok)
	}
	if _, ok := c.Supports("djvu"); ok {
		t.Fatal("an override that points nowhere means the tool is absent")
	}
	if c.CacheDir == "" || c.InputLimit <= 0 || c.Timeout <= 0 || c.Run == nil {
		t.Fatalf("the detected converter has its limits: %+v", c)
	}
	if formats := c.Formats(); len(formats) == 0 || formats[0] != "azw" {
		t.Fatalf("formats are listed sorted: %v", formats)
	}
	if names := c.Names(); len(names) != 1 || names[0] != "ebook-convert" {
		t.Fatalf("names: %v", names)
	}
}
