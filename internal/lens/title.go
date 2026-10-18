package lens

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// YearPattern finds four-digit release years from 1800 to 2099 in free text.
var YearPattern = regexp.MustCompile(`\b(18\d{2}|19\d{2}|20\d{2})\b`)

// Words splits a title into lower-case alphanumeric words.
func Words(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// SubstantiveWords is Words without the articles and connectives that carry
// no identity: a, an, the, of, and.
func SubstantiveWords(value string) []string {
	var words []string
	for _, word := range Words(value) {
		switch word {
		case "a", "an", "the", "of", "and":
			continue
		}
		words = append(words, word)
	}
	return words
}

// TrimSearchWords removes the phrasing people add around a title when they
// search, such as a leading "watch" or a trailing "full movie online".
func TrimSearchWords(query string) string {
	query = strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToLower(query), "watch ") {
		query = strings.TrimSpace(query[6:])
	}
	for _, suffix := range []string{" full movie online", " full movie", " watch online", " movie online", " online", " movie"} {
		if strings.HasSuffix(strings.ToLower(query), suffix) && len(query) > len(suffix) {
			query = strings.TrimSpace(query[:len(query)-len(suffix)])
			break
		}
	}
	return query
}

// CanonicalQuery reduces a search to its title and an optional trailing
// release year. A year is only taken when it ends the query and something
// precedes it, so "2001" stays a title and "Sintel 2010" splits.
func CanonicalQuery(query string) (string, int) {
	query = TrimSearchWords(query)
	year := 0
	if matches := YearPattern.FindAllStringIndex(query, -1); len(matches) > 0 {
		last := matches[len(matches)-1]
		prefix := strings.TrimSpace(strings.TrimRight(query[:last[0]], " (["))
		suffix := strings.TrimSpace(strings.Trim(query[last[1]:], ") ]"))
		parsed, _ := strconv.Atoi(query[last[0]:last[1]])
		if prefix != "" && suffix == "" && parsed <= time.Now().Year()+1 {
			year, query = parsed, prefix
		}
	}
	return TrimSearchWords(query), year
}

// TitleDetails separates a listed title from its release label. The 2001 in
// a film's name must not become its release year, while a (1968) label
// should. A known release year wins over anything parsed from the title.
func TitleDetails(title string, releaseYear int) (string, int) {
	base := strings.TrimSpace(title)
	metadata := ""
	if i := strings.IndexAny(base, "(["); i > 0 {
		metadata, base = base[i:], strings.TrimSpace(base[:i])
	}
	year := releaseYear
	if year == 0 {
		if found := YearPattern.FindAllString(metadata, -1); len(found) > 0 {
			parsed, _ := strconv.Atoi(found[len(found)-1])
			if parsed <= time.Now().Year()+1 {
				year = parsed
			}
		}
	}
	canonical, trailingYear := CanonicalQuery(base)
	if year == 0 {
		year = trailingYear
	}
	return canonical, year
}

// ListedTitle reads a title as a host listed it: the release label in
// parentheses or brackets and a trailing year are separated out, but nothing
// else is interpreted. Unlike Parse, it never treats a trailing word such as
// film or movie as a type hint, because "Disaster Film" is a title. A known
// release year wins over anything read from the title.
func ListedTitle(title string, releaseYear int) (string, int) {
	base := strings.TrimSpace(title)
	metadata := ""
	if i := strings.IndexAny(base, "(["); i > 0 {
		metadata, base = base[i:], strings.TrimSpace(base[:i])
	}
	maxYear := time.Now().Year() + 1
	year := releaseYear
	if year == 0 {
		if found := YearPattern.FindAllString(metadata, -1); len(found) > 0 {
			if parsed, _ := strconv.Atoi(found[len(found)-1]); parsed <= maxYear {
				year = parsed
			}
		}
	}
	if tokens := strings.Fields(base); len(tokens) > 1 {
		if match := yearToken.FindStringSubmatch(tokens[len(tokens)-1]); match != nil {
			if parsed, _ := strconv.Atoi(match[1]); parsed <= maxYear {
				if year == 0 {
					year = parsed
				}
				base = strings.Join(tokens[:len(tokens)-1], " ")
			}
		}
	}
	return strings.TrimSpace(base), year
}

// derivativeGroups name related videos that are separate search intents.
var derivativeGroups = [][]string{
	{"trailer", "trailers", "teaser", "teasers"},
	{"reveal", "reveals", "announcement", "announcements", "fan made", "fanmade"},
	{"review", "reviews"},
	{"reaction", "reactions"},
	{"explained", "explanation"},
	{"clip", "clips"},
	{"soundtrack", "soundtracks", "score", "scores"},
	{"news"},
	{"opening", "closing", "openingclosing"},
	{"intermission"},
	{"behind the scenes", "behind scenes", "behindscenes", "making of"},
	{"analysis", "analyses", "breakdown", "hidden meaning", "things to know"},
	{"interview", "interviews", "commentary"},
	{"screenbug", "up next", "upnext"},
}

// HasUnrequestedDerivative reports whether a candidate title names a related
// video (a trailer, review, clip, soundtrack and so on) that the query did
// not ask for. It looks at the complete candidate title, including metadata
// that canonical matching strips.
func HasUnrequestedDerivative(query, candidate string) bool {
	query = " " + strings.Join(Words(query), " ") + " "
	candidate = " " + strings.Join(Words(candidate), " ") + " "
	for _, group := range derivativeGroups {
		found, requested := false, false
		for _, phrase := range group {
			found = found || strings.Contains(candidate, " "+phrase+" ")
			requested = requested || strings.Contains(query, " "+phrase+" ")
		}
		if found && !requested {
			return true
		}
	}
	return false
}

// FileYearConflicts reports whether a media file name carries a year that is
// neither the release year nor part of the title, which contradicts a listing
// that claims the file holds that film.
func FileYearConflicts(name, title string, releaseYear int) bool {
	if releaseYear == 0 {
		return false
	}
	titleYears := map[string]bool{}
	for _, year := range YearPattern.FindAllString(title, -1) {
		titleYears[year] = true
	}
	label := strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	for _, year := range YearPattern.FindAllString(label, -1) {
		parsed, _ := strconv.Atoi(year)
		if parsed != releaseYear && !titleYears[year] {
			return true
		}
	}
	return false
}

// SameFeatureTitle reports whether two listed titles name the same feature
// once release labels are removed, and the second is not a derivative of the
// first. A page can add "Full Movie Review" after its year label and still
// embed the feature; its canonical title must still match exactly.
func SameFeatureTitle(left, right string) bool {
	a, _ := TitleDetails(left, 0)
	b, _ := TitleDetails(right, 0)
	return a != "" && !HasUnrequestedDerivative(a, b) && strings.Join(Words(a), " ") == strings.Join(Words(b), " ")
}
