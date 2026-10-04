package edit

import (
	"path/filepath"
	"testing"
)

func TestArtifactPathsDefaultsExtension(t *testing.T) {
	t.Parallel()

	absOutput := func(name string) string {
		t.Helper()
		resolved, err := filepath.Abs(name)
		if err != nil {
			t.Fatalf("resolve %q: %v", name, err)
		}
		return resolved
	}

	tests := []struct {
		name          string
		sourceDir     string
		output        string
		wantVideoPath string
		wantPlanPath  string
	}{
		{
			name:          "output without extension defaults to mp4",
			output:        "HoneyMoon_Edit",
			wantVideoPath: absOutput("HoneyMoon_Edit.mp4"),
			wantPlanPath:  absOutput("HoneyMoon_Edit.plan.json"),
		},
		{
			name:          "output with mp4 extension is preserved",
			output:        "HoneyMoon_Edit.mp4",
			wantVideoPath: absOutput("HoneyMoon_Edit.mp4"),
			wantPlanPath:  absOutput("HoneyMoon_Edit.plan.json"),
		},
		{
			name:          "explicit extension is preserved",
			output:        "clip.mov",
			wantVideoPath: absOutput("clip.mov"),
			wantPlanPath:  absOutput("clip.plan.json"),
		},
		{
			name:          "output without extension in nested directory",
			output:        filepath.Join("out", "render"),
			wantVideoPath: absOutput(filepath.Join("out", "render.mp4")),
			wantPlanPath:  absOutput(filepath.Join("out", "render.plan.json")),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := artifactPaths(test.sourceDir, test.output)
			if err != nil {
				t.Fatalf("artifactPaths(%q, %q): %v", test.sourceDir, test.output, err)
			}
			if result.VideoPath != test.wantVideoPath {
				t.Errorf("VideoPath = %q, want %q", result.VideoPath, test.wantVideoPath)
			}
			if result.PlanPath != test.wantPlanPath {
				t.Errorf("PlanPath = %q, want %q", result.PlanPath, test.wantPlanPath)
			}
		})
	}
}

func TestArtifactPathsDefaultNamingUsesSourceDir(t *testing.T) {
	t.Parallel()

	result, err := artifactPaths(filepath.Join("some", "Footage"), "")
	if err != nil {
		t.Fatalf("artifactPaths: %v", err)
	}
	if got := filepath.Base(result.VideoPath); got != "Footage-edit.mp4" {
		t.Errorf("VideoPath base = %q, want %q", got, "Footage-edit.mp4")
	}
	if got := filepath.Base(result.PlanPath); got != "Footage-edit.plan.json" {
		t.Errorf("PlanPath base = %q, want %q", got, "Footage-edit.plan.json")
	}
}
