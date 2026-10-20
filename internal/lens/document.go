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
// The fingerprint is a similarity hash of the first TextFingerprintChars
// characters: every word and every pair of neighbouring words is a feature
// hashed to 256 bits, each bit of the result is set when more features
// voted one than zero, so texts that share most of their words share most
// of their bits. Two fingerprints are compared by the share of differing
// bits, like the picture hash; at or under SameTextThreshold they are the
// same text, and unrelated texts sit near 0.5.

const (
	// FingerprintTextSimhash names the fingerprint type this file defines.
	FingerprintTextSimhash = "text-simhash-v1"
	// TextHashWords is the fingerprint's length in 32-bit words.
	TextHashWords = 8
	// TextFingerprintChars bounds the text a fingerprint covers.
	TextFingerprintChars = 24000
	// MinFingerprintWords is the fewest words a text may be fingerprinted
	// from; fewer leave too few features to vote.
	MinFingerprintWords = 40
	// SameTextThreshold is the share of differing bits at or under which two
	// fingerprints are the same text: 38 of 256. Two scans of one edition
	// differ by OCR noise in a few; different editions of one work share the
	// body and differ in the front matter, which keeps them under it too.
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

// TextFingerprint hashes a text. A text of fewer than MinFingerprintWords
// words gives no fingerprint.
func TextFingerprint(text string) Fingerprint {
	runes := []rune(text)
	if len(runes) > TextFingerprintChars {
		text = string(runes[:TextFingerprintChars])
	}
	words := TextWords(text)
	if len(words) < MinFingerprintWords {
		return Fingerprint{}
	}
	var votes [32 * TextHashWords]int
	vote := func(feature string) {
		sum := sha256.Sum256([]byte(feature))
		for bit := 0; bit < 32*TextHashWords; bit++ {
			if sum[bit/8]&(0x80>>uint(bit%8)) != 0 {
				votes[bit]++
			} else {
				votes[bit]--
			}
		}
	}
	for i, word := range words {
		vote(word)
		if i+1 < len(words) {
			vote(word + " " + words[i+1])
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
