package scout

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Pluto's first-party movie page currently redirects this connection to
// https://pluto.tv/intl/, whose primary heading says the service is unavailable
// in the current location (observed 2026-10-18). No anonymous bootstrap tokens,
// regional overrides, private player APIs or unobserved stream URLs are used.
// An accessible movie page still requires a separate protected native check;
// HTTP page reachability never establishes movie playback or ad-free media.
func plutoMoviePlaybackTarget(source CandidateSource) bool {
	if source.AccessType != "free" || source.Price != "" {
		return false
	}
	page, safe := safeAvailabilityURL(source.URL)
	return safe && plutoMoviePageURL(page)
}

// inspectPlutoPlayback supplies an honest preflight result for the common
// playback pipeline. No provider asset attribution contract was available on
// the observed public page, so a reachable page must remain content_unconfirmed
// rather than claiming that a trailer, advertisement or movie actually played.
func inspectPlutoPlayback(ctx context.Context, client *http.Client, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	if !plutoMoviePlaybackTarget(source) {
		return source, nil
	}
	pageCheck := checkPlutoPlaybackPage(ctx, client, source, "ALL", "")
	if pageCheck == nil {
		return source, nil
	}
	check := &PlaybackCheck{
		Status: "unconfirmed", Reason: "content_unconfirmed", CheckedAt: pageCheck.CheckedAt,
		Method: "provider_playback_preflight", EvidenceURL: pageCheck.EvidenceURL,
		Detail: "Pluto TV's page does not provide enough verified main-movie attribution for a native playback sample. Movie playback and in-stream advertisements remain unconfirmed.",
	}
	switch pageCheck.Status {
	case "region_blocked":
		check.Status, check.Reason, check.Detail = "not_started", "region_unavailable", pageCheck.Detail
	case "sign_in_required":
		check.Status, check.Reason, check.Detail = "not_started", "sign_in_required", pageCheck.Detail
	case "page_reachable":
		// Preserve the distinction between a reachable movie page and native
		// full-feature playback. In-band ads cannot be certified by URL rules.
	default:
		check.Detail = pageCheck.Detail
	}
	return source, check
}

func plutoMoviePageURL(page *url.URL) bool {
	if page == nil || page.Scheme != "https" || !movieServiceHost(strings.ToLower(page.Hostname()), "pluto.tv") || page.User != nil {
		return false
	}
	parts := strings.FieldsFunc(strings.ToLower(page.Path), func(r rune) bool { return r == '/' })
	for _, route := range [][]string{{"on-demand", "movies"}, {"watch", "on-demand", "movies"}} {
		tail := movieProviderRoute(parts, route...)
		if len(tail) == 1 || len(tail) == 2 && tail[1] == "details" {
			return tail[0] != "" && len(tail[0]) <= 200
		}
	}
	return false
}

// checkPlutoPlaybackPage preserves a provider's explicit connection-level
// location block even when a generic /intl/ page cannot identify the requested
// movie. This is a provider response observation, never a successful sample.
// Callers may proceed to a protected native page check for an unconfirmed
// JavaScript shell, but must stop at an explicit block or authentication wall.
func checkPlutoPlaybackPage(ctx context.Context, client *http.Client, source CandidateSource, region, country string) *AvailabilityCheck {
	if !plutoMoviePlaybackTarget(source) {
		return nil
	}
	check := &AvailabilityCheck{
		Status: "unconfirmed", Country: movieCountry(country), RequestedRegion: strings.ToUpper(strings.TrimSpace(region)),
		CheckedAt: time.Now().UTC().Format(time.RFC3339), Method: "pluto_public_page", EvidenceURL: source.URL,
		Detail: "Pluto TV's primary movie page has not established playback. A protected native player check is required.",
	}
	checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	body, final, err := fetchFreeSourceDocument(checkCtx, client, source.URL)
	if err != nil || final == nil {
		check.Detail = "Pluto TV's public movie page could not be checked within the guarded request limits. Playback remains unconfirmed."
		return check
	}
	if !movieServiceHost(strings.ToLower(final.Hostname()), "pluto.tv") {
		check.Detail = "The Pluto TV link redirected away from its consumer site. Playback remains unconfirmed."
		return check
	}
	check.EvidenceURL, check.HTTPStatus = final.String(), http.StatusOK
	if plutoLocationBlockedPage(body, final) {
		check.Status = "region_blocked"
		check.Detail = "Pluto TV explicitly reports that its service is not available from this connection. No movie playback was confirmed."
		return check
	}
	parsed := parseAvailabilityPage(string(body))
	if parsed.challenge {
		check.Detail = "Pluto TV presented an automated-access challenge; playback remains unconfirmed."
		return check
	}
	if parsed.signIn || availabilityLoginPath(final.Path) {
		check.Status = "sign_in_required"
		check.Detail = "Pluto TV requires sign-in before the requested player can be checked. No account state was used."
		return check
	}
	if !plutoMoviePageURL(final) || availabilitySoftNotFound(source, parsed) || !availabilityMovieMatches(source, parsed, final.Hostname()) {
		check.Detail = "The returned Pluto TV page did not establish the requested movie's identity. A protected native check must verify the movie before confirming playback."
		return check
	}
	check.Status = "page_reachable"
	check.Detail = "The matching Pluto TV movie page responded. Movie playback, regional licensing and in-stream advertisements remain unconfirmed."
	return check
}

func plutoLocationBlockedPage(body []byte, final *url.URL) bool {
	if final == nil || !movieServiceHost(strings.ToLower(final.Hostname()), "pluto.tv") || final.Scheme != "https" {
		return false
	}
	path := strings.TrimRight(strings.ToLower(final.Path), "/")
	if path != "/intl" && path != "/plutotv-is-not-available" && !plutoMoviePageURL(final) {
		return false
	}
	page := parseAvailabilityPage(string(body))
	if page.challenge {
		return false
	}
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	blocked := false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if availabilityIgnoredNode(node) {
			return
		}
		if node.Type == html.ElementNode && (node.Data == "h1" || node.Data == "h2" || node.Data == "h3") {
			label := strings.ToLower(strings.TrimSuffix(availabilityNodeText(node), "."))
			blocked = blocked || label == "pluto tv is not available in your location" || label == "pluto tv is not available in your country"
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return blocked
}
