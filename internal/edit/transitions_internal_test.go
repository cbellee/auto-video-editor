package edit

import "testing"

// TestPlanTransitionsAlwaysValidates proves the planner never emits a plan its
// own validator would reject, including short music-trimmed segments where a
// bounded Transition cannot satisfy both the 0.15s floor and the twenty-percent
// ceiling and must fall back to a hard cut.
func TestPlanTransitionsAlwaysValidates(t *testing.T) {
	cases := []struct {
		name     string
		segments []SelectedSegment
	}{
		{
			name: "short final segment across a source change falls back to a cut",
			segments: []SelectedSegment{
				{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 4},
				{SourcePath: "b.mp4", StartSecond: 0, EndSecond: 0.5},
			},
		},
		{
			name: "short opening segment opens on a cut",
			segments: []SelectedSegment{
				{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 0.5},
				{SourcePath: "b.mp4", StartSecond: 0, EndSecond: 4},
			},
		},
		{
			name: "roomy segments across a source change dissolve",
			segments: []SelectedSegment{
				{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 5},
				{SourcePath: "b.mp4", StartSecond: 0, EndSecond: 5},
			},
		},
		{
			name: "continuous footage hard cuts",
			segments: []SelectedSegment{
				{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 5},
				{SourcePath: "a.mp4", StartSecond: 5, EndSecond: 10},
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			segments := make([]SelectedSegment, len(test.segments))
			copy(segments, test.segments)
			planTransitions(segments)
			for index := range segments {
				if err := validateTransitionTiming(index, segments); err != nil {
					t.Errorf("planner emitted a Transition the validator rejects at %d: %v (%+v)",
						index, err, segments[index])
				}
			}
		})
	}
}

func TestPlanTransitionsShortSegmentsCut(t *testing.T) {
	segments := []SelectedSegment{
		{SourcePath: "a.mp4", StartSecond: 0, EndSecond: 0.5},
		{SourcePath: "b.mp4", StartSecond: 0, EndSecond: 0.5},
	}
	planTransitions(segments)
	for index, segment := range segments {
		if segment.Transition != transitionCut {
			t.Errorf("segment %d Transition = %q, want a hard cut for a sub-0.75s segment", index, segment.Transition)
		}
		if segment.TransitionSeconds != 0 {
			t.Errorf("segment %d hard cut carries a duration %.3f", index, segment.TransitionSeconds)
		}
	}
}
