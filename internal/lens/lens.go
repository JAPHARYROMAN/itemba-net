// Package lens decides what an object is: it parses a search request into a
// typed object description, normalises titles so the same thing matches
// itself across spellings, places evidence on the verification ladder, and
// ranks candidate locations under the hundred cap.
package lens

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Object types, matching the atlas.objects.type check constraint.
const (
	TypeVideo    = "video"
	TypeAudio    = "audio"
	TypeImage    = "image"
	TypeDocument = "document"
	TypeGame     = "game"
	TypeSoftware = "software"
	TypeLive     = "live"
)

// ObjectQuery is a parsed search request. A URL or hash short-circuits to
// identification; otherwise Title and TitleKey carry the object's name.
type ObjectQuery struct {
	Raw      string `json:"raw"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	TitleKey string `json:"titleKey"`
	Year     int    `json:"year,omitempty"`
	URL      string `json:"url,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// typeHints are words that name an object type when they trail a title.
var typeHints = map[string]string{
	"movie": TypeVideo, "film": TypeVideo, "video": TypeVideo, "episode": TypeVideo, "series": TypeVideo, "season": TypeVideo, "documentary": TypeVideo,
	"mp3": TypeAudio, "flac": TypeAudio, "album": TypeAudio, "song": TypeAudio, "track": TypeAudio, "soundtrack": TypeAudio, "podcast": TypeAudio, "audiobook": TypeAudio,
	"pdf": TypeDocument, "epub": TypeDocument, "book": TypeDocument, "ebook": TypeDocument, "paper": TypeDocument, "thesis": TypeDocument, "manual": TypeDocument,
	"game": TypeGame, "rom": TypeGame,
	"wallpaper": TypeImage, "photo": TypeImage, "photos": TypeImage, "picture": TypeImage, "pictures": TypeImage, "image": TypeImage, "poster": TypeImage,
	"software": TypeSoftware, "installer": TypeSoftware, "apk": TypeSoftware, "firmware": TypeSoftware,
	"livestream": TypeLive,
}

var (
	yearToken = regexp.MustCompile(`^\(?((?:18|19|20)\d\d)\)?$`)
	hexToken  = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// Parse interprets a raw query. It never fails: an unrecognised query is a
// video title with no year.
func Parse(raw string) ObjectQuery {
	q := ObjectQuery{Raw: strings.TrimSpace(raw), Type: TypeVideo}
	if q.Raw == "" {
		return q
	}
	if u, err := url.Parse(q.Raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		q.URL = u.String()
		return q
	}
	if n := len(q.Raw); (n == 32 || n == 40 || n == 64) && hexToken.MatchString(q.Raw) {
		q.Hash = strings.ToLower(q.Raw)
		return q
	}

	tokens := strings.Fields(q.Raw)
	maxYear := time.Now().Year() + 1
	for i := len(tokens) - 1; i >= 0; i-- {
		if match := yearToken.FindStringSubmatch(tokens[i]); match != nil {
			year, _ := strconv.Atoi(match[1])
			if year <= maxYear && len(tokens) > 1 {
				q.Year = year
				tokens = append(tokens[:i], tokens[i+1:]...)
				break
			}
		}
	}
	if len(tokens) > 1 {
		last := strings.ToLower(strings.Trim(tokens[len(tokens)-1], ".,:;!?"))
		if hinted, ok := typeHints[last]; ok && !onlyArticles(tokens[:len(tokens)-1]) {
			q.Type = hinted
			tokens = tokens[:len(tokens)-1]
		}
	}
	q.Title = strings.Join(tokens, " ")
	q.TitleKey = NormalizeTitle(q.Title)
	return q
}

func onlyArticles(tokens []string) bool {
	for _, token := range tokens {
		switch strings.ToLower(token) {
		case "the", "a", "an", "le", "la", "les", "el", "los", "der", "die", "das":
		default:
			return false
		}
	}
	return true
}

var stripMarks = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// NormalizeTitle reduces a title to a comparison key: diacritics removed,
// lower case, "&" as "and", punctuation as spaces, single spaces. Articles
// are kept so that different titles do not collide.
func NormalizeTitle(title string) string {
	plain, _, err := transform.String(stripMarks, title)
	if err != nil {
		plain = title
	}
	plain = strings.ReplaceAll(strings.ToLower(plain), "&", " and ")
	var b strings.Builder
	lastSpace := true
	for _, r := range plain {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// TitleRelevance scores how well a location's title key matches a query key:
// 1 for an exact match, 0.8 when one contains the other, otherwise the share
// of query tokens present in the title.
func TitleRelevance(queryKey, titleKey string) float64 {
	if queryKey == "" || titleKey == "" {
		return 0
	}
	if queryKey == titleKey {
		return 1
	}
	if strings.Contains(titleKey, queryKey) || strings.Contains(queryKey, titleKey) {
		return 0.8
	}
	queryTokens := strings.Fields(queryKey)
	titleTokens := make(map[string]bool)
	for _, token := range strings.Fields(titleKey) {
		titleTokens[token] = true
	}
	hits := 0
	for _, token := range queryTokens {
		if titleTokens[token] {
			hits++
		}
	}
	return 0.6 * float64(hits) / float64(len(queryTokens))
}

// ObservedTime parses the timestamps scout attaches to checks. It accepts
// RFC 3339 and the clock-only form some checks use, which is taken as today.
func ObservedTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	if t, err := time.Parse("15:04:05 UTC", value); err == nil {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC), true
	}
	return time.Time{}, false
}

// Hostname extracts the comparison hostname of a location: lower case, no
// leading www. An unparseable URL yields an empty string.
func Hostname(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "www.")
}
