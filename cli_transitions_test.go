package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transitionPlanDocument decodes the Transition provenance an Edit Plan records
// so the tests can prove the planner stayed inside the approved vocabulary and
// the duration bounds.
type transitionPlanDocument struct {
	Segments []struct {
		SourcePath        string  `json:"source_path"`
		StartSecond       float64 `json:"start_seconds"`
		EndSecond         float64 `json:"end_seconds"`
		Transition        string  `json:"transition"`
		TransitionSeconds float64 `json:"transition_seconds"`
		TransitionReason  string  `json:"transition_reason"`
	} `json:"selected_segments"`
}

func decodeTransitionPlan(t *testing.T, planPath string) transitionPlanDocument {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan transitionPlanDocument
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

// mutatePlanSegment loads a plan, applies fn to the raw segment at index, and
// writes the plan back so render tests can inject hand-authored Transitions
// into an otherwise valid, fingerprinted plan.
func mutatePlanSegment(t *testing.T, planPath string, index int, fn func(segment map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	segments, ok := doc["selected_segments"].([]any)
	if !ok || index >= len(segments) {
		t.Fatalf("plan lacks selected_segments[%d]: %v", index, doc["selected_segments"])
	}
	segment, ok := segments[index].(map[string]any)
	if !ok {
		t.Fatalf("segment %d is not an object: %v", index, segments[index])
	}
	fn(segment)
	segments[index] = segment
	doc["selected_segments"] = segments
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encode Edit Plan: %v", err)
	}
	if err := os.WriteFile(planPath, out, 0o644); err != nil {
		t.Fatalf("write Edit Plan: %v", err)
	}
}

var approvedTransitionVocabulary = map[string]bool{
	"cut": true, "fade": true, "dissolve": true,
	"dip-black": true, "dip-white": true,
	"wipeleft": true, "wiperight": true, "wipeup": true, "wipedown": true,
	"slideleft": true, "slideright": true, "slideup": true, "slidedown": true,
}

// TestEditTransitionsStayApprovedAndBounded proves the planner chooses only
// approved Transitions, opens on a fade, justifies every non-cut, and keeps
// durations inside the 0.15-0.8s band and twenty percent of each adjoining
// segment (ACs 1, 3, 4).
func TestEditTransitionsStayApprovedAndBounded(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-hero.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeTransitionPlan(t, planPath)
	if len(plan.Segments) < 2 {
		t.Fatalf("expected at least two Selected Segments, got %d", len(plan.Segments))
	}
	if plan.Segments[0].Transition != "fade" {
		t.Errorf("opening Transition = %q, want fade", plan.Segments[0].Transition)
	}
	if plan.Segments[0].TransitionReason == "" {
		t.Error("opening fade is missing its justification")
	}
	sawDissolve := false
	for index, segment := range plan.Segments {
		if !approvedTransitionVocabulary[segment.Transition] {
			t.Errorf("segment %d Transition %q is outside the approved vocabulary", index, segment.Transition)
		}
		if segment.Transition == "fade" && index != 0 {
			t.Errorf("segment %d uses a fade outside the opening", index)
		}
		if segment.Transition == "cut" {
			if segment.TransitionSeconds != 0 {
				t.Errorf("segment %d hard cut carries a duration %.3f", index, segment.TransitionSeconds)
			}
			continue
		}
		if segment.TransitionReason == "" {
			t.Errorf("segment %d Transition %q lacks a justification", index, segment.Transition)
		}
		if segment.TransitionSeconds < 0.15-1e-6 || segment.TransitionSeconds > 0.8+1e-6 {
			t.Errorf("segment %d Transition %.3fs is outside the 0.15-0.8s band", index, segment.TransitionSeconds)
		}
		this := segment.EndSecond - segment.StartSecond
		if segment.TransitionSeconds > 0.2*this+1e-6 {
			t.Errorf("segment %d Transition %.3fs exceeds twenty percent of its %.2fs segment", index, segment.TransitionSeconds, this)
		}
		if index > 0 {
			prev := plan.Segments[index-1].EndSecond - plan.Segments[index-1].StartSecond
			if segment.TransitionSeconds > 0.2*prev+1e-6 {
				t.Errorf("segment %d Transition %.3fs exceeds twenty percent of the preceding %.2fs segment", index, segment.TransitionSeconds, prev)
			}
		}
		if segment.Transition == "dissolve" {
			sawDissolve = true
		}
	}
	if !sawDissolve {
		t.Error("expected a cross-dissolve across the Source Clip change")
	}
}

// TestRenderRejectsUnapprovedTransition proves a hand-authored Transition
// outside the schema is rejected during plan validation (AC 2).
func TestRenderRejectsUnapprovedTransition(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-hero.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("edit status = %d, want 0\n%s", status, output)
	}
	mutatePlanSegment(t, planPath, 1, func(segment map[string]any) {
		segment["transition"] = "zoom-blast"
	})

	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
	if status == 0 {
		t.Fatalf("render accepted an unapproved Transition:\n%s", output)
	}
	if !strings.Contains(output, "unsupported transition") {
		t.Errorf("render did not reject the unapproved Transition:\n%s", output)
	}
}

// TestRenderRejectsOutOfBoundTransition proves a Transition whose duration
// leaves the 0.15-0.8s band is rejected during plan validation (AC 3).
func TestRenderRejectsOutOfBoundTransition(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-hero.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("edit status = %d, want 0\n%s", status, output)
	}
	mutatePlanSegment(t, planPath, 1, func(segment map[string]any) {
		segment["transition"] = "dissolve"
		segment["transition_seconds"] = 2.0
		segment["transition_reason"] = "hand-authored overlong dissolve"
	})

	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
	if status == 0 {
		t.Fatalf("render accepted an out-of-band Transition:\n%s", output)
	}
	if !strings.Contains(output, "outside the 0.15-0.80s band") &&
		!strings.Contains(output, "exceeds twenty percent") {
		t.Errorf("render did not reject the out-of-band Transition:\n%s", output)
	}
}

// TestRenderRealizesDipWipeAndSlide proves the dip, wipe, and slide Transitions
// are realized in the filtergraph: dips fade through a solid color while wipes
// and slides overlap via xfade (AC 1 render coverage).
func TestRenderRealizesDipWipeAndSlide(t *testing.T) {
	cases := []struct {
		name       string
		transition string
		want       string
	}{
		{"dip to black", "dip-black", "color=black"},
		{"dip to white", "dip-white", "color=white"},
		{"directional wipe", "wipeleft", "xfade=transition=wipeleft"},
		{"simple slide", "slideup", "xfade=transition=slideup"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binary := buildCLI(t)
			workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-hero.mp4")
			planPath := filepath.Join(workingDir, "source-edit.plan.json")
			ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
			env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

			status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
			if status != 0 {
				t.Fatalf("edit status = %d, want 0\n%s", status, output)
			}
			mutatePlanSegment(t, planPath, 1, func(segment map[string]any) {
				segment["transition"] = test.transition
				segment["transition_seconds"] = 0.3
				segment["transition_reason"] = "hand-authored " + test.transition
			})

			status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
			if status != 0 {
				t.Fatalf("render status = %d, want 0\n%s", status, output)
			}
			log, err := os.ReadFile(ffmpegLog)
			if err != nil {
				t.Fatalf("read ffmpeg log: %v", err)
			}
			if !strings.Contains(string(log), test.want) {
				t.Errorf("filtergraph missing %q for %s:\n%s", test.want, test.transition, log)
			}
		})
	}
}

// TestRenderNormalizesTimebaseForMixedTransitions proves a timeline that mixes a
// hard cut (concat) and an overlap transition (xfade) renders with a single,
// consistent timebase. Without settb=AVTB normalization, the fps filter leaves
// each segment at 1/<rate> while concat emits 1/1000000, so a concat-joined
// accumulator feeding a later xfade aborts FFmpeg with a "timebase do not match"
// error (issue #41).
func TestRenderNormalizesTimebaseForMixedTransitions(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-hero.mp4", "c-hero.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("edit status = %d, want 0\n%s", status, output)
	}
	// Segment 1 joins with a hard cut (concat); segment 2 overlaps via xfade, so
	// the accumulator crosses from a concat output into an xfade input.
	mutatePlanSegment(t, planPath, 1, func(segment map[string]any) {
		segment["transition"] = "cut"
		segment["transition_seconds"] = 0.0
		segment["transition_reason"] = "hand-authored hard cut"
	})
	mutatePlanSegment(t, planPath, 2, func(segment map[string]any) {
		segment["transition"] = "wipeleft"
		segment["transition_seconds"] = 0.3
		segment["transition_reason"] = "hand-authored wipeleft"
	})

	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
	if status != 0 {
		t.Fatalf("render status = %d, want 0\n%s", status, output)
	}
	log, err := os.ReadFile(ffmpegLog)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	graph := string(log)
	for _, want := range []string{
		",settb=AVTB[v",                 // every segment branch normalized
		"concat=n=2:v=1:a=0,settb=AVTB", // hard-cut join normalized
		"xfade=transition=wipeleft",     // overlap transition realized
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("filtergraph missing %q (timebase normalization):\n%s", want, graph)
		}
	}
}

// TestStabilizeRendersOnlyFlaggedSegments proves stabilize mode keeps a
// high-interest shaky clip, drops a low-interest one, and runs the two-pass
// vidstab transform only over the Selected Segment during render (AC 6).
func TestStabilizeRendersOnlyFlaggedSegments(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-shaky-clip.mp4", "c-boring-shaky.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--shake", "stabilize")
	if status != 0 {
		t.Fatalf("edit status = %d, want 0\n%s", status, output)
	}

	plan := decodeEditPlan(t, planPath)
	var keptShaky, keptBoring bool
	for _, segment := range plan.Segments {
		switch filepath.Base(segment.SourcePath) {
		case "b-shaky-clip.mp4":
			keptShaky = true
			if !segment.NeedsStabilization {
				t.Errorf("retained shaky segment not flagged for stabilization: %+v", segment)
			}
		case "c-boring-shaky.mp4":
			keptBoring = true
		}
	}
	if !keptShaky {
		t.Error("high-interest shaky clip was not retained under stabilize")
	}
	if keptBoring {
		t.Error("low-interest shaky clip should have been dropped under stabilize")
	}

	log, err := os.ReadFile(ffmpegLog)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	if strings.Contains(string(log), "vidstabtransform") {
		t.Errorf("final render log should be the encode pass, not the stabilize pass:\n%s", log)
	}
}

// TestStabilizationFailureIsSurfaced proves a stabilization failure aborts the
// render instead of silently substituting unstabilized footage (AC 7).
func TestStabilizationFailureIsSurfaced(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-shaky-clip.mp4")
	ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(),
		"PATH="+createEditTools(t),
		"AVE_TEST_FFMPEG_LOG="+ffmpegLog,
		"AVE_TEST_VIDSTAB_FAIL=1",
	)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--shake", "stabilize")
	if status == 0 {
		t.Fatalf("edit succeeded despite a stabilization failure:\n%s", output)
	}
	if !strings.Contains(output, "stabilization transform pass failed") {
		t.Errorf("stabilization failure was not surfaced:\n%s", output)
	}
	videoPath := filepath.Join(workingDir, "source-edit.mp4")
	if _, err := os.Stat(videoPath); err == nil {
		t.Error("a Finished Video was produced despite the stabilization failure")
	}
}
