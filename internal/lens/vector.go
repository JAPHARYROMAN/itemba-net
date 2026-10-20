package lens

import (
	"hash/fnv"
	"math"
	"strconv"
	"strings"
)

// Lens version two, first piece: a vector for a title, so that a title
// spelt a little differently, in another order or with an accent dropped
// still finds its object, and so that a title in another language finds
// the alias Atlas holds for it. No model is downloaded: the vector is the
// title's character bigrams and trigrams, with word boundaries marked,
// hashed into VectorDims buckets with a sign per gram and scaled to unit
// length. Two vectors are compared by cosine similarity; the same title
// scores 1, a misspelling or a word order change stays high, and
// unrelated titles sit near 0. Atlas stores the vector in the objects'
// embedding column, which the schema reserved for this, and in the aliases'.

const (
	// VectorDims is the vector length, the width the schema reserved.
	VectorDims = 384
	// SimilarTitleThreshold is the cosine similarity at or above which a
	// vector match counts as the same title in a lookup.
	SimilarTitleThreshold = 0.45
)

// TitleVector is the vector of a title, or nil for an empty one.
func TitleVector(title string) []float32 {
	key := NormalizeTitle(title)
	if key == "" {
		return nil
	}
	vector := make([]float32, VectorDims)
	for _, word := range strings.Fields(key) {
		runes := []rune(" " + word + " ")
		for n := 2; n <= 3; n++ {
			for i := 0; i+n <= len(runes); i++ {
				h := fnv.New64a()
				h.Write([]byte(string(runes[i : i+n])))
				sum := h.Sum64()
				bucket := int(sum % VectorDims)
				if (sum>>63)&1 == 0 {
					vector[bucket]++
				} else {
					vector[bucket]--
				}
			}
		}
	}
	var norm float64
	for _, v := range vector {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return nil
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vector {
		vector[i] *= scale
	}
	return vector
}

// CosineSimilarity compares two vectors; unit vectors give their dot
// product. Vectors of different lengths, or empty ones, are 0.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// VectorLiteral writes a vector the way pgvector reads one: "[v1,v2,...]".
func VectorLiteral(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', 6, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// VectorRelevance turns a similarity into a relevance: nothing under the
// threshold, and a little under the similarity above it, so an exact title
// key still outranks a vector match of the same title.
func VectorRelevance(similarity float64) float64 {
	if similarity < SimilarTitleThreshold {
		return 0
	}
	return math.Round(similarity*0.95*1000) / 1000
}
