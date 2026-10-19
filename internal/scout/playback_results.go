package scout

import (
	"context"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxAutomaticPlaybackChecks = 2

func skippedPlayback(region, country, detail string) *PlaybackCheck {
	return &PlaybackCheck{Status: "skipped", RequestedRegion: region, Country: country, Method: "not_checked", Detail: detail}
}

// Keep automatic samples bounded and separate from reachability observations.
// Only the catalog-owned free-media paths supported by the probe are eligible.
func (s *Scout) checkFreeMoviePlayback(ctx context.Context, results []VerifiedSource, region, country string) {
	var indexes []int
	for i := range results {
		if results[i].AccessType != "free" && !s.supportedProviderPlayback(results[i].CandidateSource) {
			continue
		}
		results[i].PlaybackCheck = skippedPlayback(region, country, "Automatic playback needs a reachable free video from a supported movie catalog. Open the source to check playback.")
		if s.supportedProviderPlayback(results[i].CandidateSource) {
			results[i].PlaybackCheck.Detail = "Not checked within this search's four-link provider access-check budget. Open the movie page with ad blocking to check access."
		}
	}
	s.checkProviderMoviePlayback(ctx, results, region, country)
	for i := range results {
		if eligibleFreePlayback(results[i]) && (results[i].PlaybackCheck == nil || results[i].PlaybackCheck.Method == "not_checked") {
			indexes = append(indexes, i)
			results[i].PlaybackCheck = skippedPlayback(region, country, "Not sampled within this search's two-video playback budget. You can still open the movie page with ad blocking.")
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		return playbackSampleLess(results[indexes[i]], results[indexes[j]], region)
	})
	indexes = diversePlaybackIndexes(results, indexes)
	if len(indexes) == 0 || ctx.Err() != nil {
		return
	}
	probe := s.playbackProbe
	if probe == nil {
		probe = s.probeFreeMoviePlayback
	}
	checkCtx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, idx := range indexes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.playbackSlots != nil {
				select {
				case s.playbackSlots <- struct{}{}:
					defer func() { <-s.playbackSlots }()
				case <-checkCtx.Done():
					return
				}
			}
			if checkCtx.Err() != nil {
				return
			}
			if check := probe(checkCtx, results[i], region, country); check != nil {
				results[i].PlaybackCheck = check
			}
		}(idx)
	}
	wg.Wait()
}

func playbackSampleLess(a, b VerifiedSource, region string) bool {
	if pa, pb := checkedWatchPriority(a, region), checkedWatchPriority(b, region); pa != pb {
		return pa < pb
	}
	if a.RelevanceScore != b.RelevanceScore {
		return a.RelevanceScore > b.RelevanceScore
	}
	// Discovered movie hosts retain a sample before built-in reference CDNs.
	if (a.SourceType == "HLS Reference") != (b.SourceType == "HLS Reference") {
		return a.SourceType != "HLS Reference"
	}
	// Equal movie/evidence candidates can spend the short sample window more
	// usefully on declared browser formats and lower known average bitrates.
	// This is a scheduling hint only; native decoding must still prove playback.
	if a.mediaBrowserFormat != b.mediaBrowserFormat {
		return a.mediaBrowserFormat
	}
	aKnown, bKnown := positiveFinitePlaybackBitrate(a.mediaBitrate), positiveFinitePlaybackBitrate(b.mediaBitrate)
	if aKnown != bKnown {
		return aKnown
	}
	return aKnown && a.mediaBitrate < b.mediaBitrate
}

func positiveFinitePlaybackBitrate(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Spend the small playback budget across different sites before checking
// additional copies from one site. Identical media URLs are sampled once.
func diversePlaybackIndexes(results []VerifiedSource, indexes []int) []int {
	selected := make([]int, 0, maxAutomaticPlaybackChecks)
	sites, streams, used := map[string]bool{}, map[string]bool{}, map[int]bool{}
	for pass := 0; pass < 2; pass++ {
		for _, i := range indexes {
			stream := results[i].DirectStreamURL
			if stream == "" {
				stream = results[i].providerStreamURL
			}
			if used[i] || streams[stream] {
				continue
			}
			site := results[i].Provider
			if parsed, err := url.Parse(results[i].URL); err == nil && parsed.Hostname() != "" {
				site = strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
			}
			if pass == 0 && sites[site] {
				continue
			}
			selected = append(selected, i)
			sites[site], streams[stream], used[i] = true, true, true
			if len(selected) == maxAutomaticPlaybackChecks {
				return selected
			}
		}
	}
	return selected
}

func (s *Scout) probeFreeMoviePlayback(ctx context.Context, source VerifiedSource, region, country string) *PlaybackCheck {
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	internal := source
	if source.providerContentBound {
		internal.DirectStreamURL = source.providerStreamURL
	}
	origin, closeHarness, outcome, err := startProtectedMoviePlaybackHarness(ctx, internal, client, s.shield)
	if err != nil {
		check := &PlaybackCheck{Status: "unconfirmed", RequestedRegion: region, Country: country, Method: "isolated_video_sample", CheckedAt: time.Now().UTC().Format(time.RFC3339), EvidenceURL: source.URL, Detail: "The isolated playback sample could not be prepared; playback remains unconfirmed."}
		return applyPlaybackProtectionOutcome(check, outcome)
	}
	defer closeHarness()
	return applyPlaybackProtectionOutcome(verifyFreeMoviePlayback(ctx, source, origin, region, country), outcome)
}

func applyPlaybackProtectionOutcome(check *PlaybackCheck, outcome func() string) *PlaybackCheck {
	if check == nil || outcome == nil {
		return check
	}
	if reason := outcome(); reason != "" {
		check.Status, check.Reason, check.PlaybackConfirmed = "unconfirmed", reason, false
		switch reason {
		case "blocked_with_protection":
			check.Detail = "A playback resource was blocked by ad protection. Protection remained enabled; movie playback was not confirmed."
		case "drm_required":
			check.Detail = "This stream requires DRM. No license was requested and movie playback was not confirmed."
		default:
			check.Detail = "The stream contains advertising cues or movie content could not be distinguished from ads. No movie start was confirmed."
		}
	}
	return check
}
