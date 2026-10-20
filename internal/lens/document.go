package lens

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"unicode"
)

// Document identity. A document is what its tags say it is, a title, an
// author, a year and a page count, until something has read it; what it
// says is a text fingerprint, which two copies of the same text share
// across scans, encodings and editions, whatever their file names say.
//
// The fingerprint covers the body of the text, not its beginning: a scanned
// book opens with endpapers whose OCR is noise, a publisher's catalogue, a
// title page and a table of contents, and two scans of one work differ
// most there. The body is found by reading the words in windows of
// textWindow and taking the first run of textBodyWindows windows that read
// as prose: mostly words of three letters or more, few in capitals, few
// with digits. From there TextFingerprintWords words are hashed: every
// word and every pair of neighbouring words is a feature hashed to 256
// bits, weighted by how often it occurs, and each bit of the result is set
// when more features voted one than zero, so texts that share most of
// their words share most of their bits. Two fingerprints are compared by
// the share of differing bits, like the picture hash; at or under
// SameTextThreshold they are the same text. Weighting features by their
// count keeps the common words' votes large, which is what makes the hash
// survive OCR noise and the differences between editions (measured on
// Archive scans: the same work at 0.02 to 0.11), at the price of unrelated
// English prose sitting near 0.25 rather than 0.5, since every English
// text shares its function words.

const (
	// FingerprintTextSimhash names the fingerprint type this file defines.
	// Version two hashes the body found as above; version one hashed the
	// first 24,000 characters and is not compared with it.
	FingerprintTextSimhash = "text-simhash-v2"
	// TextHashWords is the fingerprint's length in 32-bit words.
	TextHashWords = 8
	// TextFingerprintWords bounds the words a fingerprint covers, counted
	// from the body's start.
	TextFingerprintWords = 6000
	// MinFingerprintWords is the fewest words a body may be fingerprinted
	// from; fewer leave too few features to vote.
	MinFingerprintWords = 40
	// textWindow is the run of words whose statistics say whether they
	// read as prose, and textBodyWindows how many such runs in a row begin
	// the body. Windows past the end of the text count as prose, so a short
	// text that reads as prose is its own body.
	textWindow      = 100
	textBodyWindows = 3
	// SameTextThreshold is the share of differing bits at or under which two
	// fingerprints are the same text: 38 of 256.
	SameTextThreshold = 0.15
)

var (
	lifeDates   = regexp.MustCompile(`,?\s*\(?\d{3,4}\s*-\s*(\d{3,4})?\)?\.?\s*$`)
	textWordRun = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// TextWords splits a text into lower-case words of letters and digits.
func TextWords(text string) []string {
	return textWordRun.FindAllString(strings.ToLower(text), -1)
}

// textTokens splits a text into words as written, case kept.
func textTokens(text string) []string {
	return textWordRun.FindAllString(text, -1)
}

// readsAsProse says whether a run of words reads as the body of a text
// rather than as OCR noise, a catalogue or a title page: at least 55 in a
// hundred are words of three or more letters, at most 25 are in capitals
// and at most 10 carry a digit.
func readsAsProse(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	long, capitals, digits := 0, 0, 0
	for _, token := range tokens {
		letters, upper, digit := 0, 0, false
		for _, r := range token {
			switch {
			case unicode.IsDigit(r):
				digit = true
			case unicode.IsLetter(r):
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
		runes := len([]rune(token))
		if digit {
			digits++
		} else if letters >= 3 {
			long++
		}
		if runes >= 2 && upper == runes {
			capitals++
		}
	}
	n := len(tokens)
	return long*100 >= 55*n && capitals*100 <= 25*n && digits*100 <= 10*n
}

// TextBody finds the body of a text and returns its first
// TextFingerprintWords words, lower-cased, with the number of words skipped
// before it. A text with no body, such as a scan whose OCR is noise
// throughout, gives no words.
func TextBody(text string) (words []string, skipped int) {
	tokens := textTokens(text)
	windows := (len(tokens) + textWindow - 1) / textWindow
	prose := make([]bool, windows)
	for i := range prose {
		end := min((i+1)*textWindow, len(tokens))
		window := tokens[i*textWindow : end]
		// A last window of a few words is too short to judge; it reads as
		// the words before it did.
		if len(window) < textWindow/5 {
			prose[i] = i == 0 || prose[i-1]
			continue
		}
		prose[i] = readsAsProse(window)
	}
	start := -1
	for i := range prose {
		run := true
		for j := i; j < i+textBodyWindows && j < windows; j++ {
			if !prose[j] {
				run = false
				break
			}
		}
		if run {
			start = i * textWindow
			break
		}
	}
	if start < 0 {
		return nil, len(tokens)
	}
	end := min(start+TextFingerprintWords, len(tokens))
	words = make([]string, 0, end-start)
	for _, token := range tokens[start:end] {
		words = append(words, strings.ToLower(token))
	}
	return words, start
}

// TextFingerprint hashes the body of a text. A text whose body has fewer
// than MinFingerprintWords words, or no body at all, gives no fingerprint.
func TextFingerprint(text string) Fingerprint {
	words, _ := TextBody(text)
	return fingerprintWords(words)
}

// fingerprintWords hashes a run of words, every word and every pair of
// neighbours voting as often as it occurs.
func fingerprintWords(words []string) Fingerprint {
	if len(words) < MinFingerprintWords {
		return Fingerprint{}
	}
	counts := make(map[string]int, 2*len(words))
	for i, word := range words {
		counts[word]++
		if i+1 < len(words) {
			counts[word+" "+words[i+1]]++
		}
	}
	var votes [32 * TextHashWords]int
	for feature, count := range counts {
		sum := sha256.Sum256([]byte(feature))
		for bit := 0; bit < 32*TextHashWords; bit++ {
			if sum[bit/8]&(0x80>>uint(bit%8)) != 0 {
				votes[bit] += count
			} else {
				votes[bit] -= count
			}
		}
	}
	data := make([]byte, 4*TextHashWords)
	for bit, v := range votes {
		if v > 0 {
			data[bit/8] |= 0x80 >> uint(bit%8)
		}
	}
	return Fingerprint{Type: FingerprintTextSimhash, Frames: TextHashWords, Data: data}
}

// NormalizeAuthor reads a catalogue credit as a name: "Carroll, Lewis,
// 1832-1898" is Lewis Carroll. Life dates go, and a single "Surname, Given"
// turns around; anything else is kept as written.
func NormalizeAuthor(credit string) string {
	name := strings.TrimSpace(credit)
	for {
		stripped := strings.TrimSpace(lifeDates.ReplaceAllString(name, ""))
		if stripped == name {
			break
		}
		name = stripped
	}
	name = strings.TrimRight(name, ", ")
	if parts := strings.Split(name, ","); len(parts) == 2 {
		surname, given := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if surname != "" && given != "" && !strings.ContainsFunc(given, unicode.IsDigit) {
			name = given + " " + surname
		}
	}
	return name
}

// DocumentIdentity is a document as far as its tags and, when a probe read
// it, its text say. An empty author, a zero year or page count, or a
// missing fingerprint is unknown, which never contradicts a known value.
type DocumentIdentity struct {
	Title       string       `json:"title"`
	TitleKey    string       `json:"titleKey"`
	Creator     string       `json:"creator,omitempty"`
	CreatorKey  string       `json:"creatorKey,omitempty"`
	Year        int          `json:"year,omitempty"`
	Pages       int          `json:"pages,omitempty"`
	Fingerprint *Fingerprint `json:"fingerprint,omitempty"`
}

// DocumentListing reads a host's tags into an identity: the title with its
// release label separated as for any listing, the credited author as a
// name, the year and the page count the host states.
func DocumentListing(title, author string, year, pages int) DocumentIdentity {
	base, year := ListedTitle(title, year)
	name := NormalizeAuthor(author)
	return DocumentIdentity{
		Title:      base,
		TitleKey:   NormalizeTitle(base),
		Creator:    name,
		CreatorKey: NormalizeTitle(name),
		Year:       year,
		Pages:      pages,
	}
}

// DocumentRelevance scores how well a listing answers a document query,
// from HostMatchRelevance to 1: the title relevance with the host's own
// match as the floor, halved when both name different authors and lowered
// when both name different years.
func DocumentRelevance(q ObjectQuery, id DocumentIdentity) float64 {
	score := TitleRelevance(q.TitleKey, id.TitleKey)
	if score < HostMatchRelevance {
		score = HostMatchRelevance
	}
	if q.CreatorKey != "" && id.CreatorKey != "" && !CreatorsAgree(q.CreatorKey, id.CreatorKey) {
		score *= 0.5
	}
	if q.Year != 0 && id.Year != 0 && q.Year != id.Year {
		score *= 0.8
	}
	return score
}

// DocumentMatch is MatchDocuments' answer: whether two identities name the
// same document and on what basis, fingerprint when both carried one and
// tags otherwise, with the fingerprints' distance when they were compared.
type DocumentMatch struct {
	Same     bool    `json:"same"`
	Basis    string  `json:"basis"`
	Distance float64 `json:"distance,omitempty"`
}

// MatchDocuments decides whether two identities name the same document.
// When both carry a fingerprint the text decides, whatever the tags say;
// otherwise the same title with authors that agree when both are known is
// the same document.
func MatchDocuments(a, b DocumentIdentity) DocumentMatch {
	if a.Fingerprint != nil && b.Fingerprint != nil {
		if c := CompareFingerprints(*a.Fingerprint, *b.Fingerprint); c.Known {
			return DocumentMatch{Same: c.Same(), Basis: "fingerprint", Distance: c.Distance}
		}
	}
	match := DocumentMatch{Basis: "tags"}
	if a.TitleKey == "" || a.TitleKey != b.TitleKey {
		return match
	}
	if a.CreatorKey != "" && b.CreatorKey != "" && !CreatorsAgree(a.CreatorKey, b.CreatorKey) {
		return match
	}
	match.Same = true
	return match
}
