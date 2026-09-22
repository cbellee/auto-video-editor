package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rankingPlan captures the #7 ranking provenance written into an Edit Plan.
type rankingPlan struct {
	Ranking *struct {
		Model          string  `json:"model"`
		BaseURL        string  `json:"base_url"`
		SystemPrompt   string  `json:"system_prompt"`
		Guidance       string  `json:"guidance"`
		TargetSeconds  float64 `json:"target_seconds"`
		SelectedSecond float64 `json:"selected_seconds"`
		DurationSource string  `json:"duration_source"`
	} `json:"ranking"`
	Audio struct {
		Source string `json:"source"`
	} `json:"audio"`
	Segments []struct {
		SourcePath  string    `json:"source_path"`
		StartSecond float64   `json:"start_seconds"`
		EndSecond   float64   `json:"end_seconds"`
		Transition  string    `json:"transition"`
		SampleTimes []float64 `json:"sample_times"`
		Prompt      string    `json:"prompt"`
		Score       *struct {
			VisualInterest float64 `json:"visual_interest"`
			Base           float64 `json:"base_score"`
			Redundancy     float64 `json:"redundancy"`
		} `json:"score"`
	} `json:"selected_segments"`
	RankedOut []struct {
		SourcePath string `json:"source_path"`
		Reason     string `json:"reason"`
	} `json:"ranked_out_segments"`
}

func decodeRankingPlan(t *testing.T, planPath string) rankingPlan {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan rankingPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

// editEnv builds an environment for edit tests that overrides the shared
// LM Studio URL and config directory set by TestMain, so a test can control
// discovery and remembered-model behavior in isolation.
func editEnv(t *testing.T, toolDir, lmStudioURL, configDir string) []string {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "AVE_LM_STUDIO_URL=") ||
			strings.HasPrefix(entry, "AVE_CONFIG_DIR=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PATH="+toolDir)
	if lmStudioURL != "" {
		env = append(env, "AVE_LM_STUDIO_URL="+lmStudioURL)
	}
	if configDir != "" {
		env = append(env, "AVE_CONFIG_DIR="+configDir)
	}
	return env
}

func TestEditRecordsRankingProvenance(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeRankingPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Ranking == nil {
		t.Fatal("plan is missing ranking provenance")
	}
	if plan.Ranking.Model != "local/vision-model" {
		t.Errorf("ranking model = %q, want the remembered model", plan.Ranking.Model)
	}
	if !strings.HasPrefix(plan.Ranking.BaseURL, "http://127.0.0.1") {
		t.Errorf("ranking base URL = %q, want a loopback address", plan.Ranking.BaseURL)
	}
	if plan.Ranking.SystemPrompt == "" {
		t.Error("ranking system prompt should be recorded")
	}
	if plan.Ranking.TargetSeconds <= 0 || plan.Ranking.SelectedSecond <= 0 {
		t.Errorf("ranking durations should be positive: %+v", plan.Ranking)
	}
	if plan.Audio.Source != "source" {
		t.Errorf("audio source = %q, want source", plan.Audio.Source)
	}
	if len(plan.Segments) == 0 {
		t.Fatal("expected selected segments")
	}
	for index, segment := range plan.Segments {
		if segment.Transition != "cut" {
			t.Errorf("segment %d transition = %q, want cut", index, segment.Transition)
		}
		if segment.Score == nil {
			t.Errorf("segment %d is missing its AI score", index)
		}
		if len(segment.SampleTimes) == 0 {
			t.Errorf("segment %d is missing contact-sheet sample times", index)
		}
		if segment.Prompt == "" {
			t.Errorf("segment %d is missing its ranking prompt", index)
		}
	}
}

func TestEditRejectsNonLoopbackLMStudio(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	env := editEnv(t, createEditTools(t), "http://10.0.0.5:1234", filepath.Join(t.TempDir(), "config"))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status == 0 {
		t.Fatalf("expected failure for non-loopback LM Studio\noutput:\n%s", output)
	}
	if !strings.Contains(strings.ToLower(output), "loopback") {
		t.Errorf("expected a loopback error, got:\n%s", output)
	}
}

func TestEditRequiresModelWhenNoneRemembered(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	server := newFakeLMStudio(t, true)
	env := editEnv(t, createEditTools(t), server.URL, filepath.Join(t.TempDir(), "config"))

	status, output := runCLIInDirStdin(t, binary, workingDir, env, strings.NewReader(""),
		"edit", sourceDir, "--plan-only")
	if status == 0 {
		t.Fatalf("expected failure when no model is selectable non-interactively\noutput:\n%s", output)
	}
	if !strings.Contains(output, "--model") {
		t.Errorf("expected guidance to pass --model, got:\n%s", output)
	}
}

func TestEditDropsNearDuplicateFootage(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-dup.mp4", "02-dup.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeRankingPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Segments) != 1 {
		t.Fatalf("selected %d segments, want 1 after dropping the near-duplicate", len(plan.Segments))
	}
	if len(plan.RankedOut) != 1 {
		t.Fatalf("ranked out %d segments, want 1", len(plan.RankedOut))
	}
	if !strings.Contains(plan.RankedOut[0].Reason, "diversity") {
		t.Errorf("ranked-out reason = %q, want a diversity reason", plan.RankedOut[0].Reason)
	}
}

func TestEditModelFlagRemembersChoice(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	server := newFakeLMStudio(t, true)
	configDir := filepath.Join(t.TempDir(), "config")
	env := editEnv(t, createEditTools(t), server.URL, configDir)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--model", "local/vision-model")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	remembered, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("read remembered config: %v", err)
	}
	if !strings.Contains(string(remembered), "local/vision-model") {
		t.Errorf("expected the model to be remembered, got: %s", remembered)
	}
}

func TestEditGuidanceAffectsRanking(t *testing.T) {
	binary := buildCLI(t)

	withoutGuidance := runGuidanceEdit(t, binary, "")
	withGuidance := runGuidanceEdit(t, binary, "prefer:clip")

	if withGuidance <= withoutGuidance {
		t.Errorf("guidance should raise the recorded visual interest: with=%.2f without=%.2f",
			withGuidance, withoutGuidance)
	}
	if withGuidance < 0.98 {
		t.Errorf("guided visual interest = %.2f, want the guidance-boosted score", withGuidance)
	}
}

// runGuidanceEdit runs a plan-only edit and returns the first segment's recorded
// visual interest, optionally with Selection Guidance.
func runGuidanceEdit(t *testing.T, binary, guidance string) float64 {
	t.Helper()
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))
	args := []string{"edit", sourceDir, "--plan-only"}
	if guidance != "" {
		args = append(args, "--guidance", guidance)
	}
	status, output := runCLIInDir(t, binary, workingDir, env, args...)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeRankingPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Segments) == 0 || plan.Segments[0].Score == nil {
		t.Fatalf("expected a scored segment, got %+v", plan.Segments)
	}
	if guidance != "" && plan.Ranking.Guidance != guidance {
		t.Errorf("plan guidance = %q, want %q", plan.Ranking.Guidance, guidance)
	}
	return plan.Segments[0].Score.VisualInterest
}

func TestEditDurationOverride(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--duration", "3")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeRankingPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Ranking.DurationSource != "override" {
		t.Errorf("duration source = %q, want override", plan.Ranking.DurationSource)
	}
	if plan.Ranking.TargetSeconds != 3 {
		t.Errorf("target seconds = %.2f, want 3", plan.Ranking.TargetSeconds)
	}
}

func TestEditFailsAfterOneRepairAttempt(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "badscore.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status == 0 {
		t.Fatalf("expected failure when the model never returns a valid score\noutput:\n%s", output)
	}
	if !strings.Contains(output, "repair") {
		t.Errorf("expected a one-repair-then-fail error, got:\n%s", output)
	}
}

func TestEditRecoversWithOneRepair(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "needsrepair.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0 after a successful repair\noutput:\n%s", status, output)
	}
	plan := decodeRankingPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Segments) == 0 {
		t.Fatal("expected a segment after repair")
	}
}
