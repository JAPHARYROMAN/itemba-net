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
	// RawKey is the whole query normalised, before any year or type word is
	// taken out, so an alias that happens to end in a type word still matches.
	RawKey string `json:"rawKey,omitempty"`
	// Creator and CreatorKey name the performer an audio query asks for, read
	// by ParseAs; they are empty for every other type so far.
	Creator    string `json:"creator,omitempty"`
	CreatorKey string `json:"creatorKey,omitempty"`
	Year       int    `json:"year,omitempty"`
	URL        string `json:"url,omitempty"`
	Hash       string `json:"hash,omitempty"`
	// Open says the query named no kind, so a lookup may search every
	// kind; Type then holds Parse's default and should not narrow a search.
	Open bool `json:"open,omitempty"`
}

// leadingHints are phrases that name an object type when they open a
// query: "the film Sintel", "a painting called Starry Night", "the book
// Alice in Wonderland by Lewis Carroll". The phrase goes; what follows is
// the title, with its creator after "by".
var leadingHints = map[string]string{
	"film": TypeVideo, "movie": TypeVideo, "video": TypeVideo, "documentary": TypeVideo, "short": TypeVideo,
	"recording": TypeAudio, "album": TypeAudio, "song": TypeAudio, "track": TypeAudio, "audiobook": TypeAudio, "podcast": TypeAudio, "symphony": TypeAudio,
	"book": TypeDocument, "novel": TypeDocument, "ebook": TypeDocument, "pdf": TypeDocument, "paper": TypeDocument, "text": TypeDocument, "essay": TypeDocument, "poem": TypeDocument,
	"painting": TypeImage, "picture": TypeImage, "photo": TypeImage, "photograph": TypeImage, "image": TypeImage, "drawing": TypeImage, "poster": TypeImage, "print": TypeImage,
}

var leadingHint = regexp.MustCompile(`^(?i)(?:the|a|an)?\s*([a-z]+)\s+(?:of|called|named|titled|by the name of)?\s*(.+)$`)

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
	q, _ := parse(raw)
	return q
}

// ParseOpen interprets a raw query without a kind chosen for it: a kind
// named in the words, by a leading phrase ("the painting Starry Night") or
// a trailing word ("Sintel movie"), is read; a query that names none is
// Open, for a lookup across every kind. A creator after "by" is read for
// any kind, since a film, a recording, a picture and a book are all named
// that way.
func ParseOpen(raw string) ObjectQuery {
	trimmed := strings.TrimSpace(raw)
	kind := ""
	if match := leadingHint.FindStringSubmatch(trimmed); match != nil {
		if hinted, ok := leadingHints[strings.ToLower(match[1])]; ok && strings.TrimSpace(match[2]) != "" {
			kind, trimmed = hinted, strings.TrimSpace(match[2])
		}
	}
	q, hinted := parse(trimmed)
	q.Raw = strings.TrimSpace(raw)
	if q.URL != "" || q.Hash != "" {
		return q
	}
	switch {
	case kind != "":
		q.Type = kind
	case !hinted:
		q.Open = true
	}
	if title, creator := SplitPerformer(q.Title); creator != "" {
		q.Title, q.TitleKey = title, NormalizeTitle(title)
		q.Creator, q.CreatorKey = creator, NormalizeTitle(creator)
	}
	return q
}

// parse is Parse, also saying whether a trailing type word named the kind.
func parse(raw string) (ObjectQuery, bool) {
	hinted := false
	q := ObjectQuery{Raw: strings.TrimSpace(raw), Type: TypeVideo}
	if q.Raw == "" {
		return q, false
	}
	if u, err := url.Parse(q.Raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		q.URL = u.String()
		return q, false
	}
	if n := len(q.Raw); (n == 32 || n == 40 || n == 64) && hexToken.MatchString(q.Raw) {
		q.Hash = strings.ToLower(q.Raw)
		return q, false
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
		if kind, ok := typeHints[last]; ok && !onlyArticles(tokens[:len(tokens)-1]) {
			q.Type = kind
			hinted = true
			tokens = tokens[:len(tokens)-1]
		}
	}
	q.Title = strings.Join(tokens, " ")
	q.TitleKey = NormalizeTitle(q.Title)
	q.RawKey = NormalizeTitle(q.Raw)
	return q, hinted
}

// ParseAs interprets a raw query as the object type the search form chose,
// which wins over a type word in the query. A URL or hash is identified as
// it is, and an unknown or empty kind leaves Parse's own reading. An audio
// query may name its performer, "Kind of Blue by Miles Davis" or "Miles
// Davis - Kind of Blue", which is read into Creator and CreatorKey; the title
// key then carries the title alone.
func ParseAs(raw, kind string) ObjectQuery {
	q := Parse(raw)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if q.URL != "" || q.Hash != "" || !KnownType(kind) {
		return q
	}
	q.Type = kind
	// A recording names its performer, a picture its maker and a document
	// its author the same way: "Starry Night by Van Gogh".
	if kind == TypeAudio || kind == TypeImage || kind == TypeDocument {
		if title, performer := SplitPerformer(q.Title); performer != "" {
			q.Title, q.TitleKey = title, NormalizeTitle(title)
			q.Creator, q.CreatorKey = performer, NormalizeTitle(performer)
		}
	}
	return q
}

// KnownType reports whether kind is one of the object types Atlas stores.
func KnownType(kind string) bool {
	switch kind {
	case TypeVideo, TypeAudio, TypeImage, TypeDocument, TypeGame, TypeSoftware, TypeLive:
		return true
	}
	return false
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

// stripMarks builds the transformer that removes diacritics. A chained
// transformer carries buffers of its own and is not safe for concurrent use,
// so every call gets a fresh one; the search pipeline normalises titles from
// several goroutines at once.
func stripMarks() transform.Transformer {
	return transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
}

// NormalizeTitle reduces a title to a comparison key: diacritics removed,
// lower case, "&" as "and", punctuation as spaces, single spaces. Articles
// are kept so that different titles do not collide.
func NormalizeTitle(title string) string {
	plain, _, err := transform.String(stripMarks(), title)
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
