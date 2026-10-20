package scout

import (
	"fmt"
	"strings"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// AvailabilityCheck describes an observation from one network, not a guarantee
// of a complete movie or the signed-in user's subscription/DRM entitlement.
type AvailabilityCheck struct {
	Status            string `json:"status"`
	Country           string `json:"country,omitempty"`
	RequestedRegion   string `json:"requestedRegion"`
	CheckedAt         string `json:"checkedAt"`
	Detail            string `json:"detail"`
	HTTPStatus        int    `json:"httpStatus,omitempty"`
	EvidenceURL       string `json:"evidenceUrl,omitempty"`
	PlaybackConfirmed bool   `json:"playbackConfirmed"`
	Method            string `json:"method"`
}

type AvailabilityCheckCoverage struct {
	Country              string `json:"country,omitempty"`
	RequestedRegion      string `json:"requestedRegion"`
	CheckedAt            string `json:"checkedAt"`
	Method               string `json:"method"`
	CheckedCount         int    `json:"checkedCount"`
	PlaybackCheckedCount int    `json:"playbackCheckedCount"`
	ProviderCheckedCount int    `json:"providerCheckedCount"`
	PlaybackStartedCount int    `json:"playbackStartedCount"`
	// FingerprintCount is how many sampled recordings yielded an acoustic
	// fingerprint; movie searches report none.
	FingerprintCount int    `json:"fingerprintCount,omitempty"`
	Detail           string `json:"detail"`
}

// PlaybackCheck records a short decoded video sample, separately from HTTP
// reachability. It never promises uninterrupted playback of the complete film.
type PlaybackCheck struct {
	Status            string  `json:"status"`
	Reason            string  `json:"reason,omitempty"`
	Country           string  `json:"country,omitempty"`
	RequestedRegion   string  `json:"requestedRegion"`
	CheckedAt         string  `json:"checkedAt"`
	Detail            string  `json:"detail"`
	EvidenceURL       string  `json:"evidenceUrl,omitempty"`
	SecondsAdvanced   float64 `json:"secondsAdvanced"`
	DecodedFrames     int     `json:"decodedFrames"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DurationSeconds   float64 `json:"durationSeconds"`
	Method            string  `json:"method"`
	PlaybackConfirmed bool    `json:"playbackConfirmed"`
	// Fingerprint is what an audio sample heard of the file's beginning,
	// when the harness page computed one, and FingerprintStatus how that
	// went: computed, pending, timeout, invalid, failed, or the page's own
	// cause (fetch_failed, decode_failed, too_short, unsupported). A movie
	// sample carries neither.
	Fingerprint       *lens.Fingerprint `json:"fingerprint,omitempty"`
	FingerprintStatus string            `json:"fingerprintStatus,omitempty"`
}

const maxAutomaticPageChecks = 6

func uncheckedMovieAvailability(region, country, detail string) *AvailabilityCheck {
	return &AvailabilityCheck{Status: "unconfirmed", Country: country, RequestedRegion: region, Detail: detail, Method: "not_checked"}
}

func mediaAvailabilityResult(source VerifiedSource, region, country string) *AvailabilityCheck {
	check := uncheckedMovieAvailability(region, country, "The media check did not confirm a reachable video response.")
	check.Method = "http_media_probe"
	check.CheckedAt = source.CheckedAt
	check.EvidenceURL = source.DirectStreamURL
	if source.VerificationStatus == "media_reachable" {
		check.Status = "media_reachable"
		location := "the check server; its country is unconfirmed"
		if country != "" {
			location = country
		}
		if country == "TZ" {
			location = "Tanzania"
		}
		check.Detail = "The media URL returned recognizable video or streaming data from " + location + ". Full movie playback and account access are unconfirmed."
	} else if source.PrunedReason != "" {
		check.Detail = "Media response not confirmed: " + source.PrunedReason + ". The movie page may still be accessible."
		var status int
		if _, err := fmt.Sscanf(source.PrunedReason, "Media returned HTTP %d", &status); err == nil && status >= 100 && status <= 599 {
			check.HTTPStatus = status
			switch status {
			case 404, 410:
				check.Status = "not_found"
			case 451:
				check.Status = "region_blocked"
			case 401:
				check.Status = "sign_in_required"
			case 403, 429:
				check.Status = "access_restricted"
			default:
				if status >= 500 {
					check.Status = "error"
				}
			}
		}
	}
	return check
}

// Global searches rank observed playback and reachability from any check
// network. An explicitly selected country requires matching country evidence
// before that observation receives a local priority. Neither case promises
// worldwide playback; the actual check location remains separate metadata.
func checkedWatchPriority(source VerifiedSource, region string) int {
	region = movieCountry(region)
	global := region == ""
	if playbackStarted(source) && (global || source.PlaybackCheck.Country != "" && strings.EqualFold(source.PlaybackCheck.Country, region)) {
		return -10
	}
	check := source.AvailabilityCheck
	if check != nil {
		matchingCountry := global || check.Country != "" && strings.EqualFold(check.Country, region)
		switch check.Status {
		case "not_found":
			return 100
		case "region_blocked":
			if matchingCountry {
				return 100
			}
			return 70
		case "access_restricted", "error":
			return 90
		}
		if matchingCountry {
			switch check.Status {
			case "media_reachable":
				return 0
			case "page_reachable":
				if !global && source.AvailabilityStatus == "other_region" {
					return 50
				}
				return 10
			case "sign_in_required":
				if !global && source.AvailabilityStatus == "other_region" {
					return 55
				}
				return 30
			}
		}
	}
	if global {
		return 42
	}
	if source.AvailabilityStatus == "other_region" {
		return 60
	}
	return 40 + watchOptionPriority(source.CandidateSource)
}

func movieAvailabilityCoverage(region, country, locationDetail string, results []VerifiedSource) *AvailabilityCheckCoverage {
	count := 0
	playbackChecked, playbackStartedCount, providerChecked := 0, 0, 0
	for _, result := range results {
		if result.PlaybackCheck != nil && result.PlaybackCheck.CheckedAt != "" && result.PlaybackCheck.Method == "provider_playback_preflight" {
			providerChecked++
		}
		if result.AvailabilityCheck != nil && result.AvailabilityCheck.Method != "not_checked" {
			count++
		}
		if result.PlaybackCheck != nil && (result.PlaybackCheck.Method == "isolated_video_sample" || result.PlaybackCheck.Method == "isolated_audio_sample") && result.PlaybackCheck.Status != "skipped" {
			playbackChecked++
		}
		if playbackStarted(result) || audioPlaybackStarted(result) {
			playbackStartedCount++
		}
	}
	detail := "Up to two supported free videos receive a short playback sample. A successful start does not confirm the complete movie. Paid access may require sign-in. "
	if country == "" {
		detail += "The check server's country could not be confirmed. "
	}
	if country != "" && region != "ALL" && country != region {
		detail += "These checks do not confirm access in " + region + ". "
	}
	if country == "" && locationDetail != "" {
		detail += " " + locationDetail
	}
	return &AvailabilityCheckCoverage{Country: country, RequestedRegion: region, CheckedAt: time.Now().UTC().Format(time.RFC3339), Method: "direct_server_http", CheckedCount: count, PlaybackCheckedCount: playbackChecked, ProviderCheckedCount: providerChecked, PlaybackStartedCount: playbackStartedCount, Detail: detail}
}

func playbackStarted(source VerifiedSource) bool {
	p := source.PlaybackCheck
	return source.AccessType == "free" && p != nil && p.Status == "started" && p.Reason == "" && p.PlaybackConfirmed && p.Method == "isolated_video_sample" && p.SecondsAdvanced >= 1 && p.DecodedFrames > 0 && p.Width > 0 && p.Height > 0
}
