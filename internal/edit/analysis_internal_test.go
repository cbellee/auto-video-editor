package edit

import (
	"testing"
)

func TestParseSceneMetadata(t *testing.T) {
	raw := `frame:0    pts:0   pts_time:0
lavfi.scd.mafd=0.000
lavfi.scd.score=0.000
frame:1    pts:1   pts_time:1.5
lavfi.scd.mafd=32.371
lavfi.scd.score=32.027
lavfi.scd.time=1.5
frame:2    pts:2   pts_time:2.0
lavfi.scd.mafd=1.100
`
	frames := parseSceneMetadata(raw)
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	if frames[1].time != 1.5 || !frames[1].cut {
		t.Errorf("frame 1 = %+v, want time 1.5 cut true", frames[1])
	}
	if frames[1].mafd != 32.371 {
		t.Errorf("frame 1 mafd = %v, want 32.371", frames[1].mafd)
	}
	if frames[2].cut {
		t.Errorf("frame 2 should not be a cut: %+v", frames[2])
	}
}

func TestDetectCutsIgnoresBoundaries(t *testing.T) {
	frames := []frameMotion{
		{time: 0.5, cut: true},  // too close to start
		{time: 1.5, cut: true},  // valid interior cut
		{time: 9.8, cut: true},  // too close to end (clip 10s)
		{time: 4.0, cut: false}, // not a cut
	}
	cuts := detectCuts(frames, 10.0)
	if len(cuts) != 1 || cuts[0] != 1.5 {
		t.Fatalf("cuts = %v, want [1.5]", cuts)
	}
}

func TestSegmentShots(t *testing.T) {
	shots := segmentShots(10.0, []float64{3.0, 6.0})
	want := []candidateRange{{0, 3}, {3, 6}, {6, 10}}
	if len(shots) != len(want) {
		t.Fatalf("shots = %v, want %v", shots, want)
	}
	for i := range want {
		if shots[i] != want[i] {
			t.Errorf("shot %d = %v, want %v", i, shots[i], want[i])
		}
	}
}

func TestCandidatesFromShotsSubdividesLongDynamicShot(t *testing.T) {
	// A single 20s dynamic shot (high motion) should split into 3-8s windows.
	frames := []frameMotion{{time: 5, mafd: 10}, {time: 15, mafd: 10}}
	shots := []candidateRange{{0, 20}}
	candidates := candidatesFromShots(20, shots, frames)
	if len(candidates) < 3 {
		t.Fatalf("expected long shot subdivided into >=3 windows, got %d: %v", len(candidates), candidates)
	}
	for _, c := range candidates {
		d := c.end - c.start
		if d < minSegmentSeconds || d > targetMaxSeconds+0.001 {
			t.Errorf("window %v duration %.2f outside [1,8]", c, d)
		}
	}
	if candidates[len(candidates)-1].end != 20 {
		t.Errorf("last window should end at 20, got %v", candidates[len(candidates)-1])
	}
}

func TestCandidatesFromShotsCollapsesStaticShot(t *testing.T) {
	// A 30s static shot (near-zero motion) collapses to one representative window.
	frames := []frameMotion{{time: 5, mafd: 0.1}, {time: 25, mafd: 0.1}}
	candidates := candidatesFromShots(30, []candidateRange{{0, 30}}, frames)
	if len(candidates) != 1 {
		t.Fatalf("static shot candidates = %d, want 1: %v", len(candidates), candidates)
	}
	d := candidates[0].end - candidates[0].start
	if d < targetMinSeconds || d > targetMaxSeconds+0.001 {
		t.Errorf("representative window %.2fs outside target range", d)
	}
}

func TestCandidatesFromShotsKeepsShortWholeClip(t *testing.T) {
	// A 2s clip (single short shot) stays eligible down to one second.
	candidates := candidatesFromShots(2, []candidateRange{{0, 2}}, nil)
	if len(candidates) != 1 || candidates[0] != (candidateRange{0, 2}) {
		t.Fatalf("short clip candidates = %v, want [{0 2}]", candidates)
	}
}

func TestParseVideoMetrics(t *testing.T) {
	raw := `lavfi.blur=4.0
lavfi.signalstats.YAVG=100
lavfi.signalstats.YMIN=6
lavfi.signalstats.YMAX=240
lavfi.blur=6.0
lavfi.signalstats.YAVG=120
lavfi.signalstats.YMIN=4
lavfi.signalstats.YMAX=250
`
	m := parseVideoMetrics(raw)
	if m.Blur != 5.0 {
		t.Errorf("blur = %v, want 5.0", m.Blur)
	}
	if m.LumaAvg != 110 {
		t.Errorf("luma avg = %v, want 110", m.LumaAvg)
	}
	if m.LumaMin != 4 || m.LumaMax != 250 {
		t.Errorf("luma min/max = %v/%v, want 4/250", m.LumaMin, m.LumaMax)
	}
}

func TestParseAudioMetrics(t *testing.T) {
	rms, peak, silent := parseAudioMetrics("lavfi.astats.1.RMS_level=-20\nlavfi.astats.1.Peak_level=-10\nlavfi.astats.1.RMS_level=-30\n")
	if rms != -25 {
		t.Errorf("rms = %v, want -25", rms)
	}
	if peak != -10 {
		t.Errorf("peak = %v, want -10", peak)
	}
	if silent {
		t.Errorf("audio present, should not be silent")
	}

	rms, _, silent = parseAudioMetrics("frame:0 pts_time:0\n")
	if !silent || rms != audioFloorDB {
		t.Errorf("no audio: silent=%v rms=%v, want true/%v", silent, rms, audioFloorDB)
	}
}

func TestGateCandidateHardFailures(t *testing.T) {
	thresholds := qualityThresholdsFor(profileBalanced)
	tests := []struct {
		name    string
		dur     float64
		metrics SegmentMetrics
		reason  string
	}{
		{"blur", 4, SegmentMetrics{Blur: 30, LumaAvg: 120}, "excessive blur"},
		{"dark", 4, SegmentMetrics{Blur: 5, LumaAvg: 10}, "severely underexposed"},
		{"bright", 4, SegmentMetrics{Blur: 5, LumaAvg: 250}, "severely overexposed"},
		{"tooshort", 0.5, SegmentMetrics{Blur: 5, LumaAvg: 120}, "shorter than one second"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gateCandidate(tc.dur, tc.metrics, thresholds, shakeReject)
			if r.eligible || !r.hardFailure {
				t.Fatalf("expected hard failure, got %+v", r)
			}
			if !containsString(r.reasons, tc.reason) {
				t.Errorf("reasons %v missing %q", r.reasons, tc.reason)
			}
		})
	}
}

func TestGateCandidateShakeTreatment(t *testing.T) {
	thresholds := qualityThresholdsFor(profileBalanced)
	shaky := SegmentMetrics{Blur: 5, LumaAvg: 120, Motion: 40}

	rejected := gateCandidate(4, shaky, thresholds, shakeReject)
	if rejected.eligible || rejected.hardFailure || !rejected.shaky {
		t.Errorf("reject mode: expected soft shake rejection, got %+v", rejected)
	}
	if !containsString(rejected.reasons, "excessive camera shake") {
		t.Errorf("missing shake reason: %v", rejected.reasons)
	}

	stabilized := gateCandidate(4, shaky, thresholds, shakeStabilize)
	if !stabilized.eligible || !stabilized.needsStabilization || !stabilized.shaky {
		t.Errorf("stabilize mode: expected eligible+needsStabilization, got %+v", stabilized)
	}
}

func TestGateCandidatePasses(t *testing.T) {
	thresholds := qualityThresholdsFor(profileBalanced)
	r := gateCandidate(5, SegmentMetrics{Blur: 5, LumaAvg: 120, Motion: 5}, thresholds, shakeReject)
	if !r.eligible || r.hardFailure || r.shaky {
		t.Fatalf("good candidate should pass, got %+v", r)
	}
}

func TestEscapeFilterValue(t *testing.T) {
	got := escapeFilterValue(`/tmp/a b/c:d.txt`)
	want := `/tmp/a\ b/c\:d.txt`
	if got != want {
		t.Fatalf("escapeFilterValue = %q, want %q", got, want)
	}
	if escapeFilterValue("/var/folders/x/ave-1.txt") != "/var/folders/x/ave-1.txt" {
		t.Errorf("plain path should be unchanged")
	}
}

func TestQualityThresholdsProfilesDiffer(t *testing.T) {
	strict := qualityThresholdsFor(profileStrict)
	balanced := qualityThresholdsFor(profileBalanced)
	lenient := qualityThresholdsFor(profileLenient)
	if !(strict.MaxBlur < balanced.MaxBlur && balanced.MaxBlur < lenient.MaxBlur) {
		t.Errorf("blur tolerance should widen strict<balanced<lenient: %v %v %v", strict.MaxBlur, balanced.MaxBlur, lenient.MaxBlur)
	}
	if !(strict.MinLuma > balanced.MinLuma && balanced.MinLuma > lenient.MinLuma) {
		t.Errorf("min luma should relax strict>balanced>lenient")
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
