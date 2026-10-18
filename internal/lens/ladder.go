package lens

import "time"

// The verification ladder from section 5 of the white paper.
const (
	// LevelListed: a page references the object. Nothing has been checked.
	LevelListed = 0
	// LevelReachable: the location responds with content from the current egress.
	LevelReachable = 1
	// LevelIdentified: the object was confirmed at the location from metadata, file inspection or fingerprint.
	LevelIdentified = 2
	// LevelRendered: a sandbox opened it and the object played, displayed, ran or opened for a bounded sample.
	LevelRendered = 3
	// LevelClean: Rendered with Shield fully active and zero measured ad exposure. Phase 5 measures exposure.
	LevelClean = 4
)

var levelNames = [...]string{"listed", "reachable", "identified", "rendered", "clean"}

// LevelName returns the ladder name of a level.
func LevelName(level int) string {
	if level < 0 || level >= len(levelNames) {
		return "unknown"
	}
	return levelNames[level]
}

// LevelFromChecks places a scout result on the ladder from its availability
// and playback statuses. A decoded playback start is Rendered; recognised
// media is Identified; a responding page, even a restricted one, is
// Reachable; everything else is Listed. Clean is never inferred here because
// ad exposure is not yet measured.
func LevelFromChecks(availabilityStatus, playbackStatus string, playbackConfirmed bool) int {
	if playbackStatus == "started" && playbackConfirmed {
		return LevelRendered
	}
	switch availabilityStatus {
	case "media_reachable":
		return LevelIdentified
	case "page_reachable", "region_blocked", "sign_in_required", "access_restricted":
		return LevelReachable
	}
	return LevelListed
}

// FreshnessWindow is how long evidence at a level keeps its level before it
// decays one step. Playback and media checks age fastest because hosts move
// files; a listing is the slowest to change.
func FreshnessWindow(objectType string, level int) time.Duration {
	switch level {
	case LevelClean, LevelRendered:
		return 24 * time.Hour
	case LevelIdentified:
		return 3 * 24 * time.Hour
	case LevelReachable:
		return 7 * 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// EffectiveLevel is the level evidence still earns at its age: one step lower
// once the freshness window has passed, never below Listed.
func EffectiveLevel(level int, objectType string, age time.Duration) int {
	if level > LevelListed && age > FreshnessWindow(objectType, level) {
		return level - 1
	}
	if level < LevelListed {
		return LevelListed
	}
	return level
}
