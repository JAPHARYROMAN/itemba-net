package console

import (
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
	"github.com/itemba-net/core/internal/scout"
)

// observationsFromReport turns a search report into Atlas observations:
// every source becomes a listing with the evidence its checks earned.
// Pruned sources are not locations and are left out. This is the bridge from
// scout's untyped results; it shrinks as providers become drivers.
func observationsFromReport(report scout.SearchReport) []atlas.Observation {
	sources := report.Sources
	if len(sources) == 0 {
		sources = report.VerifiedSources
	}
	egress := ""
	if report.AvailabilityChecks != nil {
		egress = report.AvailabilityChecks.Country
	}
	observations := make([]atlas.Observation, 0, len(sources))
	for _, source := range sources {
		listing := atlas.Listing{
			Kind:        source.Kind,
			Title:       source.Title,
			Creator:     source.Creator,
			URL:         source.URL,
			MediaURL:    source.DirectStreamURL,
			Provider:    source.Provider,
			SourceType:  source.SourceType,
			AccessType:  source.AccessType,
			Price:       source.Price,
			Market:      source.Region,
			Year:        source.ReleaseYear,
			Duration:    source.Duration,
			Description: source.Description,
			Thumbnail:   source.Thumbnail,
			ListingOnly: source.ListingOnly,
		}
		// What the sample heard goes to the fingerprint library with the
		// length of the file it heard it from, as the browser read it.
		if check := source.PlaybackCheck; check != nil && check.Fingerprint.Valid() {
			listing.Fingerprint = &atlas.FingerprintObservation{Fingerprint: *check.Fingerprint, MediaSeconds: check.DurationSeconds, ObservedAt: observedOrNow(check.CheckedAt)}
		}
		observations = append(observations, atlas.Observation{Listing: listing, Evidence: evidenceFor(source, egress)})
	}
	return observations
}

// evidenceFor turns a source's checks into ladder evidence. A source with no
// checks gets none here and is recorded as listed by its provider.
func evidenceFor(source scout.VerifiedSource, defaultEgress string) []atlas.Evidence {
	var out []atlas.Evidence
	if check := source.AvailabilityCheck; check != nil && check.Status != "" {
		out = append(out, atlas.Evidence{
			Level:      lens.LevelFromChecks(check.Status, "", false),
			Method:     firstNonEmpty(check.Method, "availability_check"),
			Status:     check.Status,
			Detail:     check.Detail,
			Egress:     firstNonEmpty(check.Country, defaultEgress),
			ObservedAt: observedOrNow(check.CheckedAt),
			Artefacts:  map[string]any{"httpStatus": check.HTTPStatus, "evidenceUrl": check.EvidenceURL, "requestedRegion": check.RequestedRegion},
		})
	}
	if check := source.PlaybackCheck; check != nil && check.Status != "" && check.Status != "skipped" {
		level := lens.LevelListed
		if check.Status == "started" && check.PlaybackConfirmed {
			level = lens.LevelRendered
		} else if source.AvailabilityCheck != nil {
			level = lens.LevelFromChecks(source.AvailabilityCheck.Status, "", false)
		}
		artefacts := map[string]any{
			"secondsAdvanced": check.SecondsAdvanced,
			"decodedFrames":   check.DecodedFrames,
			"width":           check.Width,
			"height":          check.Height,
			"durationSeconds": check.DurationSeconds,
			"evidenceUrl":     check.EvidenceURL,
			"requestedRegion": check.RequestedRegion,
		}
		if check.FingerprintStatus != "" {
			artefacts["fingerprintStatus"] = check.FingerprintStatus
		}
		out = append(out, atlas.Evidence{
			Level:      level,
			Method:     firstNonEmpty(check.Method, "playback_sample"),
			Status:     check.Status,
			Detail:     firstNonEmpty(check.Reason, check.Detail),
			Egress:     firstNonEmpty(check.Country, defaultEgress),
			ObservedAt: observedOrNow(check.CheckedAt),
			Artefacts:  artefacts,
		})
	}
	if len(out) == 0 && defaultEgress != "" {
		out = append(out, atlas.Evidence{Level: lens.LevelListed, Method: "provider_listing", Status: "listed", Detail: source.Provider, Egress: defaultEgress, ObservedAt: time.Now()})
	}
	return out
}

func observedOrNow(value string) time.Time {
	if t, ok := lens.ObservedTime(value); ok {
		return t
	}
	return time.Now()
}
