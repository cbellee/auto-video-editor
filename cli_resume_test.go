package ave_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// countCacheEntries returns the number of persisted cache entries in dir.
func countCacheEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read cache dir %s: %v", dir, err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

// TestEditCancellationLeavesNoPartialOutputAndPreservesCache proves AC1
// (cancellation) and #12 AC6: interrupting a run terminates child processes,
// removes partial outputs, preserves the cache already on disk, and lets a
// later run resume from that cache.
func TestEditCancellationLeavesNoPartialOutputAndPreservesCache(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	cacheDir := t.TempDir()

	// Prime the cache with a completed analysis of one clip.
	primeDir, primeSource := makeDistinctEditSource(t, "hero-prime.mp4")
	primeCounter := filepath.Join(t.TempDir(), "prime-calls")
	primeEnv := editEnvWith(t, toolDir, map[string]string{
		"AVE_CACHE_DIR":                  cacheDir,
		"AVE_NO_CACHE":                   "",
		"AVE_TEST_FFMPEG_ANALYSIS_CALLS": primeCounter,
	})
	status, primeOut := runCLIInDir(t, binary, primeDir, primeEnv, "edit", primeSource, "--plan-only")
	if status != 0 {
		t.Fatalf("prime run failed: %d\n%s", status, primeOut)
	}
	if countLines(t, primeCounter) == 0 {
		t.Fatalf("prime run performed no analysis")
	}
	cachedBefore := countCacheEntries(t, cacheDir)
	if cachedBefore == 0 {
		t.Fatalf("prime run populated no cache entries")
	}

	// Interrupt a second run analyzing a different clip (a cache miss, so it
	// truly runs the slow analysis we can interrupt).
	workingDir, sourceDir := makeDistinctEditSource(t, "hero-interrupt.mp4")
	readyMarker := filepath.Join(t.TempDir(), "analysis-ready")
	outputPath := filepath.Join(workingDir, "cancelled.mp4")
	env := editEnvWith(t, toolDir, map[string]string{
		"AVE_CACHE_DIR":                  cacheDir,
		"AVE_NO_CACHE":                   "",
		"AVE_TEST_FFMPEG_SLEEP":          "30",
		"AVE_TEST_FFMPEG_ANALYSIS_READY": readyMarker,
	})

	command := exec.Command(binary, "edit", sourceDir, "--output", outputPath)
	command.Dir = workingDir
	if coverageDir := os.Getenv("AVE_COVER_DIR"); coverageDir != "" {
		env = append(env, "GOCOVERDIR="+coverageDir)
	}
	command.Env = env
	var out strings.Builder
	command.Stdout = &out
	command.Stderr = &out
	if err := command.Start(); err != nil {
		t.Fatalf("start edit: %v", err)
	}

	if !waitForFile(t, readyMarker, 10*time.Second) {
		_ = command.Process.Kill()
		t.Fatalf("analysis never began:\n%s", out.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("edit succeeded despite interruption:\n%s", out.String())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("edit did not terminate promptly after interrupt (child not cancelled):\n%s", out.String())
	}

	if _, err := os.Stat(outputPath); err == nil {
		t.Errorf("interrupted run left a partial Finished Video at %s", outputPath)
	}
	assertNoScratchArtifacts(t, workingDir)

	// The cache primed before the interruption survives intact.
	if cachedAfter := countCacheEntries(t, cacheDir); cachedAfter < cachedBefore {
		t.Errorf("interrupted run shrank the cache from %d to %d entries", cachedBefore, cachedAfter)
	}

	// Re-running the primed clip resumes entirely from the preserved cache,
	// performing no fresh analysis.
	resumeCounter := filepath.Join(t.TempDir(), "resume-calls")
	resumeEnv := editEnvWith(t, toolDir, map[string]string{
		"AVE_CACHE_DIR":                  cacheDir,
		"AVE_NO_CACHE":                   "",
		"AVE_TEST_FFMPEG_ANALYSIS_CALLS": resumeCounter,
	})
	resumeOutput := filepath.Join(primeDir, "resumed.mp4")
	status, resumeOut := runCLIInDir(t, binary, primeDir, resumeEnv, "edit", primeSource, "--output", resumeOutput)
	if status != 0 {
		t.Fatalf("resume run failed: %d\n%s", status, resumeOut)
	}
	if _, err := os.Stat(resumeOutput); err != nil {
		t.Errorf("resume run did not produce the Finished Video: %v", err)
	}
	if calls := countLines(t, resumeCounter); calls != 0 {
		t.Errorf("resume run performed %d analysis call(s); the preserved cache was not reused", calls)
	}
}

// TestEditModelTimeoutFailsCleanly proves AC2 (timeouts): when the local model
// stops responding, the run fails promptly with a clear error instead of
// hanging, honoring the configurable per-request timeout.
func TestEditModelTimeoutFailsCleanly(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeEditSource(t, "hero-clip.mp4")

	// released is closed once the test's assertions are done so the blocked
	// handler returns and the server shuts down promptly regardless of outcome.
	released := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"type":"llm","key":"local/vision-model","capabilities":{"vision":true}}]}`))
			return
		}
		// Block the scoring call well past both the short client timeout and the
		// production default, so only an honored client-side timeout can end the
		// request quickly. A generous backstop prevents a wedged test.
		select {
		case <-r.Context().Done():
		case <-released:
		case <-time.After(150 * time.Second):
		}
	}))
	// Defers run LIFO: close(released) first unblocks the handler, then Close
	// can return without waiting on the backstop.
	defer hang.Close()
	defer close(released)

	env := editEnvWith(t, toolDir, map[string]string{
		"AVE_LM_STUDIO_URL":     hang.URL,
		"AVE_LM_STUDIO_TIMEOUT": "300ms",
	})

	start := time.Now()
	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	elapsed := time.Since(start)

	if status == 0 {
		t.Fatalf("edit succeeded despite a model timeout:\n%s", output)
	}
	// With a 300ms client timeout (plus one repair attempt) a correct run fails
	// in well under 10s. If the configurable timeout were ignored, the request
	// would hang on the 150s backstop and blow past this bound.
	if elapsed > 10*time.Second {
		t.Errorf("edit took %s to fail; the per-request timeout was not honored", elapsed)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "source-edit.plan.json")); err == nil {
		t.Errorf("a plan was written despite the model timeout")
	}
}

// model calls and contacts no network service.
func TestRenderMakesNoAICalls(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)
	workingDir, sourceDir := makeEditSource(t, "a-earlier.mp4", "b-later.mp4")
	outputPath := filepath.Join(workingDir, "story.mp4")
	planPath := filepath.Join(workingDir, "story.plan.json")

	// First, produce a plan using the shared fake model server.
	env := append(os.Environ(), "PATH="+toolDir)
	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--output", outputPath, "--plan-only")
	if status != 0 {
		t.Fatalf("edit (plan) failed: %d\n%s", status, output)
	}

	// Re-render with the model endpoint pointed at a server that fails the test
	// if contacted at all.
	var hits atomic.Int64
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "render must not call the model", http.StatusInternalServerError)
	}))
	defer guard.Close()

	renderEnv := editEnvWith(t, toolDir, map[string]string{"AVE_LM_STUDIO_URL": guard.URL})
	status, output = runCLIInDir(t, binary, workingDir, renderEnv, "render", planPath)
	if status != 0 {
		t.Fatalf("render failed: %d\n%s", status, output)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("render made %d model/network call(s); want 0", got)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("render did not produce the Finished Video: %v", err)
	}
}
