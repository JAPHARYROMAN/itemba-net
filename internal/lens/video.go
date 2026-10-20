package lens

import (
	"image"
	"math"
	"math/bits"
)

// The video fingerprint: what the playback sample saw. The harness page
// draws the playing video onto a canvas of ImageHashColumns+1 by
// ImageHashRows cells every VideoFrameInterval seconds of media time and
// hands back each frame's grey cells; each frame is hashed as a picture is
// (ImageFingerprint), and the sequence of frame hashes, in order, is the
// fingerprint. Two sequences are compared like two acoustic fingerprints,
// at every alignment within maxVideoShift frames, by the mean share of
// differing bits over the frames that overlap; at or under
// SameMotionThreshold they are the same film. The same film from two
// hosts, at two resolutions or two encodes, differs by a few bits a frame
// and by at most one frame of alignment; different films sit near 0.5.

const (
	// FingerprintVideoFrames names the fingerprint type this file defines.
	FingerprintVideoFrames = "video-frames-v1"
	// VideoFrameInterval is the media time between sampled frames.
	VideoFrameInterval = 0.5
	// VideoFingerprintSeconds bounds the span a fingerprint covers.
	VideoFingerprintSeconds = 30
	// VideoFrameWords is the words one frame's hash takes: a picture hash.
	VideoFrameWords = ImageHashWords
	// VideoFrameCells is the cells one frame carries: the picture hash grid
	// with its extra column.
	VideoFrameCells = (ImageHashColumns + 1) * ImageHashRows
	// MinVideoFrames is the fewest frames a comparison may rest on: three
	// seconds of the beginning.
	MinVideoFrames = 6
	// maxVideoShift is how far the alignment search goes either way: four
	// seconds, enough for a logo one host kept and another cut.
	maxVideoShift = 8
	// SameMotionThreshold is the mean share of differing bits per frame at
	// or under which two sequences are the same film.
	SameMotionThreshold = 0.2
)

// VideoFrame is one sampled frame as the harness hands it over: the media
// time it was taken at and the grey level, 0 to 255, of each cell of the
// grid, row by row.
type VideoFrame struct {
	Seconds float64 `json:"t"`
	Cells   []int   `json:"cells"`
}

// VideoFingerprint hashes a sequence of sampled frames. Frames are placed
// by the interval slot of their time; a second frame in a slot is dropped,
// and a slot the capture missed repeats the frame before it, so the
// sequence stays evenly spaced. The sequence stops at
// VideoFingerprintSeconds. No usable frame gives no fingerprint.
func VideoFingerprint(frames []VideoFrame) Fingerprint {
	maxFrames := int(VideoFingerprintSeconds / VideoFrameInterval)
	var data []byte
	var previous []byte
	count, lastSlot, firstSlot := 0, -1, 0
	for _, frame := range frames {
		if len(frame.Cells) != VideoFrameCells || math.IsNaN(frame.Seconds) || math.IsInf(frame.Seconds, 0) || frame.Seconds < 0 {
			continue
		}
		slot := int(frame.Seconds / VideoFrameInterval)
		if count > 0 && slot <= lastSlot {
			continue
		}
		if count == 0 {
			firstSlot = slot
		}
		if slot-firstSlot >= maxFrames {
			break
		}
		for gap := lastSlot + 1; count > 0 && gap < slot && gap-firstSlot < maxFrames; gap++ {
			data = append(data, previous...)
			count++
		}
		img := image.NewGray(image.Rect(0, 0, ImageHashColumns+1, ImageHashRows))
		for i, v := range frame.Cells {
			img.Pix[i] = uint8(min(max(v, 0), 255))
		}
		hash := ImageFingerprint(img)
		data = append(data, hash.Data...)
		previous = hash.Data
		count++
		lastSlot = slot
	}
	if count == 0 {
		return Fingerprint{}
	}
	return Fingerprint{Type: FingerprintVideoFrames, Frames: count * VideoFrameWords, Seconds: math.Round(float64(count)*VideoFrameInterval*10) / 10, Data: data}
}

// VideoFrameCount is how many frames a video fingerprint holds.
func VideoFrameCount(f Fingerprint) int {
	if f.Type != FingerprintVideoFrames {
		return 0
	}
	return f.Frames / VideoFrameWords
}

// compareFrames is the alignment search over two frame sequences: frame j
// of a is compared with frame j-shift of b, bit for bit, and the shift
// with the smallest mean share of differing bits over at least
// MinVideoFrames overlapping frames wins; a tie keeps the smaller shift.
func compareFrames(wa, wb []uint32) FingerprintComparison {
	na, nb := len(wa)/VideoFrameWords, len(wb)/VideoFrameWords
	best := FingerprintComparison{Distance: 1}
	for shift := -maxVideoShift; shift <= maxVideoShift; shift++ {
		lo, hi := max(0, shift), min(na, nb+shift)
		overlap := hi - lo
		if overlap < MinVideoFrames {
			continue
		}
		errors := 0
		for j := lo; j < hi; j++ {
			for w := 0; w < VideoFrameWords; w++ {
				errors += bits.OnesCount32(wa[j*VideoFrameWords+w] ^ wb[(j-shift)*VideoFrameWords+w])
			}
		}
		distance := float64(errors) / float64(32*VideoFrameWords*overlap)
		if !best.Known || distance < best.Distance || distance == best.Distance && abs(shift) < abs(best.Shift) {
			best = FingerprintComparison{Known: true, Distance: distance, Overlap: overlap, Shift: shift}
		}
	}
	return best
}
