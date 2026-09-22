package edit

import (
	"strings"
	"testing"

	"github.com/cbellee/auto-video-editor/internal/lmstudio"
)

func rc(order int, dur, base, redundancy float64, subjects, actions []string) rankedCandidate {
	return rankedCandidate{
		eligibleCandidate: eligibleCandidate{order: order, rng: candidateRange{start: 0, end: dur}},
		base:              base,
		score:             lmstudio.Score{Redundancy: redundancy, Subjects: subjects, Actions: actions},
	}
}

func TestAutomaticDuration(t *testing.T) {
	tests := []struct {
		name                     string
		eligible, override, want float64
	}{
		{"override wins", 100, 45, 45},
		{"fraction below floor clamps to 30", 100, 0, 30},
		{"fraction within band", 1000, 0, 120},
		{"short footage finishes short", 6, 0, 6},
		{"mid footage", 800, 0, 120},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := automaticDuration(test.eligible, test.override); got != test.want {
				t.Errorf("automaticDuration(%v,%v) = %v, want %v", test.eligible, test.override, got, test.want)
			}
		})
	}
}

func TestAutomaticDurationClampCeiling(t *testing.T) {
	// 0.15 * 900 = 135 -> clamped to 120.
	if got := automaticDuration(900, 0); got != 120 {
		t.Errorf("automaticDuration(900,0) = %v, want 120", got)
	}
}

func TestSelectChronologicalPreservesOrderAndFillsTarget(t *testing.T) {
	candidates := []rankedCandidate{
		rc(0, 4, 0.2, 0, []string{"beach"}, []string{"walking"}),
		rc(1, 4, 0.9, 0, []string{"surfing"}, []string{"riding"}),
		rc(2, 4, 0.5, 0, []string{"sunset"}, []string{"panning"}),
	}
	selected, dropped := selectChronological(candidates, 8)
	if len(selected) != 2 {
		t.Fatalf("selected %d, want 2 to fill 8s target", len(selected))
	}
	// Highest scores are order 1 (0.9) and order 2 (0.5); output must be chronological.
	if selected[0].order != 1 || selected[1].order != 2 {
		t.Errorf("selection not chronological: got orders %d,%d", selected[0].order, selected[1].order)
	}
	if len(dropped) != 1 || dropped[0].order != 0 {
		t.Errorf("expected the weakest candidate dropped, got %+v", dropped)
	}
}

func TestSelectChronologicalDropsNearDuplicates(t *testing.T) {
	candidates := []rankedCandidate{
		rc(0, 4, 0.9, 0.8, []string{"dog"}, []string{"running"}),
		rc(1, 4, 0.8, 0.8, []string{"dog"}, []string{"running"}), // duplicate of 0
		rc(2, 4, 0.7, 0.1, []string{"kite"}, []string{"flying"}),
	}
	selected, dropped := selectChronological(candidates, 100)
	if len(selected) != 2 {
		t.Fatalf("selected %d, want 2 (duplicate removed)", len(selected))
	}
	for _, candidate := range selected {
		if candidate.order == 1 {
			t.Errorf("near-duplicate candidate 1 should have been dropped")
		}
	}
	if len(dropped) != 1 || dropped[0].order != 1 {
		t.Errorf("expected candidate 1 dropped as duplicate, got %+v", dropped)
	}
}

func TestSelectChronologicalRetainsDistinctAction(t *testing.T) {
	// Same subject but a distinct action must be retained (US34).
	candidates := []rankedCandidate{
		rc(0, 4, 0.9, 0.8, []string{"dog"}, []string{"running"}),
		rc(1, 4, 0.8, 0.8, []string{"dog"}, []string{"jumping"}),
	}
	selected, _ := selectChronological(candidates, 100)
	if len(selected) != 2 {
		t.Fatalf("distinct action should be retained: selected %d, want 2", len(selected))
	}
}

func TestBaseScoreWeighting(t *testing.T) {
	high := baseScore(lmstudio.Score{VisualInterest: 1, Usefulness: 1, Energy: 1})
	low := baseScore(lmstudio.Score{VisualInterest: 0, Usefulness: 0, Energy: 0})
	if high <= low || high != 1 || low != 0 {
		t.Errorf("baseScore extremes wrong: high=%v low=%v", high, low)
	}
}

func TestSampleTimestamps(t *testing.T) {
	times := sampleTimestamps(candidateRange{start: 0, end: 4}, 4)
	want := []float64{0, 1, 2, 3}
	if len(times) != 4 {
		t.Fatalf("got %d timestamps, want 4", len(times))
	}
	for index := range want {
		if times[index] != want[index] {
			t.Errorf("timestamp %d = %v, want %v", index, times[index], want[index])
		}
	}
}

func TestSampleTimestampsMatchesFPSFilterSlices(t *testing.T) {
	// A 6s window sampled at 4 frames uses fps=0.667, extracting frames at the
	// start of each 1.5s slice.
	times := sampleTimestamps(candidateRange{start: 2, end: 8}, 4)
	want := []float64{2, 3.5, 5, 6.5}
	for index := range want {
		if times[index] != want[index] {
			t.Errorf("timestamp %d = %v, want %v", index, times[index], want[index])
		}
	}
}

func TestSystemPromptFoldsGuidance(t *testing.T) {
	base := systemPrompt("")
	guided := systemPrompt("prefer dogs")
	if strings.Contains(base, "prefer dogs") {
		t.Error("base prompt should not contain guidance")
	}
	if !strings.Contains(guided, "prefer dogs") {
		t.Error("guided prompt should contain the guidance text")
	}
}
