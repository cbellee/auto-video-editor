package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// thematicPlan captures the #8 thematic Edit Intent provenance and the ordered
// selected segments with their energy, so tests can assert the energy arc.
type thematicPlan struct {
	EditIntent string `json:"edit_intent"`
	Ranking    *struct {
		Intent       string `json:"intent"`
		Theme        string `json:"theme"`
		ThemeSource  string `json:"theme_source"`
		SystemPrompt string `json:"system_prompt"`
	} `json:"ranking"`
	Segments []struct {
		SourcePath string `json:"source_path"`
		Score      *struct {
			Energy float64 `json:"energy"`
		} `json:"score"`
	} `json:"selected_segments"`
}

func decodeThematicPlan(t *testing.T, planPath string) thematicPlan {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan thematicPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

func TestEditThematicReordersIntoEnergyArc(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-peak.mp4", "02-calm.mp4", "03-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--intent", "thematic", "--theme", "an adventure")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeThematicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.EditIntent != "thematic" {
		t.Fatalf("edit_intent = %q, want thematic", plan.EditIntent)
	}
	if plan.Ranking == nil || plan.Ranking.Theme != "an adventure" || plan.Ranking.ThemeSource != "user" {
		t.Fatalf("ranking theme provenance = %+v, want user-supplied theme", plan.Ranking)
	}
	if len(plan.Segments) < 2 {
		t.Fatalf("expected multiple selected segments, got %d", len(plan.Segments))
	}
	for index := 1; index < len(plan.Segments); index++ {
		if plan.Segments[index].Score == nil || plan.Segments[index-1].Score == nil {
			t.Fatalf("segments missing AI scores: %+v", plan.Segments)
		}
		if plan.Segments[index].Score.Energy < plan.Segments[index-1].Score.Energy {
			t.Errorf("segments not in rising-energy order: %.2f then %.2f",
				plan.Segments[index-1].Score.Energy, plan.Segments[index].Score.Energy)
		}
	}
	if plan.Segments[0].Score.Energy != 0.2 {
		t.Errorf("first segment energy = %.2f, want the calm establishing shot (0.20)", plan.Segments[0].Score.Energy)
	}
	last := plan.Segments[len(plan.Segments)-1]
	if last.Score.Energy != 0.9 {
		t.Errorf("last segment energy = %.2f, want the peak conclusive shot (0.90)", last.Score.Energy)
	}
}

func TestEditThematicDiscoversThemeWhenAbsent(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--intent", "thematic")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeThematicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Ranking == nil {
		t.Fatal("plan is missing ranking provenance")
	}
	if plan.Ranking.Theme == "" {
		t.Error("expected a discovered theme to be recorded")
	}
	if plan.Ranking.ThemeSource != "discovered" {
		t.Errorf("theme_source = %q, want discovered", plan.Ranking.ThemeSource)
	}
	if strings.Contains(plan.Ranking.SystemPrompt, plan.Ranking.Theme) {
		t.Errorf("recorded system prompt should reflect the empty-theme scoring prompt, "+
			"but contains the discovered theme %q:\n%s", plan.Ranking.Theme, plan.Ranking.SystemPrompt)
	}
}

func TestEditDefaultsToChronologicalIntent(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeThematicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.EditIntent != "chronological" {
		t.Errorf("edit_intent = %q, want chronological", plan.EditIntent)
	}
	if plan.Ranking == nil || plan.Ranking.Intent != "chronological" {
		t.Errorf("ranking intent = %+v, want chronological", plan.Ranking)
	}
	if plan.Ranking.Theme != "" || plan.Ranking.ThemeSource != "" {
		t.Errorf("chronological edit should record no theme: %+v", plan.Ranking)
	}
}

func TestEditRejectsUnknownIntent(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--intent", "documentary")
	if status == 0 {
		t.Fatalf("expected failure for unknown intent\noutput:\n%s", output)
	}
}
