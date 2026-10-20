// Package formats is the format matrix: a table of what the console can
// present per path and what the checks can prove of each format, for
// video, audio, pictures and documents. Nothing may be labelled playable,
// viewable or readable outside it: the dashboard's extension sets are
// generated from it, and the scout's own tables are held to it by parity
// tests. The matrix names what the code can do; what a particular host can
// do (a converter installed, the HLS library built, a browser present) is
// answered by Availability against that host.
package formats

import (
	"sort"
	"strings"
)

// The paths a format can take through the console, and the checks and
// sandbox work it gets.
const (
	RelayPlayer = "player"
	RelayViewer = "viewer"
	RelayReader = "reader"

	ProbeMedia    = "media_probe"
	ProbeImage    = "image_decode"
	ProbeDocument = "document_parse"

	SandboxSample = "sample"
	SandboxRender = "render"

	ConverterEbook  = "ebook-convert"
	ConverterDjVu   = "ddjvu"
	ConverterOffice = "soffice"
)

// Entry is one row of the matrix: a format of a kind, the extensions and
// MIME types that name it, and what the console does with it. An empty
// Relay means the console never presents it itself and only the protected
// browser can open it; an empty Probe, Sandbox or Fingerprint means that
// check or work does not exist for it; a Converter names the host tool
// that turns it into a PDF before the reader or the sandbox sees it.
type Entry struct {
	Kind        string   `json:"kind"`
	Format      string   `json:"format"`
	Label       string   `json:"label"`
	Extensions  []string `json:"extensions"`
	MimeTypes   []string `json:"mimeTypes,omitempty"`
	Relay       string   `json:"relay,omitempty"`
	Probe       string   `json:"probe,omitempty"`
	Sandbox     string   `json:"sandbox,omitempty"`
	Converter   string   `json:"converter,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// matrix is the table itself, in the order the dashboard shows it.
var matrix = []Entry{
	// Video.
	{Kind: "video", Format: "mp4", Label: "MP4", Extensions: []string{"mp4", "m4v", "mov"}, MimeTypes: []string{"video/mp4", "video/quicktime", "application/mp4"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "video-frames-v1", Note: "The browser's own decoder; codecs the browser lacks stay unplayable."},
	{Kind: "video", Format: "webm", Label: "WebM", Extensions: []string{"webm"}, MimeTypes: []string{"video/webm"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "video-frames-v1"},
	{Kind: "video", Format: "mkv", Label: "Matroska", Extensions: []string{"mkv"}, MimeTypes: []string{"video/x-matroska"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "video-frames-v1", Note: "Plays when the codecs inside are ones the browser decodes."},
	{Kind: "video", Format: "ogv", Label: "Ogg video", Extensions: []string{"ogv"}, MimeTypes: []string{"video/ogg"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "video-frames-v1"},
	{Kind: "video", Format: "hls", Label: "HLS", Extensions: []string{"m3u8"}, MimeTypes: []string{"application/vnd.apple.mpegurl", "application/x-mpegurl"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "video-frames-v1", Note: "Through hls.js, the lowest rendition; the relay rewrites the playlist onto itself."},
	{Kind: "video", Format: "ts", Label: "MPEG transport stream", Extensions: []string{"ts"}, MimeTypes: []string{"video/mp2t"}, Note: "A segment of an HLS stream; plays only inside its playlist."},
	{Kind: "video", Format: "dash", Label: "DASH", Extensions: []string{"mpd"}, MimeTypes: []string{"application/dash+xml"}, Note: "Reported as unsupported rather than attempted."},
	// Audio.
	{Kind: "audio", Format: "mp3", Label: "MP3", Extensions: []string{"mp3"}, MimeTypes: []string{"audio/mpeg"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1"},
	{Kind: "audio", Format: "flac", Label: "FLAC", Extensions: []string{"flac"}, MimeTypes: []string{"audio/flac", "audio/x-flac"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1"},
	{Kind: "audio", Format: "ogg", Label: "Ogg audio", Extensions: []string{"ogg", "oga"}, MimeTypes: []string{"audio/ogg"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1"},
	{Kind: "audio", Format: "opus", Label: "Opus", Extensions: []string{"opus"}, MimeTypes: []string{"audio/opus"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1"},
	{Kind: "audio", Format: "aac", Label: "AAC in MP4", Extensions: []string{"m4a"}, MimeTypes: []string{"audio/mp4", "audio/aac"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1", Note: "A trailing index decodes only in the player, not offline, so the fingerprint may be missing."},
	{Kind: "audio", Format: "wav", Label: "WAVE", Extensions: []string{"wav"}, MimeTypes: []string{"audio/wav", "audio/x-wav", "audio/wave"}, Relay: RelayPlayer, Probe: ProbeMedia, Sandbox: SandboxSample, Fingerprint: "acoustic-bands-v1"},
	{Kind: "audio", Format: "weba", Label: "WebM audio", Extensions: []string{"weba"}, MimeTypes: []string{"audio/webm"}, Relay: RelayPlayer, Note: "The player decodes it; the audio probe and sample take the Archive's containers only."},
	// Pictures.
	{Kind: "image", Format: "jpeg", Label: "JPEG", Extensions: []string{"jpg", "jpeg"}, MimeTypes: []string{"image/jpeg"}, Relay: RelayViewer, Probe: ProbeImage, Sandbox: SandboxRender, Fingerprint: "image-dhash-v1"},
	{Kind: "image", Format: "png", Label: "PNG", Extensions: []string{"png"}, MimeTypes: []string{"image/png"}, Relay: RelayViewer, Probe: ProbeImage, Sandbox: SandboxRender, Fingerprint: "image-dhash-v1"},
	{Kind: "image", Format: "gif", Label: "GIF", Extensions: []string{"gif"}, MimeTypes: []string{"image/gif"}, Relay: RelayViewer, Probe: ProbeImage, Sandbox: SandboxRender, Fingerprint: "image-dhash-v1"},
	{Kind: "image", Format: "webp", Label: "WebP", Extensions: []string{"webp"}, MimeTypes: []string{"image/webp"}, Relay: RelayViewer, Sandbox: SandboxRender, Note: "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."},
	{Kind: "image", Format: "avif", Label: "AVIF", Extensions: []string{"avif"}, MimeTypes: []string{"image/avif"}, Relay: RelayViewer, Sandbox: SandboxRender, Note: "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."},
	{Kind: "image", Format: "bmp", Label: "BMP", Extensions: []string{"bmp"}, MimeTypes: []string{"image/bmp"}, Relay: RelayViewer, Sandbox: SandboxRender, Note: "The probe cannot decode it and says unsupported; the viewer and the sandbox show it."},
	{Kind: "image", Format: "tiff", Label: "TIFF", Extensions: []string{"tif", "tiff"}, MimeTypes: []string{"image/tiff"}, Relay: RelayViewer, Note: "The probe cannot decode it; browsers seldom show it either."},
	{Kind: "image", Format: "svg", Label: "SVG", Extensions: []string{"svg"}, MimeTypes: []string{"image/svg+xml"}, Note: "May carry script; never relayed to the viewer or the sandbox."},
	// Documents.
	{Kind: "document", Format: "pdf", Label: "PDF", Extensions: []string{"pdf"}, MimeTypes: []string{"application/pdf"}, Relay: RelayReader, Probe: ProbeDocument, Sandbox: SandboxRender, Fingerprint: "text-simhash-v2"},
	{Kind: "document", Format: "txt", Label: "Plain text", Extensions: []string{"txt"}, MimeTypes: []string{"text/plain"}, Probe: ProbeDocument, Fingerprint: "text-simhash-v2", Note: "Read and fingerprinted; opens in the protected browser, not the reader."},
	{Kind: "document", Format: "epub", Label: "EPUB", Extensions: []string{"epub"}, MimeTypes: []string{"application/epub+zip"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterEbook, Note: "Converted to PDF first, by Calibre's ebook-convert."},
	{Kind: "document", Format: "mobi", Label: "Kindle (MOBI, AZW)", Extensions: []string{"mobi", "azw", "azw3"}, MimeTypes: []string{"application/x-mobipocket-ebook"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterEbook, Note: "Converted to PDF first, by Calibre's ebook-convert."},
	{Kind: "document", Format: "fb2", Label: "FictionBook", Extensions: []string{"fb2"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterEbook, Note: "Converted to PDF first, by Calibre's ebook-convert."},
	{Kind: "document", Format: "lit", Label: "Microsoft Reader", Extensions: []string{"lit"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterEbook, Note: "Converted to PDF first, by Calibre's ebook-convert."},
	{Kind: "document", Format: "djvu", Label: "DjVu", Extensions: []string{"djvu"}, MimeTypes: []string{"image/vnd.djvu"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterDjVu, Note: "Converted to PDF first, by DjVuLibre's ddjvu."},
	{Kind: "document", Format: "word", Label: "Word, OpenDocument text, RTF", Extensions: []string{"doc", "docx", "odt", "rtf"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterOffice, Note: "Converted to PDF first, by LibreOffice."},
	{Kind: "document", Format: "slides", Label: "PowerPoint, OpenDocument presentation", Extensions: []string{"ppt", "pptx", "odp"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterOffice, Note: "Converted to PDF first, by LibreOffice."},
	{Kind: "document", Format: "sheet", Label: "Excel, OpenDocument spreadsheet", Extensions: []string{"xls", "xlsx", "ods"}, Relay: RelayReader, Sandbox: SandboxRender, Converter: ConverterOffice, Note: "Converted to PDF first, by LibreOffice."},
	{Kind: "document", Format: "html", Label: "HTML", Extensions: []string{"htm", "html"}, MimeTypes: []string{"text/html"}, Note: "A page, not a document: the protected browser opens it."},
}

// All is the matrix, as a copy.
func All() []Entry {
	out := make([]Entry, len(matrix))
	copy(out, matrix)
	return out
}

// ByExtension finds the entry an extension names, with or without its dot
// and in any case.
func ByExtension(ext string) (Entry, bool) {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	for _, entry := range matrix {
		for _, e := range entry.Extensions {
			if e == ext {
				return entry, true
			}
		}
	}
	return Entry{}, false
}

// ByFormat finds the entry of a kind and format name.
func ByFormat(kind, format string) (Entry, bool) {
	for _, entry := range matrix {
		if entry.Kind == kind && entry.Format == strings.ToLower(strings.TrimSpace(format)) {
			return entry, true
		}
	}
	return Entry{}, false
}

// Extensions lists the extensions of a kind that take a path, sorted:
// every extension of the kind when relay is empty, else only those the
// console presents through that relay path.
func Extensions(kind, relay string) []string {
	var out []string
	for _, entry := range matrix {
		if entry.Kind != kind || relay != "" && entry.Relay != relay {
			continue
		}
		out = append(out, entry.Extensions...)
	}
	sort.Strings(out)
	return out
}

// Presentable reports whether the console presents a file of that
// extension itself: through the player, the viewer or the reader. Nothing
// else may be labelled playable, viewable or readable.
func Presentable(ext string) bool {
	entry, ok := ByExtension(ext)
	return ok && entry.Relay != ""
}

// Host is what a particular host has: the converters found, whether the
// HLS library was built into the console, and whether a browser exists
// for the sandbox.
type Host struct {
	Converters []string `json:"converters"`
	HLS        bool     `json:"hls"`
	Browser    bool     `json:"browser"`
}

// Available is an entry judged against a host: whether its relay path
// works here, whether its sandbox work can run here, and why not when not.
type Available struct {
	Entry
	RelayAvailable   bool   `json:"relayAvailable"`
	SandboxAvailable bool   `json:"sandboxAvailable"`
	Because          string `json:"because,omitempty"`
}

// Availability judges every entry against the host.
func Availability(host Host) []Available {
	hasConverter := func(name string) bool {
		for _, c := range host.Converters {
			if c == name {
				return true
			}
		}
		return false
	}
	out := make([]Available, 0, len(matrix))
	for _, entry := range matrix {
		a := Available{Entry: entry, RelayAvailable: entry.Relay != "", SandboxAvailable: entry.Sandbox != ""}
		var because []string
		if entry.Converter != "" && !hasConverter(entry.Converter) {
			a.RelayAvailable, a.SandboxAvailable = false, false
			because = append(because, "needs "+entry.Converter+", which this host does not have")
		}
		if entry.Format == "hls" && !host.HLS {
			a.RelayAvailable, a.SandboxAvailable = false, false
			because = append(because, "the HLS library was not built into this console")
		}
		if entry.Sandbox != "" && !host.Browser {
			a.SandboxAvailable = false
			because = append(because, "no browser on this host for the sandbox")
		}
		a.Because = strings.Join(because, "; ")
		out = append(out, a)
	}
	return out
}

// Kinds lists the kinds in the matrix, in order.
func Kinds() []string {
	var out []string
	seen := map[string]bool{}
	for _, entry := range matrix {
		if !seen[entry.Kind] {
			seen[entry.Kind] = true
			out = append(out, entry.Kind)
		}
	}
	return out
}
