package edit

import (
	"strings"
	"testing"
)

func validSegment() SelectedSegment {
	return SelectedSegment{
		SourcePath:  "a.mp4",
		StartSecond: 0,
		EndSecond:   4,
		Transition:  transitionCut,
		Score:       &SegmentScore{},
	}
}

func validPlan() Plan {
	return Plan{
		EditIntent:       "chronological",
		Audio:            AudioSettings{Source: audioSource},
		Ranking:          &RankingSettings{Model: "vision-1", TargetSeconds: 30},
		SelectedSegments: []SelectedSegment{validSegment()},
	}
}

func TestValidatePlanAcceptsWellFormed(t *testing.T) {
	if err := validatePlan(validPlan()); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestValidatePlanRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan)
		want   string
	}{
		{"unknown intent", func(p *Plan) { p.EditIntent = "thematic" }, "unknown edit intent"},
		{"bad audio", func(p *Plan) { p.Audio.Source = "music" }, "unsupported audio source"},
		{"missing ranking", func(p *Plan) { p.Ranking = nil }, "missing ranking provenance"},
		{"missing model", func(p *Plan) { p.Ranking.Model = "" }, "missing the ranking model"},
		{"zero target", func(p *Plan) { p.Ranking.TargetSeconds = 0 }, "target duration"},
		{"no segments", func(p *Plan) { p.SelectedSegments = nil }, "no selected segments"},
		{"negative start", func(p *Plan) { p.SelectedSegments[0].StartSecond = -1 }, "is negative"},
		{"end before start", func(p *Plan) { p.SelectedSegments[0].EndSecond = 0 }, "not after start"},
		{"bad transition", func(p *Plan) { p.SelectedSegments[0].Transition = "crossfade" }, "unsupported transition"},
		{"missing score", func(p *Plan) { p.SelectedSegments[0].Score = nil }, "missing its AI score"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			test.mutate(&plan)
			err := validatePlan(plan)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not contain %q", err.Error(), test.want)
			}
		})
	}
}

func TestValidatePlanRejectsOverlap(t *testing.T) {
	plan := validPlan()
	plan.SelectedSegments = []SelectedSegment{
		{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 5, Transition: transitionCut, Score: &SegmentScore{}},
		{SourcePath: "a.mp4", StartSecond: 3, EndSecond: 8, Transition: transitionCut, Score: &SegmentScore{}},
	}
	err := validatePlan(plan)
	if err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestValidatePlanAllowsAdjacentSameSource(t *testing.T) {
	plan := validPlan()
	plan.SelectedSegments = []SelectedSegment{
		{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 5, Transition: transitionCut, Score: &SegmentScore{}},
		{SourcePath: "a.mp4", StartSecond: 5, EndSecond: 8, Transition: transitionCut, Score: &SegmentScore{}},
	}
	if err := validatePlan(plan); err != nil {
		t.Fatalf("adjacent non-overlapping ranges rejected: %v", err)
	}
}
