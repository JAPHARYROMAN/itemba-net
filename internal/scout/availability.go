package scout

import (
	"context"
	"errors"
	"fmt"
	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const availabilityPageLimit = 512 << 10

// newMovieAvailabilityClient makes a direct, public-Internet-only connection.
// Each DNS answer is checked and the selected address is pinned to the dial;
// validation followed by a second hostname lookup would permit DNS rebinding.
// Environment proxies are deliberately unused because their egress may differ
// from the machine whose check country is displayed to the user.
func newMovieAvailabilityClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialMovieAvailability
	transport.DialTLSContext = nil
	transport.TLSHandshakeTimeout = 3 * time.Second
	transport.ResponseHeaderTimeout = 4 * time.Second
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 3
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

var availabilityBlockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func availabilityPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return false
	}
	// Only the currently allocated global IPv6 unicast space is considered a
	// public destination; this also excludes local NAT64/translation prefixes.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, blocked := range availabilityBlockedNetworks {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}

func safeAvailabilityURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return nil, false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if !availabilityPublicIP(ip) {
			return nil, false
		}
	} else {
		if !strings.Contains(host, ".") || strings.ContainsAny(host, "\\% \t\r\n") || availabilityNumericHost.MatchString(host) {
			return nil, false
		}
		for _, suffix := range []string{".localhost", ".local", ".internal", ".localdomain", ".lan", ".home", ".test", ".invalid", ".example", ".onion"} {
			if strings.HasSuffix(host, suffix) {
				return nil, false
			}
		}
	}
	u.Fragment = ""
	return u, true
}

func dialMovieAvailability(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "80" && port != "443") {
		return nil, errors.New("availability check requires a public HTTP destination")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("availability destination could not be resolved")
	}
	// Reject mixed public/private DNS answers as well as private-only ones.
	for _, ip := range addresses {
		if !availabilityPublicIP(ip) {
			return nil, errors.New("availability destination is not on the public Internet")
		}
	}
	dialer := net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range addresses {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("availability destination could not be reached")
}

// checkMoviePageAvailability establishes page access only. It never executes
// scripts, signs in, purchases an offer, or claims successful movie playback.
// A supplied custom transport is useful for deterministic fixture tests;
// production callers use newMovieAvailabilityClient's guarded transport.
func checkMoviePageAvailability(ctx context.Context, client *http.Client, candidate CandidateSource, requestedRegion, checkCountry string) AvailabilityCheck {
	result := AvailabilityCheck{
		Status: "unconfirmed", Country: movieCountry(checkCountry), RequestedRegion: strings.ToUpper(strings.TrimSpace(requestedRegion)),
		CheckedAt: time.Now().UTC().Format(time.RFC3339), Method: "http_page", PlaybackConfirmed: false,
		Detail: "Movie-page access and playback are unconfirmed.",
	}
	initial, ok := safeAvailabilityURL(candidate.URL)
	if !ok {
		result.Detail = "Automatic checks require a public HTTP or HTTPS movie page on a standard web port."
		return result
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	guarded := *client
	if guarded.Transport == nil {
		guarded.Transport = newMovieAvailabilityClient().Transport
	}
	if guarded.Timeout <= 0 || guarded.Timeout > 5*time.Second {
		guarded.Timeout = 5 * time.Second
	}
	guarded.Jar = nil
	priorRedirect := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("availability redirect limit reached")
		}
		if _, safe := safeAvailabilityURL(req.URL.String()); !safe {
			return errors.New("availability redirect is not a public movie page")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, initial.String(), nil)
	if err != nil {
		return result
	}
	request.Header.Set("User-Agent", "ITEMBA-NET/0.1 (movie page availability check)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	request.Header.Set("Accept-Language", "en,sw;q=0.8")
	response, err := guarded.Do(request)
	if err != nil {
		result.Status, result.Detail = "error", "The movie page could not be checked within the connection and redirect limits."
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	if response.Request != nil && response.Request.URL != nil {
		if finalURL, safe := safeAvailabilityURL(response.Request.URL.String()); safe {
			result.EvidenceURL = finalURL.String()
		}
	}
	if result.EvidenceURL == "" {
		result.EvidenceURL = initial.String()
	}
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		result.Status, result.Detail = "not_found", "The movie link returned HTTP 404 or 410."
		return result
	case http.StatusUnavailableForLegalReasons:
		result.Status, result.Detail = "region_blocked", "The movie link returned HTTP 451, a legal or location restriction. Playback is unconfirmed."
		return result
	case http.StatusUnauthorized:
		result.Status, result.Detail = "sign_in_required", "The site requires authentication before this page can be checked."
		return result
	case http.StatusForbidden, http.StatusTooManyRequests:
		result.Status, result.Detail = "access_restricted", "The site restricted the automated check. This does not establish a country block."
		return result
	}
	if response.StatusCode >= 500 {
		result.Status, result.Detail = "error", fmt.Sprintf("The site returned HTTP %d during the check.", response.StatusCode)
		return result
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Detail = "The site did not return a usable movie page."
		return result
	}
	final, _ := url.Parse(result.EvidenceURL)
	if final != nil && final.String() != initial.String() && availabilityLoginPath(final.Path) {
		result.Status, result.Detail = "sign_in_required", "The movie link redirected to a sign-in page; access after sign-in is unconfirmed."
		return result
	}
	if strings.EqualFold(response.Header.Get("cf-mitigated"), "challenge") {
		result.Detail = "The site presented an automated-access challenge; movie access is unconfirmed."
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, availabilityPageLimit+1))
	if err != nil {
		result.Status, result.Detail = "error", "The movie-page response could not be read."
		return result
	}
	if len(body) > availabilityPageLimit {
		result.Detail = "The page exceeded the bounded check size; movie access is unconfirmed."
		return result
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = strings.ToLower(http.DetectContentType(body))
	}
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		result.Detail = "The destination did not return a movie HTML page; playback is unconfirmed."
		return result
	}
	page := parseAvailabilityPage(string(body))
	if page.challenge {
		result.Detail = "The site presented an automated-access challenge; movie access is unconfirmed."
		return result
	}
	if page.signIn {
		result.Status, result.Detail = "sign_in_required", "The site requires sign-in before movie access can be checked."
		return result
	}
	if availabilitySoftNotFound(candidate, page) {
		result.Status, result.Detail = "not_found", "The site returned a page-not-found heading instead of the requested movie, despite its HTTP success response."
		return result
	}
	host := initial.Hostname()
	if final != nil {
		host = final.Hostname()
	}
	if !availabilityMovieMatches(candidate, page, host) {
		result.Detail = "The returned page did not identify the requested movie; access is unconfirmed."
		return result
	}
	for _, block := range page.blocks {
		if availabilityRegionBlock(block, result.Country) {
			result.Status, result.Detail = "region_blocked", "The movie page explicitly reports a country or location restriction for this connection. Playback is unconfirmed."
			return result
		}
	}
	if len([]rune(strings.Join(page.blocks, " "))) < 80 {
		result.Detail = "Only a short page shell was returned; movie access and playback are unconfirmed."
		return result
	}
	result.Status, result.Detail = "page_reachable", "The matching movie page responded successfully. Movie licensing, sign-in, payment, and playback remain unconfirmed."
	return result
}

var availabilityLoginRoute = regexp.MustCompile(`(?i)(?:^|/)(?:login|log-in|signin|sign-in|sign_in|signon|sign-on)(?:/|$)`)
var availabilityNumericHost = regexp.MustCompile(`(?i)^(?:0x[0-9a-f]+|[0-9]+)(?:\.(?:0x[0-9a-f]+|[0-9]+))*$`)

func availabilityLoginPath(path string) bool { return availabilityLoginRoute.MatchString(path) }

type availabilityPage struct {
	title     string
	headings  []string
	blocks    []string
	challenge bool
	signIn    bool
}

// availabilityIgnoredNode and availabilityNodeText are the one judgment of
// what a visitor reads on a page, shared with the drivers' checks.
func availabilityIgnoredNode(node *html.Node) bool { return crawler.IgnoredNode(node) }

func availabilityNodeText(node *html.Node) string { return crawler.VisibleText(node) }

func availabilityBlockElement(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}
	switch node.Data {
	case "body", "main", "article", "section", "div", "p", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "form", "table", "tr", "td", "th", "figure", "figcaption", "blockquote":
		return true
	}
	return false
}

func parseAvailabilityPage(raw string) availabilityPage {
	page := availabilityPage{}
	document, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return page
	}
	var body, main *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if availabilityIgnoredNode(n) {
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				page.title = availabilityNodeText(n)
			case "body":
				body = n
			case "main":
				if main == nil {
					main = n
				}
			}
			for _, attr := range n.Attr {
				if main == nil && strings.EqualFold(attr.Key, "role") && strings.EqualFold(attr.Val, "main") {
					main = n
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	if main == nil {
		main = body
	}
	if main != nil {
		var inspectMain func(*html.Node)
		inspectMain = func(n *html.Node) {
			if availabilityIgnoredNode(n) {
				return
			}
			if n.Type == html.ElementNode && n.Data == "h1" {
				page.headings = append(page.headings, availabilityNodeText(n))
			}
			if n.Type == html.ElementNode && n.Data == "input" {
				for _, attr := range n.Attr {
					if strings.EqualFold(attr.Key, "type") && strings.EqualFold(attr.Val, "password") {
						page.signIn = true
					}
				}
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				inspectMain(child)
			}
		}
		inspectMain(main)
		var blocks func(*html.Node)
		blocks = func(n *html.Node) {
			var inline strings.Builder
			flush := func() {
				if value := strings.Join(strings.Fields(inline.String()), " "); value != "" {
					page.blocks = append(page.blocks, value)
				}
				inline.Reset()
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if availabilityIgnoredNode(child) {
					continue
				}
				if availabilityBlockElement(child) {
					flush()
					blocks(child)
				} else {
					inline.WriteString(availabilityNodeText(child))
					inline.WriteByte(' ')
				}
			}
			flush()
		}
		blocks(main)
	}
	visible := strings.ToLower(strings.Join(page.blocks, " "))
	for _, phrase := range []string{"verify you are human", "verify that you are human", "checking your browser", "enable javascript and cookies to continue", "performing security verification", "checking if the site connection is secure", "complete the captcha", "unusual traffic from your computer"} {
		if strings.Contains(visible, phrase) {
			page.challenge = true
		}
	}
	if strings.EqualFold(strings.TrimSpace(page.title), "just a moment...") || strings.EqualFold(strings.TrimSpace(page.title), "access denied") {
		page.challenge = true
	}
	page.signIn = page.signIn && (strings.Contains(visible, "sign in") || strings.Contains(visible, "log in") || strings.Contains(visible, "login"))
	return page
}

func availabilityMovieMatches(candidate CandidateSource, page availabilityPage, host string) bool {
	title, year := movieTitleDetails(candidate)
	if strings.TrimSpace(title) == "" || hasUnrequestedDerivative(title, page.title) {
		return false
	}
	query := title
	if year != 0 {
		query += fmt.Sprintf(" (%d)", year)
	}
	identities := append([]string{page.title}, page.headings...)
	matched := false
	headingMatched := false
	for i, identity := range identities {
		normalized := normalizeWebMovieTitle(identity, host)
		if candidate.Kind == lens.TypeImage {
			// A file page names its picture as a file, "File:Name.jpg";
			// the name is what the listing carries.
			normalized = pictureFileName(normalized)
		}
		pageTitle, pageYear := movieTitleDetails(CandidateSource{Title: normalized})
		if strings.EqualFold(strings.Join(titleWords(pageTitle), " "), strings.Join(titleWords(title), " ")) && year != 0 && pageYear != 0 && pageYear != year {
			return false
		}
		if relevanceScore(query, CandidateSource{Title: normalized}) > 0 {
			matched = true
			headingMatched = headingMatched || i > 0
		}
	}
	// A matching browser title cannot override a primary heading for a
	// different work after a redirect or an incorrectly indexed page.
	return matched && (len(page.headings) == 0 || headingMatched)
}

var pictureExtension = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|tiff?|webp|svg|bmp|avif)$`)

// pictureFileName reads a picture's name out of a file page's heading: the
// "File:" prefix and the extension go.
func pictureFileName(title string) string {
	title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "File:"))
	return strings.TrimSpace(pictureExtension.ReplaceAllString(title, ""))
}

func availabilitySoftNotFound(candidate CandidateSource, page availabilityPage) bool {
	title, _ := movieTitleDetails(candidate)
	expected := strings.Join(titleWords(title), " ")
	for _, heading := range page.headings {
		label := strings.Join(titleWords(heading), " ")
		if label == expected {
			// A film can itself be named "Page Not Found" or "404".
			return false
		}
		switch label {
		case "404", "404 not found", "404 page not found", "page not found", "not found", "movie not found", "video not found", "title not found", "content not found", "this page does not exist", "this page no longer exists":
			return true
		}
	}
	return false
}

func availabilityRegionBlock(block, checkCountry string) bool {
	text := strings.ToLower(strings.Join(strings.Fields(block), " "))
	if len([]rune(text)) > 350 || strings.Contains(text, "?") {
		return false
	}
	for _, exception := range []string{"trailer", "teaser", "preview", " clip", "may be", "might be", "some titles", "certain titles", "if this", "for example", "such as", "frequently asked"} {
		if strings.Contains(text, exception) {
			return false
		}
	}
	for _, phrase := range []string{"not available in your country", "not available in your region", "not available in your location", "unavailable in your country", "unavailable in your region", "unavailable in your location", "not available for viewing in your location", "not available to watch in your location", "not supported in your country", "not supported in your region", "not currently available in your country", "not currently available in your region"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	names := map[string][]string{"TZ": {"tanzania"}, "KE": {"kenya"}, "US": {"the united states", "united states", "the us"}, "GB": {"the united kingdom", "united kingdom", "the uk"}, "ZA": {"south africa"}, "IN": {"india"}, "CA": {"canada"}, "AU": {"australia"}, "DE": {"germany"}, "FR": {"france"}}
	for _, name := range names[checkCountry] {
		if strings.Contains(text, "not available in "+name) || strings.Contains(text, "unavailable in "+name) {
			return true
		}
	}
	return false
}
