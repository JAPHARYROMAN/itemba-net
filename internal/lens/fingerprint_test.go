package lens

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

// tune synthesises a recording: a deterministic melody of overlapping
// partials whose pitches change every 400 ms, so each window has its own
// band energies. seed picks the melody; seconds its length.
func tune(seed int64, seconds float64) []float64 {
	r := rand.New(rand.NewSource(seed))
	n := int(seconds * FingerprintRate)
	samples := make([]float64, n)
	noteLength := int(0.4 * FingerprintRate)
	notes := n/noteLength + 1
	pitches := make([][3]float64, notes)
	for i := range pitches {
		base := 220 * math.Pow(2, float64(r.Intn(24))/12)
		pitches[i] = [3]float64{base, base * 1.5 * math.Pow(2, float64(r.Intn(3)-1)/12), base * 2.4}
	}
	for i := range samples {
		note := pitches[i/noteLength]
		t := float64(i) / FingerprintRate
		samples[i] = 0.3*math.Sin(2*math.Pi*note[0]*t) + 0.2*math.Sin(2*math.Pi*note[1]*t) + 0.1*math.Sin(2*math.Pi*note[2]*t)
	}
	return samples
}

// edition returns the tune as another host might publish it: a lead-in of
// silence, a different level and noise at the given amplitude.
func edition(samples []float64, leadIn float64, gain, noise float64, seed int64) []float64 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float64, int(leadIn*FingerprintRate), int(leadIn*FingerprintRate)+len(samples))
	for _, s := range samples {
		out = append(out, s*gain+noise*(2*r.Float64()-1))
	}
	return out
}

func TestAcousticFingerprintCoversTheBeginningInFrames(t *testing.T) {
	fp := AcousticFingerprint(tune(1, 40))
	if !fp.Valid() || fp.Type != FingerprintAcousticBands || fp.Seconds != 30 {
		t.Fatalf("a fingerprint covers at most thirty seconds: %+v", fp)
	}
	// 30 s at 11025 Hz is 330750 samples: (330750-4096)/1024+1 windows, one
	// fewer frames since the first window has no predecessor.
	if want := (330750-4096)/1024 + 1 - 1; fp.Frames != want || len(fp.Data) != 4*want {
		t.Fatalf("frames = %d (%d bytes), want %d", fp.Frames, len(fp.Data), want)
	}
	if short := AcousticFingerprint(tune(1, 0.4)); short.Valid() || short.Frames != 0 {
		t.Fatalf("less than two windows is no fingerprint: %+v", short)
	}
	if again := AcousticFingerprint(tune(1, 40)); string(again.Data) != string(fp.Data) {
		t.Fatal("the same samples give the same words")
	}
	// Roughly half the bits are set: the sign of a difference of differences.
	ones := 0
	for _, w := range fp.Words() {
		for ; w != 0; w &= w - 1 {
			ones++
		}
	}
	if ratio := float64(ones) / float64(32*fp.Frames); ratio < 0.3 || ratio > 0.7 {
		t.Fatalf("bit density %.2f is not balanced", ratio)
	}
}

func TestFingerprintsOfTheSameRecordingAgreeAcrossEditions(t *testing.T) {
	original := tune(7, 25)
	master := AcousticFingerprint(original)
	cases := []struct {
		name    string
		samples []float64
	}{
		{"identical", original},
		{"quieter", edition(original, 0, 0.4, 0, 1)},
		{"noisy", edition(original, 0, 1, 0.05, 2)},
		{"lead-in of a second", edition(original, 1, 1, 0, 3)},
		{"trimmed start", original[7*FingerprintRate/10:]},
		{"lead-in, quieter and noisy", edition(original, 2.5, 0.6, 0.03, 4)},
		{"shorter cut of ten seconds", original[:10*FingerprintRate]},
		{"a master two percent slow, by its family", Resample(original, 1.02)},
		{"a master three percent fast, by its family", Resample(original, 0.97)},
	}
	for _, tc := range cases {
		c := CompareFingerprints(master, AcousticFingerprintFamily(tc.samples))
		if !c.Known || !c.Same() {
			t.Errorf("%s: distance %.3f over %d frames at shift %d speed %.2f is not the same sound", tc.name, c.Distance, c.Overlap, c.Shift, c.Speed)
		}
	}
	if c := CompareFingerprints(master, AcousticFingerprint(original)); c.Speed != 1 || c.Shift != 0 || c.Distance != 0 {
		t.Fatalf("identical words match at the natural speed and no shift: %+v", c)
	}
	// A slow master is found by the family on either side: the slow one's
	// variants against the master, or the master's against the slow one.
	slow := Resample(original, 1.02)
	if c := CompareFingerprints(master, AcousticFingerprintFamily(slow)); !c.Same() || c.Speed != 0.98 {
		t.Fatalf("a slow master is found by its own family at 0.98, got %+v", c)
	}
	if c := CompareFingerprints(AcousticFingerprintFamily(original), AcousticFingerprint(slow)); !c.Same() || c.Speed != 0.98 {
		t.Fatalf("a slow master is found by the master's family too, got %+v", c)
	}
	if c := CompareFingerprints(master, AcousticFingerprint(slow)); c.Same() {
		t.Fatalf("without a family the natural fingerprints of two speeds do not match: %+v", c)
	}
	if c := CompareFingerprints(master, AcousticFingerprint(edition(original, 1, 1, 0, 3))); c.Shift != -11 && c.Shift != -10 {
		t.Fatalf("a second of lead-in is found about eleven frames later in the edition, got shift %d", c.Shift)
	}
}

func TestFingerprintsOfDifferentRecordingsStayApart(t *testing.T) {
	master := AcousticFingerprint(tune(7, 25))
	for seed := int64(8); seed < 14; seed++ {
		c := CompareFingerprints(master, AcousticFingerprint(tune(seed, 25)))
		if !c.Known || c.Same() || c.Distance < 0.4 {
			t.Errorf("tune %d: distance %.3f reads as the same sound", seed, c.Distance)
		}
	}
	// The rate is global over the overlap: an edit that swaps four seconds
	// of a twenty-five-second beginning is still mostly the same sound, and
	// the listed lengths are what tell it apart; one that shares only its
	// last seconds is a different recording.
	original := tune(7, 25)
	if c := CompareFingerprints(master, AcousticFingerprint(append(tune(20, 4), original[4*FingerprintRate:]...))); !c.Same() || c.Distance < 0.03 {
		t.Fatalf("an edit with another four-second intro is mostly the same sound: %.3f", c.Distance)
	}
	if c := CompareFingerprints(master, AcousticFingerprint(append(tune(20, 22), original[22*FingerprintRate:]...))); c.Same() {
		t.Fatalf("a recording that shares only its last seconds is not the same sound: %.3f", c.Distance)
	}
	// Too little overlap is no verdict at all.
	if c := CompareFingerprints(master, AcousticFingerprint(tune(7, 3))); c.Known {
		t.Fatalf("three seconds cannot be compared: %+v", c)
	}
	other := AcousticFingerprint(tune(7, 25))
	other.Type = "frame-sequence-v1"
	if c := CompareFingerprints(master, other); c.Known {
		t.Fatal("fingerprints of different types are never compared")
	}
}

func TestFingerprintsTravelAsBase64AndKeepTheirKey(t *testing.T) {
	fp := AcousticFingerprint(tune(3, 12))
	data, err := json.Marshal(fp)
	if err != nil {
		t.Fatal(err)
	}
	var back Fingerprint
	if err := json.Unmarshal(data, &back); err != nil || !back.Valid() || string(back.Data) != string(fp.Data) || back.Frames != fp.Frames || back.Seconds != 12 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	if ContentKey(fp) != ContentKey(back) || len(ContentKey(fp)) != len("fp:")+16 {
		t.Fatalf("the content key is a short digest of the words: %q", ContentKey(fp))
	}
	for name, broken := range map[string]Fingerprint{
		"no frames":    {Type: FingerprintAcousticBands},
		"short data":   {Type: FingerprintAcousticBands, Frames: 3, Data: []byte{1, 2, 3, 4}},
		"unknown type": {Type: "chromaprint", Frames: 1, Data: []byte{1, 2, 3, 4}},
		"long span":    {Type: FingerprintAcousticBands, Frames: 1, Data: []byte{1, 2, 3, 4}, Seconds: 40},
	} {
		if broken.Valid() {
			t.Errorf("%s was accepted", name)
		}
	}
	var none *Fingerprint
	if none.Valid() {
		t.Fatal("a nil fingerprint is not valid")
	}
}

func TestMatchRecordingsLetsTheSoundOverruleTheTags(t *testing.T) {
	original := tune(11, 25)
	album := AcousticFingerprint(original)
	reissue := AcousticFingerprint(edition(original, 0.5, 0.7, 0.02, 9))
	other := AcousticFingerprint(tune(12, 25))
	davis := AudioIdentity{TitleKey: "so what", CreatorKey: "miles davis", DurationSeconds: 562, Fingerprint: &album}
	cases := []struct {
		name  string
		other AudioIdentity
		same  bool
		basis string
	}{
		{"same sound under another title", AudioIdentity{TitleKey: "track 01", DurationSeconds: 560, Fingerprint: &reissue}, true, "fingerprint"},
		{"same sound under another performer", AudioIdentity{TitleKey: "so what", CreatorKey: "unknown artist", Fingerprint: &reissue}, true, "fingerprint"},
		{"same tags, different sound", AudioIdentity{TitleKey: "so what", CreatorKey: "miles davis", DurationSeconds: 562, Fingerprint: &other}, false, "fingerprint"},
		{"same sound, radio edit length", AudioIdentity{TitleKey: "so what", CreatorKey: "miles davis", DurationSeconds: 210, Fingerprint: &reissue}, false, "duration"},
		{"same sound, trimmed by three percent", AudioIdentity{TitleKey: "so what", CreatorKey: "miles davis", DurationSeconds: 545, Fingerprint: &reissue}, true, "fingerprint"},
		{"no fingerprint on one side", AudioIdentity{TitleKey: "so what", CreatorKey: "miles davis sextet", DurationSeconds: 565}, true, "tags"},
		{"no fingerprint, other title", AudioIdentity{TitleKey: "track 01", DurationSeconds: 560}, false, "tags"},
	}
	for _, tc := range cases {
		got := MatchRecordings(davis, tc.other)
		if got.Same != tc.same || got.Basis != tc.basis {
			t.Errorf("%s: %+v, want same=%t basis=%s", tc.name, got, tc.same, tc.basis)
		}
		if SameRecording(davis, tc.other) != tc.same {
			t.Errorf("%s: SameRecording disagrees with MatchRecordings", tc.name)
		}
	}
}
