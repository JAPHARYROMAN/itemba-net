package lens

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
)

// The acoustic fingerprint. A recording's identity rests on its tags until
// something has heard it; the fingerprint is what a bounded sample heard,
// reduced to a few hundred 32-bit words that survive a different encoder, a
// different level and a little noise, and that two listings of the same
// recording share whatever their tags say.
//
// The method is the band-energy fingerprint of Haitsma and Kalker (2002),
// kept simple: the decoded beginning, mono at 11025 Hz, up to thirty
// seconds, is cut into Hann windows of 4096 samples every 1024 samples;
// each window's spectrum is summed into 33 bands spaced logarithmically
// from 300 Hz to 2000 Hz; and bit m of a frame is one when the energy
// difference between bands m and m+1 grew since the previous frame. Thirty
// two bits a frame, 10.8 frames a second. Two fingerprints are compared by
// the bit error rate over their overlap at the best alignment within three
// seconds; at or under 0.35 they are the same sound, which the original
// work and a random pair's rate of one half both leave wide room around.
//
// The harness page computes it with the Web Audio API (the page's code is
// the same algorithm in JavaScript); AcousticFingerprint here is the
// reference implementation, used to check the page and to make fixtures.
const (
	// FingerprintAcousticBands names the fingerprint type this file defines.
	// A fingerprint of another type is never compared with one of this.
	FingerprintAcousticBands = "acoustic-bands-v1"
	// FingerprintRate is the sample rate the audio is decoded or resampled to.
	FingerprintRate = 11025
	// FingerprintWindow is the analysis window in samples (371 ms).
	FingerprintWindow = 4096
	// FingerprintHop is the step between windows in samples (93 ms).
	FingerprintHop = 1024
	// FingerprintSeconds bounds the audio a fingerprint covers.
	FingerprintSeconds = 30
	// FingerprintBands is the number of bits in a frame: one fewer than the
	// 33 energy bands.
	FingerprintBands = 32
	// SameSoundThreshold is the bit error rate at or under which two
	// fingerprints are the same sound.
	SameSoundThreshold = 0.35
	// minFingerprintOverlap is the fewest frames a comparison may rest on:
	// 64 frames are 5.9 seconds and 2048 bits.
	minFingerprintOverlap = 64
	// maxFingerprintShift is how far the alignment search goes either way:
	// 32 frames are three seconds, enough for a lead-in that one edition
	// trimmed and another kept.
	maxFingerprintShift = 32
)

// FingerprintSpeeds are the speeds, as factors of the natural one, at which
// a fingerprint's family is taken beside it: masters of one recording run
// up to a few percent apart (the first side of Kind of Blue is the famous
// case), and a two percent difference both drifts the frames six apart
// over thirty seconds and moves every band's energy a third of a band,
// which scatters the bits at any alignment of the natural fingerprints.
// Stretching the frame sequence afterwards cannot undo the pitch, so the
// probe, which still has the audio, fingerprints it resampled at each of
// these speeds too, and a comparison tries the other side's family.
var FingerprintSpeeds = []float64{0.97, 0.98, 0.99, 1.01, 1.02, 1.03}

// FingerprintBandEdges are the FFT bins that bound the 33 bands: bin i of
// a 4096-point transform at 11025 Hz is 2.69 Hz, and the edges follow
// 300 Hz × (2000/300)^(i/33). Both implementations use this table rather
// than computing it, so a last-digit difference in pow can never move an
// edge; the harness page's literal is checked against it.
var FingerprintBandEdges = [FingerprintBands + 2]int{111, 118, 125, 132, 140, 149, 157, 167, 177, 187, 198, 210, 222, 235, 249, 264, 280, 296, 314, 332, 352, 373, 395, 418, 443, 469, 497, 526, 557, 590, 625, 662, 702, 743}

// Fingerprint is one fingerprint of one location: its type, its length in
// frames, the seconds of media it covers and the words themselves as bytes,
// big-endian, four to a frame, with the family taken at FingerprintSpeeds
// when the probe took one. Data travels as base64 in JSON.
type Fingerprint struct {
	Type     string               `json:"type"`
	Frames   int                  `json:"frames"`
	Seconds  float64              `json:"seconds,omitempty"`
	Data     []byte               `json:"data"`
	Variants []FingerprintVariant `json:"variants,omitempty"`
}

// FingerprintVariant is the same beginning fingerprinted at another speed:
// the audio resampled by Speed before the windows were cut, so a master
// that runs Speed times as long matches it at the natural alignment.
type FingerprintVariant struct {
	Speed  float64 `json:"speed"`
	Frames int     `json:"frames"`
	Data   []byte  `json:"data"`
}

// Valid reports whether the fingerprint is one this package can compare: a
// known type, at least one frame, four bytes a frame, a plausible span and
// variants, if any, of the same shape at speeds within a tenth.
func (f *Fingerprint) Valid() bool {
	if f == nil || f.Type != FingerprintAcousticBands || f.Frames < 1 || len(f.Data) != 4*f.Frames {
		return false
	}
	if math.IsNaN(f.Seconds) || math.IsInf(f.Seconds, 0) || f.Seconds < 0 || f.Seconds > FingerprintSeconds+1 {
		return false
	}
	for _, v := range f.Variants {
		if math.IsNaN(v.Speed) || v.Speed <= 0.9 || v.Speed >= 1.1 || v.Speed == 1 || v.Frames < 1 || len(v.Data) != 4*v.Frames {
			return false
		}
	}
	return true
}

// Words reads the frames as 32-bit words.
func (f Fingerprint) Words() []uint32 {
	return wordsOf(f.Data)
}

func wordsOf(data []byte) []uint32 {
	words := make([]uint32, 0, len(data)/4)
	for i := 0; i+4 <= len(data); i += 4 {
		words = append(words, uint32(data[i])<<24|uint32(data[i+1])<<16|uint32(data[i+2])<<8|uint32(data[i+3]))
	}
	return words
}

// ContentKey names the sound a fingerprint heard, for an object that shares
// every tag with another but sounds different: a radio edit beside the album
// cut. It is a digest of the words, so the same bytes give the same key.
func ContentKey(f Fingerprint) string {
	sum := sha256.Sum256(f.Data)
	return "fp:" + hex.EncodeToString(sum[:8])
}

// FingerprintComparison is the outcome of comparing two fingerprints. Known
// is false when they could not be compared: different types, or too little
// overlap at every alignment.
type FingerprintComparison struct {
	Known bool `json:"known"`
	// Distance is the bit error rate at the best alignment, 0 for identical
	// words and about 0.5 for unrelated sound.
	Distance float64 `json:"distance"`
	// Overlap is the number of frames compared at that alignment, Shift
	// how many frames the second fingerprint was moved to reach it, and
	// Speed the speed of the second recording relative to the first at
	// which they matched: 1 when the natural fingerprints did.
	Overlap int     `json:"overlap"`
	Shift   int     `json:"shift"`
	Speed   float64 `json:"speed,omitempty"`
}

// Same reports the same sound: a known comparison at or under the threshold.
func (c FingerprintComparison) Same() bool {
	return c.Known && c.Distance <= SameSoundThreshold
}

// OverlapSeconds is the span of audio the comparison rested on.
func (c FingerprintComparison) OverlapSeconds() float64 {
	return math.Round(float64(c.Overlap*FingerprintHop)/FingerprintRate*10) / 10
}

// CompareFingerprints finds the alignment within maxFingerprintShift frames
// at which the two fingerprints disagree least and reports the bit error
// rate there. The natural fingerprints are compared first; then the first
// against each of the second's variants, and each of the first's variants
// against the second's natural fingerprint, so a master at another speed is
// found whichever side carries a family. A tie keeps the natural speed and
// the smaller shift.
func CompareFingerprints(a, b Fingerprint) FingerprintComparison {
	if !a.Valid() || !b.Valid() || a.Type != b.Type {
		return FingerprintComparison{}
	}
	wa, wb := a.Words(), b.Words()
	best := compareWords(wa, wb)
	best.Speed = 1
	for _, v := range b.Variants {
		if c := compareWords(wa, wordsOf(v.Data)); c.Known && (!best.Known || c.Distance < best.Distance) {
			c.Speed = v.Speed
			best = c
		}
	}
	for _, v := range a.Variants {
		if c := compareWords(wordsOf(v.Data), wb); c.Known && (!best.Known || c.Distance < best.Distance) {
			c.Speed = math.Round(1000/v.Speed) / 1000
			best = c
		}
	}
	if !best.Known {
		return FingerprintComparison{}
	}
	return best
}

// compareWords is the alignment search over two word sequences.
func compareWords(wa, wb []uint32) FingerprintComparison {
	best := FingerprintComparison{Distance: 1}
	for shift := -maxFingerprintShift; shift <= maxFingerprintShift; shift++ {
		// Frame j of a is compared with frame j-shift of b.
		lo, hi := max(0, shift), min(len(wa), len(wb)+shift)
		overlap := hi - lo
		if overlap < minFingerprintOverlap {
			continue
		}
		errors := 0
		for j := lo; j < hi; j++ {
			errors += bits.OnesCount32(wa[j] ^ wb[j-shift])
		}
		distance := float64(errors) / float64(FingerprintBands*overlap)
		if !best.Known || distance < best.Distance || distance == best.Distance && abs(shift) < abs(best.Shift) {
			best = FingerprintComparison{Known: true, Distance: distance, Overlap: overlap, Shift: shift}
		}
	}
	return best
}

// AcousticFingerprintFamily is AcousticFingerprint with the variants at
// FingerprintSpeeds, as the harness page takes them: the samples resampled
// by each speed and fingerprinted again. A variant too short to hold two
// windows is left out.
func AcousticFingerprintFamily(samples []float64) Fingerprint {
	fp := AcousticFingerprint(samples)
	for _, speed := range FingerprintSpeeds {
		v := AcousticFingerprint(Resample(samples, speed))
		if v.Frames < 1 {
			continue
		}
		fp.Variants = append(fp.Variants, FingerprintVariant{Speed: speed, Frames: v.Frames, Data: v.Data})
	}
	return fp
}

// Resample plays samples at another speed by linear interpolation: a
// factor of 1.02 makes them two percent longer and a semitone's third
// lower, as a master run two percent slow would sound. The page runs the
// same steps.
func Resample(samples []float64, factor float64) []float64 {
	out := make([]float64, int(float64(len(samples))*factor))
	for i := range out {
		pos := float64(i) / factor
		j := int(pos)
		if j+1 >= len(samples) {
			break
		}
		frac := pos - float64(j)
		out[i] = samples[j]*(1-frac) + samples[j+1]*frac
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// AcousticFingerprint is the reference computation over mono samples at
// FingerprintRate in the range -1 to 1. Fewer than two windows of audio give
// a fingerprint with no frames, which is not Valid.
func AcousticFingerprint(samples []float64) Fingerprint {
	if len(samples) > FingerprintSeconds*FingerprintRate {
		samples = samples[:FingerprintSeconds*FingerprintRate]
	}
	fp := Fingerprint{Type: FingerprintAcousticBands, Seconds: math.Round(float64(len(samples))/FingerprintRate*100) / 100}
	frames := 0
	if len(samples) >= FingerprintWindow {
		frames = (len(samples)-FingerprintWindow)/FingerprintHop + 1
	}
	if frames < 2 {
		return fp
	}
	window := make([]float64, FingerprintWindow)
	for i := range window {
		window[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/FingerprintWindow))
	}
	re, im := make([]float64, FingerprintWindow), make([]float64, FingerprintWindow)
	previous := make([]float64, FingerprintBands+1)
	current := make([]float64, FingerprintBands+1)
	fp.Data = make([]byte, 0, 4*(frames-1))
	for n := 0; n < frames; n++ {
		start := n * FingerprintHop
		for i := 0; i < FingerprintWindow; i++ {
			re[i], im[i] = samples[start+i]*window[i], 0
		}
		fft(re, im)
		for m := 0; m <= FingerprintBands; m++ {
			energy := 0.0
			for k := FingerprintBandEdges[m]; k < FingerprintBandEdges[m+1]; k++ {
				energy += re[k]*re[k] + im[k]*im[k]
			}
			current[m] = energy
		}
		if n > 0 {
			var word uint32
			for m := 0; m < FingerprintBands; m++ {
				if (current[m]-current[m+1])-(previous[m]-previous[m+1]) > 0 {
					word |= 1 << (31 - m)
				}
			}
			fp.Data = append(fp.Data, byte(word>>24), byte(word>>16), byte(word>>8), byte(word))
		}
		previous, current = current, previous
	}
	fp.Frames = frames - 1
	return fp
}

// fft is an in-place iterative radix-2 transform; len(re) must be a power
// of two. The page runs the same steps in the same order.
func fft(re, im []float64) {
	n := len(re)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		step := -2 * math.Pi / float64(size)
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				angle := step * float64(k)
				wr, wi := math.Cos(angle), math.Sin(angle)
				a, b := start+k, start+k+half
				tr := re[b]*wr - im[b]*wi
				ti := re[b]*wi + im[b]*wr
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
			}
		}
	}
}

// IdentityMatch says what a fingerprint comparison meant for a location:
// the nearest fingerprint it was compared with, how far, and the effect on
// identity. Verdict is same or different; Effect is confirmed (the same
// sound as another location of the same object), joined (the same sound
// as a location of another object, which it therefore joined under its own
// tags), kept_apart (a different sound from every location of the object
// its tags name, so it became an object of its own) or empty (a different
// sound from a different recording, which tells nothing new).
type IdentityMatch struct {
	Method    string  `json:"method"`
	Verdict   string  `json:"verdict"`
	Effect    string  `json:"effect,omitempty"`
	PeerURL   string  `json:"peerUrl,omitempty"`
	PeerTitle string  `json:"peerTitle,omitempty"`
	Distance  float64 `json:"distance"`
	Seconds   float64 `json:"seconds,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

// The verdicts and effects an IdentityMatch carries.
const (
	VerdictSame       = "same"
	VerdictDifferent  = "different"
	EffectConfirmed   = "confirmed"
	EffectJoined      = "joined"
	EffectKeptApart   = "kept_apart"
	MethodFingerprint = "acoustic_fingerprint"
)

// IdentityDetail says a match in words, the same words wherever it is
// shown: the search's cards, the index's ranked list and the evidence.
func IdentityDetail(m IdentityMatch) string {
	peer := m.PeerTitle
	if peer == "" {
		peer = m.PeerURL
	}
	if peer == "" {
		return "Fingerprinted; nothing else of its kind has been heard yet, so there is nothing to compare it with."
	}
	measure := fmt.Sprintf("bit error rate %.2f over %.0f s of the beginning", m.Distance, m.Seconds)
	switch {
	case m.Verdict == VerdictSame && m.Effect == EffectJoined:
		return fmt.Sprintf("Sounds the same as %s (%s): the same recording under different tags.", peer, measure)
	case m.Verdict == VerdictSame && m.Effect == EffectConfirmed:
		return fmt.Sprintf("Sounds the same as %s (%s): the same recording, as the tags say.", peer, measure)
	case m.Verdict == VerdictSame && m.Effect == EffectKeptApart:
		return fmt.Sprintf("Sounds the same as %s at the start (%s), but the lengths differ: an edit of the same recording, kept apart.", peer, measure)
	case m.Verdict == VerdictSame:
		return fmt.Sprintf("Sounds the same as %s at the start (%s), but the lengths differ: an edit of the same recording.", peer, measure)
	case m.Verdict == VerdictDifferent && m.Effect == EffectKeptApart:
		return fmt.Sprintf("Sounds different from %s (%s) although the tags agree: a different recording, kept apart.", peer, measure)
	case m.Verdict == VerdictDifferent:
		return fmt.Sprintf("Sounds different from %s (%s), the nearest fingerprint compared: a different recording, as the tags say.", peer, measure)
	}
	return ""
}
