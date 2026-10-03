package ave_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editEnvWith builds the child-process environment for an edit run, starting
// from the inherited environment, removing any keys the caller overrides (so a
// later duplicate does not lose to the inherited value, since the child reads
// the first match), then appending the overrides and the tool PATH.
func editEnvWith(t *testing.T, toolDir string, overrides map[string]string) []string {
	t.Helper()
	drop := map[string]bool{"PATH": true}
	for key := range overrides {
		drop[key] = true
	}
	base := os.Environ()
	env := make([]string, 0, len(base)+len(overrides)+1)
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if drop[key] {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	if configDir := overrides["AVE_CONFIG_DIR"]; configDir != "" {
		seedWhisperModel(t, configDir)
	}
	env = append(env, "PATH="+toolDir)
	return env
}

// seedWhisperModel places a discoverable Whisper model under an isolated test
// config directory so dialogue transcription can resolve a model path.
func seedWhisperModel(t *testing.T, configDir string) {
	t.Helper()
	modelsDir := filepath.Join(configDir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("create models dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "ggml-base.en.bin"), []byte("fake-model"), 0o644); err != nil {
		t.Fatalf("write model fixture: %v", err)
	}
}

// makeDistinctEditSource writes Source Clips whose bytes differ per name so each
// produces a distinct content fingerprint. This keeps the content-addressed
// analysis cache from collapsing different clips onto one entry.
func makeDistinctEditSource(t *testing.T, names ...string) (string, string) {
	t.Helper()
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "source")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	for _, name := range names {
		content := []byte("fixture-" + name)
		if err := os.WriteFile(filepath.Join(sourceDir, name), content, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return workingDir, sourceDir
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter %s: %v", path, err)
	}
	return strings.Count(string(data), "\n")
}

// TestEditCachesAnalysisBetweenRuns proves AC1/AC2: a second edit of the same
// clips with the cache enabled reuses persisted analysis instead of re-running
// the FFmpeg analysis passes.
func TestEditCachesAnalysisBetweenRuns(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeDistinctEditSource(t, "hero.mp4", "boring.mp4")

	cacheDir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "analysis-calls")
	env := editEnvWith(t, toolDir, map[string]string{
		"AVE_CACHE_DIR":                  cacheDir,
		"AVE_NO_CACHE":                   "",
		"AVE_TEST_FFMPEG_ANALYSIS_CALLS": counter,
	})

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("first run status = %d, want 0\noutput:\n%s", status, output)
	}
	firstCalls := countLines(t, counter)
	if firstCalls == 0 {
		t.Fatalf("expected analysis calls on the first run, got 0")
	}

	if err := os.Remove(counter); err != nil {
		t.Fatalf("reset counter: %v", err)
	}

	status, output = runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--force")
	if status != 0 {
		t.Fatalf("second run status = %d, want 0\noutput:\n%s", status, output)
	}
	secondCalls := countLines(t, counter)
	if secondCalls != 0 {
		t.Fatalf("second run ran %d analysis call(s); want 0 (cache reuse)", secondCalls)
	}
}

// TestEditJobsFlagValidation proves AC4: the parallelism override is validated.
func TestEditJobsFlagValidation(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)

	cases := []struct {
		name    string
		jobs    []string
		wantErr bool
	}{
		{name: "zero rejected", jobs: []string{"--jobs", "0"}, wantErr: true},
		{name: "negative rejected", jobs: []string{"--jobs", "-2"}, wantErr: true},
		{name: "non-numeric rejected", jobs: []string{"--jobs", "lots"}, wantErr: true},
		{name: "missing value rejected", jobs: []string{"--jobs"}, wantErr: true},
		{name: "positive accepted", jobs: []string{"--jobs", "2"}, wantErr: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workingDir, sourceDir := makeEditSource(t, "good.mp4")
			env := append(os.Environ(), "PATH="+toolDir)
			args := append([]string{"edit", sourceDir, "--plan-only"}, testCase.jobs...)
			status, output := runCLIInDir(t, binary, workingDir, env, args...)
			if testCase.wantErr && status == 0 {
				t.Fatalf("status = 0, want failure\noutput:\n%s", output)
			}
			if !testCase.wantErr && status != 0 {
				t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
			}
		})
	}
}

// TestEditProgressModes proves AC5: normal shows stage lines, quiet suppresses
// them, and verbose adds subprocess detail (cache reuse notes).
func TestEditProgressModes(t *testing.T) {
	binary := buildCLI(t)

	t.Run("normal shows stages without detail", func(t *testing.T) {
		toolDir := createEditTools(t)
		workingDir, sourceDir := makeEditSource(t, "good.mp4")
		env := append(os.Environ(), "PATH="+toolDir)
		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
		if status != 0 {
			t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "• Analyzing") {
			t.Errorf("normal output missing stage line:\n%s", output)
		}
		if strings.Contains(output, "  - ") {
			t.Errorf("normal output should not include verbose detail:\n%s", output)
		}
	})

	t.Run("quiet suppresses stages", func(t *testing.T) {
		toolDir := createEditTools(t)
		workingDir, sourceDir := makeEditSource(t, "good.mp4")
		env := append(os.Environ(), "PATH="+toolDir)
		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--quiet")
		if status != 0 {
			t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
		}
		if strings.Contains(output, "• ") {
			t.Errorf("quiet output should not include stage lines:\n%s", output)
		}
		if strings.Contains(output, "  - ") {
			t.Errorf("quiet output should not include detail lines:\n%s", output)
		}
	})

	t.Run("verbose shows cache reuse detail", func(t *testing.T) {
		toolDir := createEditTools(t)
		workingDir, sourceDir := makeDistinctEditSource(t, "hero.mp4")
		cacheDir := t.TempDir()
		env := editEnvWith(t, toolDir, map[string]string{
			"AVE_CACHE_DIR": cacheDir,
			"AVE_NO_CACHE":  "",
		})
		// Prime the cache.
		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
		if status != 0 {
			t.Fatalf("prime run status = %d, want 0\noutput:\n%s", status, output)
		}
		// Second run in verbose mode should report the reuse.
		status, output = runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--force", "--verbose")
		if status != 0 {
			t.Fatalf("verbose run status = %d, want 0\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "  - reusing cached analysis") {
			t.Errorf("verbose output missing cache reuse detail:\n%s", output)
		}
	})
}

// TestEditPersistsJobsDefault proves AC3: an explicit --jobs choice persists as
// an operational default, while creative inputs do not leak into the config.
func TestEditPersistsJobsDefault(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeEditSource(t, "good.mp4")

	configDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configDir, "config.json"),
		[]byte(`{"last_model":"local/vision-model"}`),
		0o644,
	); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	env := editEnvWith(t, toolDir, map[string]string{"AVE_CONFIG_DIR": configDir})

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--jobs", "3", "--theme", "sunset road trip")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	config := string(data)
	if !strings.Contains(config, `"default_jobs": 3`) {
		t.Errorf("config did not persist operational --jobs default: %s", config)
	}
	if strings.Contains(config, "sunset road trip") || strings.Contains(config, "theme") {
		t.Errorf("config leaked creative input: %s", config)
	}
}

// TestEditCleansTempArtifacts proves AC7: a successful edit leaves no
// run-specific scratch files behind in the working directory.
func TestEditCleansTempArtifacts(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeEditSource(t, "good.mp4")
	env := append(os.Environ(), "PATH="+toolDir)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	assertNoScratchArtifacts(t, workingDir)
}

// TestEditCleansTempArtifactsOnFailure proves AC7 under failure: a render that
// fails mid-flight still leaves no partial scratch files behind.
func TestEditCleansTempArtifactsOnFailure(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeEditSource(t, "a-hero.mp4", "b-shaky-clip.mp4")
	env := append(os.Environ(), "PATH="+toolDir, "AVE_TEST_VIDSTAB_FAIL=1")

	status, _ := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--shake", "stabilize")
	if status == 0 {
		t.Fatalf("expected a failing render, got success")
	}

	assertNoScratchArtifacts(t, workingDir)
}

func assertNoScratchArtifacts(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		for _, marker := range []string{".ave-render", ".ave-stabilize", ".ave-cache", ".tmp"} {
			if strings.Contains(base, marker) {
				t.Errorf("found leftover scratch artifact: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk working dir: %v", err)
	}
}
