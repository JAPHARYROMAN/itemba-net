package lens

import (
	"image"
	"strings"
)

// Image identity. A picture is what its tags say it is, a title, a maker
// and a year, until something has seen it; what it looks like is a
// perceptual hash, which two listings of the same picture share across
// sizes, encoders and small edits, whatever their file names say.
//
// The hash is a difference hash: the picture in grey, box-sampled to a grid
// of ImageHashColumns+1 by ImageHashRows cells, and bit (row, column) set
// when the cell is brighter than the one to its right. Sixteen rows of
// sixteen bits give 256 bits, eight 32-bit words, which the fingerprint
// library stores like the acoustic words. Two hashes are compared by the
// share of bits that differ: 0 for the same picture, about 0.5 for unrelated
// ones; at or under SameSightThreshold they are the same picture.

const (
	// FingerprintImageDHash names the fingerprint type this file defines.
	FingerprintImageDHash = "image-dhash-v1"
	// ImageHashRows and ImageHashColumns are the grid of gradients.
	ImageHashRows    = 16
	ImageHashColumns = 16
	// ImageHashWords is the hash's length in 32-bit words.
	ImageHashWords = ImageHashRows * ImageHashColumns / 32
	// SameSightThreshold is the share of differing bits at or under which
	// two hashes are the same picture: 30 of 256. The same picture at
	// another size or compression differs by a handful; unrelated pictures
	// by about half.
	SameSightThreshold = 0.12
	// HostMatchRelevance is the least a listing the host's own search
	// returned can score: the host matched it by its description or its
	// categories, which the file name may not show.
	HostMatchRelevance = 0.25
)

// ImageFingerprint hashes a decoded picture. A picture smaller than the
// grid still hashes, with cells sharing pixels; an empty one gives no hash.
func ImageFingerprint(img image.Image) Fingerprint {
	if img == nil {
		return Fingerprint{}
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 1 || height < 1 {
		return Fingerprint{}
	}
	columns, rows := ImageHashColumns+1, ImageHashRows
	sums := make([]float64, columns*rows)
	counts := make([]int, columns*rows)
	for y := 0; y < height; y++ {
		row := y * rows / height
		for x := 0; x < width; x++ {
			cell := row*columns + x*columns/width
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			sums[cell] += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
			counts[cell]++
		}
	}
	means := make([]float64, columns*rows)
	for i := range sums {
		if counts[i] > 0 {
			means[i] = sums[i] / float64(counts[i])
		}
	}
	words := make([]uint32, ImageHashWords)
	for row := 0; row < rows; row++ {
		for column := 0; column < ImageHashColumns; column++ {
			if means[row*columns+column] > means[row*columns+column+1] {
				bit := row*ImageHashColumns + column
				words[bit/32] |= 1 << (31 - uint(bit%32))
			}
		}
	}
	data := make([]byte, 0, 4*len(words))
	for _, w := range words {
		data = append(data, byte(w>>24), byte(w>>16), byte(w>>8), byte(w))
	}
	return Fingerprint{Type: FingerprintImageDHash, Frames: ImageHashWords, Data: data}
}

// ImageIdentity is a picture as far as its tags and, when a probe saw it,
// its hash say. An empty maker, a zero year or dimension, or a missing hash
// is unknown, which never contradicts a known value.
type ImageIdentity struct {
	Title       string       `json:"title"`
	TitleKey    string       `json:"titleKey"`
	Creator     string       `json:"creator,omitempty"`
	CreatorKey  string       `json:"creatorKey,omitempty"`
	Year        int          `json:"year,omitempty"`
	Width       int          `json:"width,omitempty"`
	Height      int          `json:"height,omitempty"`
	Fingerprint *Fingerprint `json:"fingerprint,omitempty"`
}

// ImageListing reads a host's tags into an identity: the file's title with
// its release label separated as for any listing, the credited maker, and
// the dimensions when the host states them.
func ImageListing(title, creator string, year, width, height int) ImageIdentity {
	base, year := ListedTitle(title, year)
	creator = strings.TrimSpace(creator)
	return ImageIdentity{
		Title:      base,
		TitleKey:   NormalizeTitle(base),
		Creator:    creator,
		CreatorKey: NormalizeTitle(creator),
		Year:       year,
		Width:      width,
		Height:     height,
	}
}

// ImageRelevance scores how well a listing answers a picture query, from
// HostMatchRelevance to 1: the title relevance, with the host's own match
// as the floor because a file name rarely says what a picture shows, halved
// when both name different makers and lowered when both name different
// years.
func ImageRelevance(q ObjectQuery, id ImageIdentity) float64 {
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

// ImageMatch is MatchImages' answer: whether two identities name the same
// picture and on what basis, fingerprint when both carried a hash and tags
// otherwise, with the hashes' distance when they were compared.
type ImageMatch struct {
	Same     bool    `json:"same"`
	Basis    string  `json:"basis"`
	Distance float64 `json:"distance,omitempty"`
}

// MatchImages decides whether two identities name the same picture. When
// both carry a hash the sight decides, whatever the tags say; otherwise the
// same title with makers that agree when both are known is the same
// picture.
func MatchImages(a, b ImageIdentity) ImageMatch {
	if a.Fingerprint != nil && b.Fingerprint != nil {
		if c := CompareFingerprints(*a.Fingerprint, *b.Fingerprint); c.Known {
			return ImageMatch{Same: c.Same(), Basis: "fingerprint", Distance: c.Distance}
		}
	}
	match := ImageMatch{Basis: "tags"}
	if a.TitleKey == "" || a.TitleKey != b.TitleKey {
		return match
	}
	if a.CreatorKey != "" && b.CreatorKey != "" && !CreatorsAgree(a.CreatorKey, b.CreatorKey) {
		return match
	}
	match.Same = true
	return match
}
