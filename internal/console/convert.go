package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/convert"
)

// The conversion route turns an e-book or office file into the PDF the
// reader frames: the console fetches the file through Shield within a byte
// budget, runs a converter the host has (internal/convert), keeps the PDF
// in a bounded cache by the file's digest, and serves it as
// application/pdf with ranges, which is what the reader checks for before
// framing. GET /api/convert?url=<file> serves the PDF; with info=1 it
// converts (or finds the cached PDF) and answers JSON saying how it went,
// so the reader can show the conversion before framing. A host with no
// converter says so in that JSON; nothing is installed.

// hostConverter finds the host's document converters once; tests put a
// converter of their own in its place.
var hostConverter = sync.OnceValue(func() *convert.Converter { return convert.Detect("") })

const (
	// convertFetchLimit bounds the bytes fetched for a conversion and
	// convertDeadline one conversion request, fetch included.
	convertFetchLimit = 48 << 20
	convertDeadline   = 150 * time.Second
)

// convertStatus is the JSON a conversion request answers with info=1, and
// the error shape of a refused one.
type convertStatus struct {
	URL        string   `json:"url"`
	Format     string   `json:"format"`
	Supported  bool     `json:"supported"`
	Converter  string   `json:"converter,omitempty"`
	Converters []string `json:"converters"`
	Formats    []string `json:"formats"`
	Converted  bool     `json:"converted"`
	Cached     bool     `json:"cached,omitempty"`
	Seconds    float64  `json:"seconds,omitempty"`
	Bytes      int64    `json:"bytes,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// noConverterMessage says why the host cannot convert the format.
func noConverterMessage(c *convert.Converter, format string) string {
	upper := strings.ToUpper(format)
	if !c.Available() {
		return "No converter on this host turns " + upper + " into a PDF. Calibre's ebook-convert converts e-books, DjVuLibre's ddjvu converts DjVu and LibreOffice's soffice converts office files; none is installed."
	}
	return "No converter on this host takes " + upper + "; this host has " + strings.Join(c.Names(), ", ") + "."
}

func (s *Server) convertRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Conversion is requested with GET."})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("url"))
	info := r.URL.Query().Get("info") == "1"
	target, err := url.Parse(raw)
	if raw == "" || err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "A document URL is required."})
		return
	}
	if s.shield != nil {
		if match := s.shield.Engine().Inspect(raw, "Document"); match.Blocked {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "The document address is blocked by itemba-shield."})
			return
		}
	}
	converter := hostConverter()
	format := convert.FormatOf(target.Path)
	status := convertStatus{URL: raw, Format: format, Converters: converter.Names(), Formats: converter.Formats()}
	if status.Converters == nil {
		status.Converters = []string{}
	}
	if status.Formats == nil {
		status.Formats = []string{}
	}
	if !convert.Convertible(format) {
		status.Error = "The reader converts e-books (EPUB, MOBI, AZW3, FB2), DjVu and office files to PDF; this is not one of them."
		writeJSON(w, http.StatusBadRequest, status)
		return
	}
	cmd, supported := converter.Supports(format)
	status.Supported, status.Converter = supported, cmd.Name
	refuse := func(code int, message string) {
		status.Error = message
		if info {
			code = http.StatusOK
		}
		writeJSON(w, code, status)
	}
	if !supported {
		refuse(http.StatusServiceUnavailable, noConverterMessage(converter, format))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), convertDeadline)
	defer cancel()
	data, err := s.fetchForConversion(ctx, raw)
	if err != nil {
		refuse(http.StatusBadGateway, "The document could not be fetched for conversion: "+err.Error()+".")
		return
	}
	result, err := converter.Convert(ctx, format, data)
	if err != nil {
		refuse(http.StatusBadGateway, "The document could not be converted: "+strings.TrimPrefix(err.Error(), "convert: ")+".")
		return
	}
	status.Converted, status.Cached, status.Seconds, status.Bytes = true, result.Cached, result.Seconds, result.Bytes
	if info {
		writeJSON(w, http.StatusOK, status)
		return
	}
	file, err := os.Open(result.Path)
	if err != nil {
		refuse(http.StatusBadGateway, "The converted PDF could not be read.")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		refuse(http.StatusBadGateway, "The converted PDF could not be read.")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline; filename=\"converted.pdf\"")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, "converted.pdf", stat.ModTime(), file)
}

// fetchForConversion fetches the file to convert through Shield, following
// public redirects it inspects too, within the conversion limit.
func (s *Server) fetchForConversion(ctx context.Context, raw string) ([]byte, error) {
	client := &http.Client{Timeout: convertDeadline, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("a redirect left the web")
		}
		if s.shield != nil {
			if match := s.shield.Engine().Inspect(req.URL.String(), "Document"); match.Blocked {
				return errors.New("a redirect was blocked by itemba-shield")
			}
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("the address could not be requested")
	}
	request.Header.Set("User-Agent", "ITEMBA-NET/2 (+https://github.com/JAPHARYROMAN/itemba-net; personal research crawler; document conversion)")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("the host did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the host answered HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, convertFetchLimit+1))
	if err != nil {
		return nil, errors.New("the file could not be read")
	}
	if int64(len(data)) > convertFetchLimit {
		return nil, errors.New("the file exceeds the conversion limit")
	}
	return data, nil
}
