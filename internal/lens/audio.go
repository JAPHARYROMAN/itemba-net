package lens

import (
	"math"
	"strings"
)

// Audio identity. A recording is what its tags say it is: a title, a
// performer and a duration, read the same way from every host; and, once a
// sample has heard it, what it sounds like. The tags decide whether two
// listings are the same recording and how well a listing answers a query
// until both carry a fingerprint, which then overrules them.

// AudioIdentity is a recording as far as its tags and, when a sample heard
// it, its fingerprint say. Keys are normalised for comparison. An empty
// performer, a zero duration or a missing fingerprint is unknown, which
// never contradicts a known value.
type AudioIdentity struct {
	Title           string       `json:"title"`
	TitleKey        string       `json:"titleKey"`
	Creator         string       `json:"creator,omitempty"`
	CreatorKey      string       `json:"creatorKey,omitempty"`
	Year            int          `json:"year,omitempty"`
	DurationSeconds float64      `json:"durationSeconds,omitempty"`
	Fingerprint     *Fingerprint `json:"fingerprint,omitempty"`
}

// dashSeparators are the ways hosts and people write "Performer - Title".
var dashSeparators = []string{" - ", " – ", " — ", " | "}

// leadingNonPerformers are words that begin an object of "by" inside a
// title ("Blinded by the Light") rather than a performer after it.
var leadingNonPerformers = map[string]bool{
	"the": true, "a": true, "an": true, "my": true, "your": true, "his": true, "her": true, "our": true, "their": true,
	"me": true, "you": true, "him": true, "us": true, "them": true, "it": true, "myself": true, "yourself": true, "now": true, "night": true, "day": true,
}

// SplitPerformer reads a performer out of a query phrased as "Title by
// Performer" or "Performer - Title". The "by" form is read at its last "by",
// so "Stand by Me by Ben E. King" keeps its title, and it is refused when
// the words after "by" read as part of the title ("Blinded by the Light") or
// when both sides are a single word ("Killed by Death"), which is as often a
// title as a credit. The dash form follows the music convention of the
// performer first. Anything else is a title with no performer.
func SplitPerformer(value string) (title, performer string) {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if i := strings.LastIndex(lower, " by "); i > 0 {
		before, after := strings.TrimSpace(value[:i]), strings.TrimSpace(value[i+4:])
		beforeWords, afterWords := strings.Fields(before), strings.Fields(after)
		if len(beforeWords) > 0 && len(afterWords) > 0 && !leadingNonPerformers[strings.ToLower(afterWords[0])] && (len(beforeWords) > 1 || len(afterWords) > 1) {
			return before, after
		}
	}
	return splitDashPerformer(value)
}

// splitDashPerformer reads "Performer - Title"; a title with no dash is
// returned whole.
func splitDashPerformer(value string) (title, performer string) {
	for _, sep := range dashSeparators {
		if i := strings.Index(value, sep); i > 0 {
			before, after := strings.TrimSpace(value[:i]), strings.TrimSpace(value[i+len(sep):])
			if before != "" && after != "" {
				return after, before
			}
		}
	}
	return strings.TrimSpace(value), ""
}

// AudioListing reads a host's tags into an identity. A performer tag wins
// over one written into the title; without a tag, a "Performer - Title"
// title is split, and with one the title loses its prefix only when the
// prefix names the tagged performer, so "Live at Fillmore - Disc 1" keeps
// its name. A release label in the title is separated as for any listing,
// and a known year wins over one read from the title.
func AudioListing(title, creator, duration string, year int) AudioIdentity {
	base, year := ListedTitle(title, year)
	creator = strings.TrimSpace(creator)
	if creator == "" {
		base, creator = splitDashPerformer(base)
	} else if split, prefix := splitDashPerformer(base); prefix != "" && CreatorsAgree(NormalizeTitle(prefix), NormalizeTitle(creator)) {
		base = split
	}
	return AudioIdentity{
		Title:           base,
		TitleKey:        NormalizeTitle(base),
		Creator:         creator,
		CreatorKey:      NormalizeTitle(creator),
		Year:            year,
		DurationSeconds: DurationSeconds(duration),
	}
}

// DurationsAgree reports whether two known durations describe the same
// recording: within three seconds or two percent of the longer, whichever
// is larger, which covers encoder padding and rounded listings but not a
// radio edit against an album cut.
func DurationsAgree(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	tolerance := math.Max(3, 0.02*math.Max(a, b))
	return math.Abs(a-b) <= tolerance
}

// EditionLengthsAgree is the length rule for two files whose sounds
// matched: within ten seconds or five percent of the longer, whichever is
// larger, because uploads of one master trim lead-in, silence and fades
// differently by that much, while an edit is a third shorter or more. It
// is looser than DurationsAgree, which judges lengths that stand alone.
func EditionLengthsAgree(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	tolerance := math.Max(10, 0.05*math.Max(a, b))
	return math.Abs(a-b) <= tolerance
}

// CreatorsAgree compares two known performer keys: equal, or one naming the
// other, so "Miles Davis" agrees with "Miles Davis Quintet". An empty key
// is unknown and agrees with nothing here; callers treat it as no evidence.
func CreatorsAgree(a, b string) bool {
	return a != "" && b != "" && (a == b || strings.Contains(a, b) || strings.Contains(b, a))
}

// RecordingMatch is MatchRecordings' answer: whether two identities name
// the same recording and on what basis. Basis is fingerprint when both
// carried one and the sounds decided, duration when the sounds agreed but
// the lengths did not (a radio edit that keeps the album's intro), and
// tags otherwise. Distance is the fingerprints' bit error rate when they
// were compared.
type RecordingMatch struct {
	Same     bool    `json:"same"`
	Basis    string  `json:"basis"`
	Distance float64 `json:"distance,omitempty"`
}

// MatchRecordings decides whether two identities name the same recording.
// When both carry a fingerprint and the two can be compared, the sound
// decides: the same sound is the same recording whatever the tags say,
// unless both lengths are known and disagree as editions once the second
// is scaled to the speed the sounds matched at, and a different sound is a
// different recording even under the same tags. Otherwise the tags decide:
// the same title, performers that agree when both are known, and durations
// that agree when both are known.
func MatchRecordings(a, b AudioIdentity) RecordingMatch {
	if a.Fingerprint != nil && b.Fingerprint != nil {
		if c := CompareFingerprints(*a.Fingerprint, *b.Fingerprint); c.Known {
			if !c.Same() {
				return RecordingMatch{Basis: "fingerprint", Distance: c.Distance}
			}
			if a.DurationSeconds > 0 && b.DurationSeconds > 0 && !EditionLengthsAgree(a.DurationSeconds, b.DurationSeconds*c.Speed) {
				return RecordingMatch{Basis: "duration", Distance: c.Distance}
			}
			return RecordingMatch{Same: true, Basis: "fingerprint", Distance: c.Distance}
		}
	}
	match := RecordingMatch{Basis: "tags"}
	if a.TitleKey == "" || a.TitleKey != b.TitleKey {
		return match
	}
	if a.CreatorKey != "" && b.CreatorKey != "" && !CreatorsAgree(a.CreatorKey, b.CreatorKey) {
		return match
	}
	if a.DurationSeconds > 0 && b.DurationSeconds > 0 && !DurationsAgree(a.DurationSeconds, b.DurationSeconds) {
		return match
	}
	match.Same = true
	return match
}

// SameRecording reports whether two identities name the same recording,
// by their fingerprints when both carry one and by their tags otherwise.
func SameRecording(a, b AudioIdentity) bool {
	return MatchRecordings(a, b).Same
}

// AudioRelevance scores how well a listing answers an audio query, from 0
// to 1: the title relevance when the title matches, contains or holds every
// word of the query (a title that merely shares some words scores nothing),
// lowered when the query names a performer the listing leaves unknown, zero
// when the listing names a different performer, and halved when both name
// different years.
func AudioRelevance(q ObjectQuery, id AudioIdentity) float64 {
	score := TitleRelevance(q.TitleKey, id.TitleKey)
	if score < 0.5 {
		return 0
	}
	if q.CreatorKey != "" {
		switch {
		case id.CreatorKey == "":
			score *= 0.8
		case CreatorsAgree(q.CreatorKey, id.CreatorKey):
		default:
			return 0
		}
	}
	if q.Year != 0 && id.Year != 0 && q.Year != id.Year {
		score *= 0.5
	}
	return score
}
