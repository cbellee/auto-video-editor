package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWhisperModelHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "ggml-small.en.bin")
	if err := os.WriteFile(model, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write model fixture: %v", err)
	}
	t.Setenv(whisperModelEnv, model)

	resolved, err := resolveWhisperModel()
	if err != nil {
		t.Fatalf("resolveWhisperModel: %v", err)
	}
	if resolved != model {
		t.Errorf("resolved = %q, want %q", resolved, model)
	}
}

func TestResolveWhisperModelEnvMissingFileErrors(t *testing.T) {
	t.Setenv(whisperModelEnv, filepath.Join(t.TempDir(), "absent.bin"))

	_, err := resolveWhisperModel()
	if err == nil {
		t.Fatal("expected an error when the configured model file does not exist")
	}
	if !strings.Contains(err.Error(), whisperModelEnv) {
		t.Errorf("error should name %s so the user can fix it: %v", whisperModelEnv, err)
	}
}

func TestResolveWhisperModelDiscoversInConfigModelsDir(t *testing.T) {
	t.Setenv(whisperModelEnv, "")
	configDir := t.TempDir()
	t.Setenv("AVE_CONFIG_DIR", configDir)
	modelsDir := filepath.Join(configDir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("create models dir: %v", err)
	}
	// A non-preferred model plus the preferred base.en to prove preference.
	for _, name := range []string{"ggml-tiny.en.bin", "ggml-base.en.bin"} {
		if err := os.WriteFile(filepath.Join(modelsDir, name), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write model fixture: %v", err)
		}
	}

	resolved, err := resolveWhisperModel()
	if err != nil {
		t.Fatalf("resolveWhisperModel: %v", err)
	}
	if filepath.Base(resolved) != "ggml-base.en.bin" {
		t.Errorf("resolved = %q, want the preferred ggml-base.en.bin", resolved)
	}
}

func TestResolveWhisperModelMissingGivesActionableError(t *testing.T) {
	t.Setenv(whisperModelEnv, "")
	// Neutralize OS-level search roots so a brew-installed model cannot make this
	// hermetic "missing model" test pass unexpectedly.
	defer func(prev []string) { systemWhisperModelDirs = prev }(systemWhisperModelDirs)
	systemWhisperModelDirs = nil
	// Point discovery at an empty config dir and run from an empty working dir
	// so no ./models model is found either.
	configDir := t.TempDir()
	t.Setenv("AVE_CONFIG_DIR", configDir)
	workDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	_, err = resolveWhisperModel()
	if err == nil {
		t.Fatal("expected an error when no model can be found anywhere")
	}
	if !strings.Contains(err.Error(), whisperModelEnv) {
		t.Errorf("error should tell the user to set %s: %v", whisperModelEnv, err)
	}
}
