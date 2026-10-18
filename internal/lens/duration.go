package lens

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var isoDuration = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?$`)

// DurationSeconds reads the duration formats hosts list: plain seconds, Go
// durations such as 1h35m12s, clock forms such as 1:35:12 or 35:12, and ISO
// 8601 PT1H35M12S, including the PT01:35:12 variant some catalogs send. It
// returns zero for anything it cannot read, so an unknown length stays unknown.
func DurationSeconds(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		return seconds
	}
	if duration, err := time.ParseDuration(strings.ReplaceAll(value, " ", "")); err == nil && duration > 0 {
		return duration.Seconds()
	}
	if strings.HasPrefix(value, "PT") {
		if strings.Contains(value, ":") {
			return clockSeconds(strings.TrimPrefix(value, "PT"))
		}
		if match := isoDuration.FindStringSubmatch(value); match != nil {
			hours, _ := strconv.ParseFloat(match[1], 64)
			minutes, _ := strconv.ParseFloat(match[2], 64)
			seconds, _ := strconv.ParseFloat(match[3], 64)
			return hours*3600 + minutes*60 + seconds
		}
		return 0
	}
	return clockSeconds(value)
}

func clockSeconds(value string) float64 {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	seconds := 0.0
	for i, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) || i > 0 && number >= 60 {
			return 0
		}
		seconds = seconds*60 + number
	}
	return seconds
}

// FormatDuration renders seconds the way Go does, for example 1h35m12s.
func FormatDuration(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	return (time.Duration(seconds) * time.Second).String()
}
