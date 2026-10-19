package scout

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/shield"
)

const maxAutomaticProviderPlaybackChecks = 4

type providerPlaybackTransport struct {
	base    http.RoundTripper
	shield  *shield.Shield
	blocked atomic.Bool
}

func (t *providerPlaybackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, safe := safeAvailabilityURL(req.URL.String()); !safe {
		return nil, errors.New("provider check requires a public destination")
	}
	if t.shield.Engine().Inspect(req.URL.String(), "Media").Blocked {
		t.blocked.Store(true)
		return nil, errors.New("provider resource blocked by ad protection")
	}
	return t.base.RoundTrip(req)
}

// providerCheckDriver is the loaded driver whose access check covers a free
// listing's page; Pluto TV and Filmzie are such drivers. A paid or priced
// listing is never checked as a free provider's film.
func (s *Scout) providerCheckDriver(source CandidateSource) *crawler.Driver {
	if source.AccessType != "free" || source.Price != "" {
		return nil
	}
	page, safe := safeAvailabilityURL(source.URL)
	if !safe || page.Scheme != "https" {
		return nil
	}
	for _, d := range s.drivers {
		if d.Checks(source.URL) {
			return d
		}
	}
	return nil
}

func (s *Scout) supportedProviderPlayback(source CandidateSource) bool {
	return s.providerPlaybackKind(source) != ""
}

// providerPlaybackKind names the check a listing gets: a driver's name, or
// one of the inspectors still written in Go.
func (s *Scout) providerPlaybackKind(source CandidateSource) string {
	if d := s.providerCheckDriver(source); d != nil {
		return d.Name
	}
	switch {
	case nasaMovieTarget(source):
		return "nasa"
	case creatorMovieTarget(source):
		return "creator"
	case fawesomeMovieTarget(source):
		return "fawesome"
	case nfbMovieTarget(source):
		return "nfb"
	}
	return ""
}

func providerPlaybackBinding(source CandidateSource) bool {
	page, pageOK := safeAvailabilityURL(source.URL)
	media, mediaOK := safeAvailabilityURL(source.providerStreamURL)
	evidence, evidenceOK := safeAvailabilityURL(source.AccessEvidenceURL)
	if !source.providerContentBound || source.providerSSAI || !pageOK || !mediaOK || !evidenceOK {
		return false
	}
	if source.providerCheck != "" {
		return driverBoundPlaybackPair(source, page, media, evidence)
	}
	switch source.AccessBasis {
	case "nasa_movie_metadata":
		return nasaResolvedPlaybackPair(source, page, media, evidence)
	case "creator_movie_metadata":
		return creatorResolvedPlaybackPair(source, page, media, evidence)
	case "fawesome_movie_metadata":
		return fawesomeResolvedPlaybackPair(source, page, media, evidence)
	case "nfb_movie_metadata":
		return nfbResolvedPlaybackPair(source, page, media, evidence)
	}
	return false
}

// driverBoundPlaybackPair accepts only the exact page, media and evidence a
// driver's check bound together: the media the host handed over for this
// page, with nothing derived or substituted since.
func driverBoundPlaybackPair(source CandidateSource, page, media, evidence *url.URL) bool {
	boundPage, pageOK := safeAvailabilityURL(source.providerCheckPage)
	boundMedia, mediaOK := safeAvailabilityURL(source.providerStreamURL)
	boundEvidence, evidenceOK := safeAvailabilityURL(source.AccessEvidenceURL)
	if !pageOK || !mediaOK || !evidenceOK || source.AccessType != "free" || media.Scheme != "https" || evidence.Scheme != "https" {
		return false
	}
	return page.String() == boundPage.String() && media.String() == boundMedia.String() && evidence.String() == boundEvidence.String()
}

// Pasted supported movie links use the same provider check as discovered links.
// The URL-derived title is only a matching hint; provider metadata must bind
// the full movie before any media is requested or a start can be confirmed.
func (s *Scout) providerMovieURLInput(raw, region string) (CandidateSource, bool) {
	source := CandidateSource{ID: resolvedFreeMovieID("provider", raw), URL: raw, AccessType: "free", SourceType: "Web Listing", ListingOnly: true, DestinationType: "watch", Region: "Unknown"}
	if !s.supportedProviderPlayback(source) {
		return CandidateSource{}, false
	}
	page, _ := safeAvailabilityURL(raw)
	brand := s.providerPlaybackKind(source)
	parts := strings.Split(strings.Trim(page.Path, "/"), "/")
	switch {
	case s.providerCheckDriver(source) != nil:
		d := s.providerCheckDriver(source)
		source.Title, source.Provider = d.Provider+" movie", d.Provider
		if slug := d.URLTitle(raw); slug != "" {
			source.Title, source.ReleaseYear = movieQuery(strings.ReplaceAll(slug, "-", " "))
		}
		// The watch-option brand labels access the same way for a pasted
		// link as for a discovered one.
		brand, _ = movieDestination(source, page)
	case brand == "nasa":
		source.Title, source.ReleaseYear = movieQuery(strings.ReplaceAll(parts[1], "-", " "))
		source.Provider = "NASA+"
	case brand == "creator":
		source.Title, source.Provider, source.ReleaseYear = "Seder-Masochism", "Catsuka / Nina Paley", 2018
	case brand == "fawesome":
		source.Title = strings.ReplaceAll(parts[len(parts)-1], "-", " ")
		source.Provider = "Fawesome"
	case brand == "nfb":
		source.Title = strings.ReplaceAll(strings.ReplaceAll(parts[len(parts)-1], "-", " "), "_", " ")
		source.Provider = "NFB / ONF"
	}
	enrichMovieAccess(&source, brand)
	enrichMovieAvailability(&source, page, brand, region)
	return source, true
}

// Check public provider responses without executing ads, logging in, requesting
// DRM licenses or changing location. A preflight is separate from decoded
// playback: even a matching main-asset response cannot prove a movie start.
func (s *Scout) checkProviderMoviePlayback(ctx context.Context, results []VerifiedSource, region, country string) {
	var targets []int
	// Reserve coverage for every provider before another copy from one site.
	seen := map[string]bool{}
	for pass := 0; pass < 2; pass++ {
		for i := range results {
			if !s.supportedProviderPlayback(results[i].CandidateSource) {
				continue
			}
			provider := s.providerPlaybackKind(results[i].CandidateSource)
			if pass == 0 && seen[provider] {
				continue
			}
			duplicate := false
			for _, idx := range targets {
				duplicate = duplicate || idx == i || results[idx].URL == results[i].URL
			}
			if duplicate || len(targets) == maxAutomaticProviderPlaybackChecks {
				continue
			}
			targets = append(targets, i)
			seen[provider] = true
		}
	}
	if len(targets) == 0 || ctx.Err() != nil {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	slots := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, index := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-checkCtx.Done():
				return
			}
			if checkCtx.Err() != nil {
				return
			}
			s.inspectProviderMoviePlayback(checkCtx, &results[i], region, country)
		}(index)
	}
	wg.Wait()
}

func (s *Scout) inspectProviderMoviePlayback(ctx context.Context, result *VerifiedSource, region, country string) {
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	sh := s.shield
	if sh == nil {
		sh = shield.New()
	}
	guarded := *client
	base := guarded.Transport
	if base == nil {
		base = newMovieAvailabilityClient().Transport
	}
	trace := &providerPlaybackTransport{base: base, shield: sh}
	guarded.Transport, guarded.Jar = trace, nil
	// Each redirected request also goes through the protected transport.
	source := result.CandidateSource
	source.DirectStreamURL, source.KnownManifests = "", nil
	var check *PlaybackCheck
	if d := s.providerCheckDriver(source); d != nil {
		source, check = s.inspectDriverPlayback(ctx, trace, d, source)
	} else {
		switch s.providerPlaybackKind(source) {
		case "nasa":
			source, check = inspectNASAPlayback(ctx, &guarded, source)
		case "creator":
			source, check = inspectCreatorPlayback(ctx, &guarded, source)
		case "fawesome":
			source, check = inspectFawesomePlayback(ctx, &guarded, source)
		case "nfb":
			source, check = inspectNFBPlayback(ctx, &guarded, source)
		}
	}
	if trace.blocked.Load() {
		check = &PlaybackCheck{Status: "unconfirmed", Reason: "blocked_with_protection", Detail: "A resource needed for the provider check was blocked by ad protection. Protection remained enabled; movie playback was not confirmed.", EvidenceURL: source.URL}
	}
	if check == nil && source.providerSSAI {
		check = &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Detail: "The provider identified a movie stream with server-inserted advertising. Movie content could not be separated from preroll ads, so no movie start was confirmed.", EvidenceURL: source.URL}
	}
	if check == nil {
		// Use authorized main media only inside the protected sampler. Signed
		// provider URLs are never returned to the client as generic relay links.
		if !providerPlaybackBinding(source) {
			check = &PlaybackCheck{Status: "unconfirmed", Reason: "content_unconfirmed", Detail: "The provider response did not identify an authorized full movie suitable for a protected playback sample.", EvidenceURL: source.URL}
		} else {
			internal := source
			internal.ListingOnly, internal.DirectStreamURL = false, source.providerStreamURL
			verified := verifyFastHTTPWithClient(ctx, sh, internal, &guarded)
			if verified.Verified && !trace.blocked.Load() {
				result.Verified, result.VerificationStatus = true, "media_reachable"
				result.VerificationMode, result.StreamFormat = "Provider main media probe", verified.StreamFormat
				result.AvailabilityCheck = &AvailabilityCheck{Status: "media_reachable", Country: country, RequestedRegion: region, CheckedAt: verified.CheckedAt, EvidenceURL: source.URL, Method: "provider_main_media_probe", Detail: "The provider's authorized main movie media responded. A decoded movie sample is still required to confirm a start."}
				source.ListingOnly = false
			} else {
				reason := "content_unconfirmed"
				if trace.blocked.Load() {
					reason = "blocked_with_protection"
				}
				check = &PlaybackCheck{Status: "unconfirmed", Reason: reason, Detail: "The authorized movie media could not be reached with ad protection enabled. Movie playback remains unconfirmed.", EvidenceURL: source.URL}
			}
		}
	}
	source.DirectStreamURL, source.KnownManifests = "", nil
	result.CandidateSource = source
	result.DetectedManifests = nil
	if check == nil {
		return
	}
	// A preflight can only report access/restrictions, never decoded frames.
	check.Method, check.Country, check.RequestedRegion = "provider_playback_preflight", movieCountry(country), strings.ToUpper(strings.TrimSpace(region))
	check.PlaybackConfirmed, check.SecondsAdvanced, check.DecodedFrames = false, 0, 0
	check.Width, check.Height, check.DurationSeconds = 0, 0, 0
	if check.CheckedAt == "" {
		check.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	}
	result.PlaybackCheck = check
	switch check.Reason {
	case "region_unavailable", "sign_in_required":
		status := "sign_in_required"
		if check.Reason == "region_unavailable" {
			status = "region_blocked"
		}
		result.AvailabilityCheck = &AvailabilityCheck{Status: status, Country: check.Country, RequestedRegion: check.RequestedRegion, CheckedAt: check.CheckedAt, EvidenceURL: check.EvidenceURL, Method: check.Method, Detail: check.Detail}
	}
}

// checkEnvironment gives a driver's check the pipeline's judgments about the
// listing it is checking: what a page shows and whether a declared identity
// names the film. The driver says what the host's answers mean; it never
// decides film identity itself.
type checkEnvironment struct {
	source CandidateSource
}

func (e checkEnvironment) PageFacts(body []byte, final *url.URL) map[string]string {
	page := parseAvailabilityPage(string(body))
	identity := "mismatch"
	if availabilityMovieMatches(e.source, page, final.Hostname()) {
		identity = "match"
	}
	return map[string]string{
		"challenge":      strconv.FormatBool(page.challenge),
		"sign_in":        strconv.FormatBool(page.signIn || availabilityLoginPath(final.Path)),
		"soft_not_found": strconv.FormatBool(availabilitySoftNotFound(e.source, page)),
		"identity":       identity,
	}
}

func (e checkEnvironment) MetadataMatches(title string, year int, duration float64) bool {
	return matchingFreeMovieMetadata(e.source, title, year, duration)
}

// inspectDriverPlayback runs a driver's access check over the protected
// transport and turns its verdict into the pipeline's terms: a preflight
// check naming a restriction or the lack of attribution, or a source bound
// to the main media the host handed over, for the protected sampler alone.
func (s *Scout) inspectDriverPlayback(ctx context.Context, transport http.RoundTripper, d *crawler.Driver, source CandidateSource) (CandidateSource, *PlaybackCheck) {
	fetcher := crawler.NewFetcher(transport)
	fetcher.DisableBackoff()
	result := d.RunCheck(ctx, fetcher, source.URL, checkEnvironment{source: source})
	if result.Metadata != nil {
		// Matching page metadata stays useful even when playback is restricted.
		source.Title, source.ReleaseYear = result.Metadata.Title, result.Metadata.Year
		source.Duration = strconv.FormatFloat(result.Metadata.Duration, 'f', -1, 64)
	}
	if bind := result.Bind; bind != nil {
		source.ID = resolvedFreeMovieID(d.Name, source.URL)
		source.Provider = d.Provider
		source.SourceType = checkDefault(bind.SourceType, "Provider free movie")
		source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", checkDefault(bind.AccessBasis, "provider_check"), bind.Evidence
		source.AccessNote = bind.AccessNote
		source.providerContentBound, source.providerSSAI, source.providerStreamURL = true, bind.SSAI, bind.Media
		source.providerCheck, source.providerCheckPage = d.Name, source.URL
		source.DirectStreamURL, source.KnownManifests = "", nil
		if !bind.SSAI {
			return source, nil
		}
	}
	check := &PlaybackCheck{Status: result.Status, Reason: result.Reason, Detail: result.Detail, Method: "provider_playback_preflight", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: result.EvidenceURL}
	return source, check
}

func checkDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
