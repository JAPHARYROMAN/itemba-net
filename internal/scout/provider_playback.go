package scout

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

func supportedProviderPlayback(source CandidateSource) bool {
	return providerPlaybackKind(source) != ""
}

func providerPlaybackKind(source CandidateSource) string {
	switch {
	case plutoMoviePlaybackTarget(source):
		return "pluto"
	case filmzieMovieTarget(source):
		return "filmzie"
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
	switch source.AccessBasis {
	case "filmzie_movie_metadata":
		return filmzieResolvedPlaybackPair(source, page, media, evidence)
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

// Pasted supported movie links use the same provider check as discovered links.
// The URL-derived title is only a matching hint; provider metadata must bind
// the full movie before any media is requested or a start can be confirmed.
func providerMovieURLInput(raw, region string) (CandidateSource, bool) {
	source := CandidateSource{ID: resolvedFreeMovieID("provider", raw), URL: raw, AccessType: "free", SourceType: "Web Listing", ListingOnly: true, DestinationType: "watch", Region: "Unknown"}
	if !supportedProviderPlayback(source) {
		return CandidateSource{}, false
	}
	page, _ := safeAvailabilityURL(raw)
	brand := providerPlaybackKind(source)
	source.Title, source.Provider = "Pluto TV movie", "Pluto TV"
	if brand == "filmzie" || brand == "nasa" {
		parts := strings.Split(strings.Trim(page.Path, "/"), "/")
		source.Title, source.ReleaseYear = movieQuery(strings.ReplaceAll(parts[1], "-", " "))
		source.Provider = map[string]string{"filmzie": "Filmzie", "nasa": "NASA+"}[brand]
	} else if brand == "creator" {
		source.Title, source.Provider, source.ReleaseYear = "Seder-Masochism", "Catsuka / Nina Paley", 2018
	} else if brand == "fawesome" {
		parts := strings.Split(strings.Trim(page.Path, "/"), "/")
		source.Title = strings.ReplaceAll(parts[len(parts)-1], "-", " ")
		source.Provider = "Fawesome"
	} else if brand == "nfb" {
		parts := strings.Split(strings.Trim(page.Path, "/"), "/")
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
	// Reserve coverage for both providers before another copy from one site.
	seen := map[string]bool{}
	for pass := 0; pass < 2; pass++ {
		for i := range results {
			if !supportedProviderPlayback(results[i].CandidateSource) {
				continue
			}
			provider := providerPlaybackKind(results[i].CandidateSource)
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
	switch providerPlaybackKind(source) {
	case "pluto":
		source, check = inspectPlutoPlayback(ctx, &guarded, source)
	case "filmzie":
		source, check = inspectFilmziePlayback(ctx, &guarded, source)
	case "nasa":
		source, check = inspectNASAPlayback(ctx, &guarded, source)
	case "creator":
		source, check = inspectCreatorPlayback(ctx, &guarded, source)
	case "fawesome":
		source, check = inspectFawesomePlayback(ctx, &guarded, source)
	case "nfb":
		source, check = inspectNFBPlayback(ctx, &guarded, source)
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
