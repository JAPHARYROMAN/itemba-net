package scout

import "math"

const minimumFeatureSeconds = 40 * 60

// Unknown runtime remains unknown. A listed runtime is evidence about a
// candidate; native media metadata, when observed, takes precedence.
func knownShortMovie(listed string, observed float64) bool {
	seconds := mediaDurationSeconds(listed)
	if observed > 0 && !math.IsNaN(observed) && !math.IsInf(observed, 0) {
		seconds = observed
	}
	return seconds > 0 && seconds < minimumFeatureSeconds
}
