package scout

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itemba-net/core/internal/lens"
)

// sampleFrame is the grey cells of one frame of a synthetic film whose
// pattern moves with the frame number.
func sampleFrame(frame int) []int {
	cells := make([]int, lens.VideoFrameCells)
	columns := lens.ImageHashColumns + 1
	for y := 0; y < lens.ImageHashRows; y++ {
		for x := 0; x < columns; x++ {
			phase := float64(frame) * 0.3
			cells[y*columns+x] = int(math.Round(128 + 100*math.Sin(float64(x)/float64(columns)*6+phase)*math.Cos(float64(y)/float64(lens.ImageHashRows)*5+phase*0.7)))
		}
	}
	return cells
}

func sampleFrames(n int) []lens.VideoFrame {
	frames := make([]lens.VideoFrame, 0, n)
	for i := 0; i < n; i++ {
		frames = append(frames, lens.VideoFrame{Seconds: float64(i)*lens.VideoFrameInterval + 0.1, Cells: sampleFrame(i)})
	}
	return frames
}

func TestVideoHarnessPageCapturesFramesForTheFingerprint(t *testing.T) {
	page := string(moviePlaybackHTML("abc", false, false))
	for _, want := range []string{
		"COLUMNS=" + strconv.Itoa(lens.ImageHashColumns+1) + ",ROWS=" + strconv.Itoa(lens.ImageHashRows) + ",INTERVAL=" + strconv.FormatFloat(lens.VideoFrameInterval, 'f', -1, 64),
		"MAX_FRAMES=" + strconv.Itoa(int(lens.VideoFingerprintSeconds/lens.VideoFrameInterval)),
		"cells.drawImage(video,0,0,COLUMNS,ROWS)",
		"window.__itembaFrames.push({t:t,cells:grey})",
		"video.addEventListener('timeupdate',capture);window.__itembaCaptureFrame=capture;",
		"willReadFrequently:true",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the video page captures frames for the fingerprint; missing %q", want)
		}
	}
	if !strings.Contains(string(moviePlaybackHTML("abc", true, true)), "__itembaCaptureFrame") {
		t.Fatal("the HLS page captures frames too")
	}
	if !strings.Contains(freePlaybackSnapshotJS, "window.__itembaCaptureFrame()") || !strings.Contains(freePlaybackSnapshotJS, "frames:window.__itembaFrames || []") {
		t.Fatal("the sampler captures a frame on every reading and reads the frames back")
	}
	if strings.Contains(audioPlaybackSnapshotJS, "__itembaFrames") || strings.Contains(string(audioPlaybackHTML("abc")), "__itembaFrames") {
		t.Fatal("the audio page captures no frames")
	}
}

func TestVideoSamplerBuildsTheFrameFingerprint(t *testing.T) {
	previousGrace, previousFrames := videoFrameGrace, videoFingerprintFrames
	videoFrameGrace, videoFingerprintFrames = 2*time.Second, 9
	defer func() { videoFrameGrace, videoFingerprintFrames = previousGrace, previousFrames }()
	origin := "http://127.0.0.1:1234"
	frames := sampleFrames(12)
	browser := &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: playbackStateFixture(origin), evolve: func(s *playbackSnapshot, evaluation int) {
		s.Frames = frames[:min(evaluation, len(frames))]
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	result := runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if !result.PlaybackConfirmed || result.FingerprintStatus != "computed" || result.Fingerprint == nil || result.Fingerprint.Type != lens.FingerprintVideoFrames {
		t.Fatalf("a confirmed video sample carries the frame fingerprint: %+v %+v", result, result.Fingerprint)
	}
	if n := lens.VideoFrameCount(*result.Fingerprint); n < 9 || browser.evaluations < 9 {
		t.Fatalf("the sample played on until it had enough frames: %d frames after %d evaluations", n, browser.evaluations)
	}
	if c := lens.CompareFingerprints(*result.Fingerprint, lens.VideoFingerprint(frames)); !c.Known || c.Distance != 0 {
		t.Fatalf("the fingerprint is the hash of the frames the page captured: %+v", c)
	}
	if result.DecodedFrames == 0 || result.Width == 0 {
		t.Fatalf("the sample still reports what it decoded: %+v", result)
	}

	// Too few frames by the end of the grace: no fingerprint, said so.
	browser = &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: playbackStateFixture(origin), evolve: func(s *playbackSnapshot, _ int) {
		s.Frames = frames[:3]
	}}
	ctx, cancel = context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	started := time.Now()
	result = runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if !result.PlaybackConfirmed || result.FingerprintStatus != "too_short" || result.Fingerprint != nil || time.Since(started) > 5*time.Second {
		t.Fatalf("too few frames give no fingerprint after the grace: %+v in %s", result, time.Since(started))
	}

	// A page that could not read the frames at all.
	browser = &fakePlaybackBrowser{origin: origin, mediaResponse: true, state: playbackStateFixture(origin), evolve: func(s *playbackSnapshot, _ int) {
		s.FrameError = "Tainted canvases may not be exported."
	}}
	ctx, cancel = context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	result = runFreePlaybackSample(ctx, browser, freePlaybackFixture(), origin, &PlaybackCheck{})
	if !result.PlaybackConfirmed || result.FingerprintStatus != "unsupported" || result.Fingerprint != nil {
		t.Fatalf("a capture the page could not make is unsupported: %+v", result)
	}
}
