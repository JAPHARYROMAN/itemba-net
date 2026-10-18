package scout

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	availabilityCountryEndpoint = "https://www.cloudflare.com/cdn-cgi/trace"
	availabilityCountryTTL      = time.Minute
	availabilityCountryTimeout  = 2 * time.Second
	availabilityCountryMaxBody  = 8 * 1024
	availabilityCountryUnknown  = "Server egress country is unconfirmed; the location check did not return a valid country."
)

func countryObservationDetail(country string) string {
	return "Server egress observed in " + country + "; this may differ from the user's location."
}

// availabilityCheckCountry observes the server's network route, never the
// user's location. Its trace response is reduced to one validated country code;
// IP addresses and other trace fields are not cached, returned, or logged.
func (s *Scout) availabilityCheckCountry(ctx context.Context) (country string, detail string) {
	if ctx.Err() != nil {
		return "", availabilityCountryUnknown
	}
	s.countryMu.Lock()
	if cached := movieCountry(s.country); cached != "" && !s.countryAt.IsZero() && time.Since(s.countryAt) >= 0 && time.Since(s.countryAt) < availabilityCountryTTL {
		s.countryMu.Unlock()
		return cached, countryObservationDetail(cached)
	}
	s.countryMu.Unlock()

	baseClient := s.availabilityHTTPClient
	if baseClient == nil {
		baseClient = newMovieAvailabilityClient()
	}
	client := *baseClient
	if client.Timeout <= 0 || client.Timeout > availabilityCountryTimeout {
		client.Timeout = availabilityCountryTimeout
	}
	baseRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil || !strings.EqualFold(req.URL.Hostname(), "www.cloudflare.com") || req.URL.Port() != "" && req.URL.Port() != "443" {
			return fmt.Errorf("country check redirect rejected")
		}
		if baseRedirect != nil {
			return baseRedirect(req, via)
		}
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, availabilityCountryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, availabilityCountryEndpoint, nil)
	if err != nil {
		return "", availabilityCountryUnknown
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		// Network errors can contain addresses, so expose no raw error text.
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return "", availabilityCountryUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", availabilityCountryUnknown
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "text/plain") {
		return "", availabilityCountryUnknown
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, availabilityCountryMaxBody+1))
	if err != nil || len(body) > availabilityCountryMaxBody || !utf8.Valid(body) {
		return "", availabilityCountryUnknown
	}
	country = traceCountry(body)
	if country == "" || checkCtx.Err() != nil {
		return "", availabilityCountryUnknown
	}
	s.countryMu.Lock()
	s.country, s.countryAt = country, time.Now()
	s.countryMu.Unlock()
	return country, countryObservationDetail(country)
}

func traceCountry(body []byte) string {
	var country string
	seen := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "loc=") {
			continue
		}
		if seen {
			return ""
		}
		seen = true
		country = movieCountry(strings.TrimSpace(strings.TrimPrefix(line, "loc=")))
	}
	return country
}
