// Package convert turns e-books and office files into PDFs for the reader
// and the sandbox render, by running a converter the host has: Calibre's
// ebook-convert for e-books, DjVuLibre's ddjvu for DjVu scans and
// LibreOffice's soffice for office files. The package finds the tools,
// runs one in a scratch directory with a timeout and a pared-down
// environment, checks that what came out is a PDF, and keeps the result in
// a bounded cache keyed by the input's digest, so a file is converted once.
// Nothing here fetches: the caller hands over the bytes it fetched through
// its own guards. A host with no converter says so; nothing is installed.
package convert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrUnsupported says no converter on this host takes the format.
	ErrUnsupported = errors.New("convert: no converter on this host takes this format")
	// ErrNoConverter says the host has no converter at all.
	ErrNoConverter = errors.New("convert: no converter on this host")
	// ErrTooLarge says the input exceeds the conversion limit.
	ErrTooLarge = errors.New("convert: the file exceeds the conversion limit")
	// ErrNotConvertible says the format is not one the reader converts.
	ErrNotConvertible = errors.New("convert: not a format the reader converts")
)

// toolFormats names the formats each known tool converts to PDF.
var toolFormats = map[string][]string{
	"ebook-convert": {"epub", "mobi", "azw3", "azw", "fb2", "lit"},
	"ddjvu":         {"djvu"},
	"soffice":       {"doc", "docx", "odt", "rtf", "ppt", "pptx", "odp", "xls", "xlsx", "ods"},
}

// toolOrder is the order tools are looked for and listed.
var toolOrder = []string{"ebook-convert", "ddjvu", "soffice"}

// Command is one converter the host has.
type Command struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Formats []string `json:"formats"`
}

// Supports reports whether the command takes the format.
func (c Command) Supports(format string) bool {
	for _, f := range c.Formats {
		if f == format {
			return true
		}
	}
	return false
}

// plan builds the command line that converts input inside workDir, names
// the PDF it should leave there, and the environment it needs: Calibre
// keeps its configuration in the scratch directory and LibreOffice its
// profile, so neither touches the host's.
func (c Command) plan(input, workDir string) (args []string, output string, env []string) {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	output = filepath.Join(workDir, base+".pdf")
	switch c.Name {
	case "ebook-convert":
		args = []string{input, output}
		env = []string{"CALIBRE_CONFIG_DIRECTORY=" + filepath.Join(workDir, "calibre"), "CALIBRE_TEMP_DIR=" + workDir, "CALIBRE_CACHE_DIRECTORY=" + filepath.Join(workDir, "calibre-cache")}
	case "ddjvu":
		args = []string{"-format=pdf", input, output}
	case "soffice":
		args = []string{"--headless", "--norestore", "--nologo", "-env:UserInstallation=" + fileURL(filepath.Join(workDir, "profile")), "--convert-to", "pdf", "--outdir", workDir, input}
	}
	return args, output, env
}

// fileURL is a path as a file URL, as LibreOffice wants its profile named.
func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if strings.HasPrefix(slashed, "/") {
		return "file://" + slashed
	}
	return "file:///" + slashed
}

// Converter finds and runs the host's converters and keeps their output.
type Converter struct {
	// Commands are the converters found, in toolOrder.
	Commands []Command
	// CacheDir holds converted PDFs and scratch directories; CacheLimit
	// bounds the PDFs kept, oldest out first.
	CacheDir   string
	CacheLimit int64
	// InputLimit bounds the bytes converted and Timeout one conversion.
	InputLimit int64
	Timeout    time.Duration
	// Run runs the prepared command; tests put a fake in its place.
	Run func(ctx context.Context, cmd *exec.Cmd) error

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Result is a converted PDF: where it is, how big, how long the
// conversion took (zero when the cache had it), by which tool.
type Result struct {
	Path      string  `json:"path"`
	Bytes     int64   `json:"bytes"`
	Seconds   float64 `json:"seconds"`
	Converter string  `json:"converter"`
	Format    string  `json:"format"`
	Cached    bool    `json:"cached"`
	Key       string  `json:"key"`
}

// Detect finds the host's converters: a path in ITEMBA_EBOOK_CONVERT,
// ITEMBA_DDJVU or ITEMBA_SOFFICE wins, then the PATH, then the usual
// Windows install directories. The cache lives under cacheDir, or the
// system temp directory when empty.
func Detect(cacheDir string) *Converter {
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "itemba-convert")
	}
	c := &Converter{CacheDir: cacheDir, CacheLimit: 512 << 20, InputLimit: 48 << 20, Timeout: 90 * time.Second, Run: func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }}
	for _, name := range toolOrder {
		if path := findTool(name); path != "" {
			c.Commands = append(c.Commands, Command{Name: name, Path: path, Formats: toolFormats[name]})
		}
	}
	return c
}

// findTool locates one tool.
func findTool(name string) string {
	if override := os.Getenv("ITEMBA_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if runtime.GOOS == "windows" {
		for _, candidate := range windowsCandidates(name) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// windowsCandidates are the install paths the tools take on Windows.
func windowsCandidates(name string) []string {
	programs := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs")}
	var out []string
	for _, root := range programs {
		if root == "" {
			continue
		}
		switch name {
		case "ebook-convert":
			out = append(out, filepath.Join(root, "Calibre2", "ebook-convert.exe"))
		case "soffice":
			out = append(out, filepath.Join(root, "LibreOffice", "program", "soffice.exe"))
		case "ddjvu":
			out = append(out, filepath.Join(root, "DjVuLibre", "ddjvu.exe"))
		}
	}
	return out
}

// NormalizeFormat reads a format name or extension as this package names
// it: lower-case, no dot.
func NormalizeFormat(format string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
}

// FormatOf is the format a file path or URL path names by its extension.
func FormatOf(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return NormalizeFormat(filepath.Ext(path))
}

// Convertible reports whether any known tool, installed or not, converts
// the format: what the reader may ask to have converted.
func Convertible(format string) bool {
	format = NormalizeFormat(format)
	for _, formats := range toolFormats {
		for _, f := range formats {
			if f == format {
				return true
			}
		}
	}
	return false
}

// Available reports whether the host has any converter.
func (c *Converter) Available() bool { return c != nil && len(c.Commands) > 0 }

// Supports names the command that takes the format on this host.
func (c *Converter) Supports(format string) (Command, bool) {
	if c == nil {
		return Command{}, false
	}
	format = NormalizeFormat(format)
	for _, cmd := range c.Commands {
		if cmd.Supports(format) {
			return cmd, true
		}
	}
	return Command{}, false
}

// Names lists the converters found.
func (c *Converter) Names() []string {
	var names []string
	if c != nil {
		for _, cmd := range c.Commands {
			names = append(names, cmd.Name)
		}
	}
	return names
}

// Formats lists the formats the host converts, sorted.
func (c *Converter) Formats() []string {
	var formats []string
	if c != nil {
		for _, cmd := range c.Commands {
			formats = append(formats, cmd.Formats...)
		}
	}
	sort.Strings(formats)
	return formats
}

// Convert turns input, a file of the format, into a PDF. The result is
// cached by the digest of the input and of the tool that converted it, so
// a second call with the same bytes returns the cached PDF without running
// anything, and another tool, or the same tool installed elsewhere,
// converts afresh.
func (c *Converter) Convert(ctx context.Context, format string, input []byte) (Result, error) {
	format = NormalizeFormat(format)
	if !Convertible(format) {
		return Result{}, ErrNotConvertible
	}
	cmd, ok := c.Supports(format)
	if !ok {
		if !c.Available() {
			return Result{}, ErrNoConverter
		}
		return Result{}, ErrUnsupported
	}
	if c.InputLimit > 0 && int64(len(input)) > c.InputLimit {
		return Result{}, ErrTooLarge
	}
	if len(input) == 0 {
		return Result{}, errors.New("convert: nothing to convert")
	}
	digest := sha256.New()
	digest.Write([]byte(format))
	digest.Write([]byte{0})
	digest.Write([]byte(cmd.Name + "\n" + cmd.Path))
	digest.Write([]byte{0})
	digest.Write(input)
	key := hex.EncodeToString(digest.Sum(nil)[:16])
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("convert: cache: %w", err)
	}
	target := filepath.Join(c.CacheDir, key+".pdf")
	lock := c.lock(key)
	lock.Lock()
	defer lock.Unlock()
	if info, err := os.Stat(target); err == nil && info.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(target, now, now)
		return Result{Path: target, Bytes: info.Size(), Converter: cmd.Name, Format: format, Cached: true, Key: key}, nil
	}
	work, err := os.MkdirTemp(c.CacheDir, "work-")
	if err != nil {
		return Result{}, fmt.Errorf("convert: scratch: %w", err)
	}
	defer os.RemoveAll(work)
	inputPath := filepath.Join(work, "input."+format)
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		return Result{}, fmt.Errorf("convert: scratch: %w", err)
	}
	args, output, env := cmd.plan(inputPath, work)
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, cmd.Path, args...)
	command.Dir = work
	command.Env = append(minimalEnv(), env...)
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = io.Discard, &stderr
	run := c.Run
	if run == nil {
		run = func(_ context.Context, cmd *exec.Cmd) error { return cmd.Run() }
	}
	started := time.Now()
	if err := run(runCtx, command); err != nil {
		return Result{}, fmt.Errorf("convert: %s failed: %w%s", cmd.Name, err, stderrTail(stderr.Bytes()))
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return Result{}, fmt.Errorf("convert: %s produced no PDF%s", cmd.Name, stderrTail(stderr.Bytes()))
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data, "\xef\xbb\xbf \r\n\t"), []byte("%PDF-")) {
		return Result{}, fmt.Errorf("convert: %s produced something that is not a PDF", cmd.Name)
	}
	staging := target + ".part"
	if err := os.WriteFile(staging, data, 0o600); err != nil {
		return Result{}, fmt.Errorf("convert: cache: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Remove(staging)
		return Result{}, fmt.Errorf("convert: cache: %w", err)
	}
	c.prune()
	return Result{Path: target, Bytes: int64(len(data)), Seconds: float64(time.Since(started).Milliseconds()) / 1000, Converter: cmd.Name, Format: format, Key: key}, nil
}

// lock is the per-key lock that keeps two callers from converting the same
// bytes at once; the second finds the cache.
func (c *Converter) lock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	lock, ok := c.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		c.locks[key] = lock
	}
	return lock
}

// prune keeps the cache under CacheLimit, oldest PDFs out first.
func (c *Converter) prune() {
	if c.CacheLimit <= 0 {
		return
	}
	entries, err := os.ReadDir(c.CacheDir)
	if err != nil {
		return
	}
	type cached struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []cached
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pdf") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, cached{filepath.Join(c.CacheDir, entry.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= c.CacheLimit {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}

// minimalEnv is the environment a converter runs with: what it needs to
// find itself and write its scratch, and no proxy or network settings.
func minimalEnv() []string {
	keep := []string{"PATH", "SystemRoot", "SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "LANG", "LC_ALL", "ComSpec", "PATHEXT", "WINDIR", "windir"}
	var env []string
	for _, name := range keep {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// stderrTail is the end of a converter's complaint, for an error.
func stderrTail(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	if len(text) > 300 {
		text = text[len(text)-300:]
	}
	return ": " + strings.Join(strings.Fields(text), " ")
}
