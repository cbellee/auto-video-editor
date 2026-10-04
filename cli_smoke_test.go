package ave_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeFakeWhisper installs minimal whisper.cpp shims that emit an empty, valid
// transcript so the real-tool smoke test exercises the genuine ffmpeg media
// pipeline without depending on a downloaded Whisper model. Dialogue is
// mandatory in the edit flow but orthogonal to producing a playable MP4.
func writeFakeWhisper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
prefix=""
next=""
for arg in "$@"; do
  if [ "$next" = "1" ]; then prefix="$arg"; next=""; fi
  if [ "$arg" = "-of" ]; then next="1"; fi
done
if [ -n "$prefix" ]; then
  printf '{"transcription":[]}' > "${prefix}.json"
fi
exit 0
`
	for _, name := range []string{"whisper-cli", "whisper-cpp"} {
		writeExecutable(t, dir, name, script)
	}
	return dir
}

type ffprobeStream struct {
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	RFrameRate   string `json:"r_frame_rate"`
	AvgFrameRate string `json:"avg_frame_rate"`
}

type ffprobeResult struct {
	Streams []ffprobeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func probeWithRealFFprobe(t *testing.T, ffprobe, path string) ffprobeResult {
	t.Helper()
	out, err := exec.Command(ffprobe,
		"-v", "error",
		"-show_streams", "-show_format",
		"-print_format", "json",
		path,
	).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var result ffprobeResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode ffprobe output: %v\n%s", err, out)
	}
	return result
}

// TestRealToolSmokeProducesPlayableMP4 is an environment-gated smoke test
// (AC3). It runs the genuine ffmpeg/ffprobe media pipeline end to end and
// asserts the Finished Video is a decodable, synchronized H.264/AAC MP4 with
// the expected dimensions, frame rate, and audio+video streams.
//
// It is skipped unless AVE_SMOKE_TEST is set and real ffmpeg/ffprobe are on
// PATH, so the deterministic fake-tool suite remains the default.
func TestRealToolSmokeProducesPlayableMP4(t *testing.T) {
	if os.Getenv("AVE_SMOKE_TEST") == "" {
		t.Skip("set AVE_SMOKE_TEST=1 to run the real-tool smoke test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real ffmpeg not found on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("real ffprobe not found on PATH")
	}

	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "source")
	if mkErr := os.Mkdir(sourceDir, 0o755); mkErr != nil {
		t.Fatalf("create source folder: %v", mkErr)
	}

	// Two visually distinct, sharp, moving clips with audio so they survive the
	// quality gates and are not treated as near-duplicates.
	clips := []struct {
		name   string
		source string
		freq   string
	}{
		{name: "a-one.mp4", source: "testsrc2=size=1280x720:rate=30", freq: "440"},
		{name: "b-two.mp4", source: "mandelbrot=size=1280x720:rate=30", freq: "660"},
	}
	for _, clip := range clips {
		path := filepath.Join(sourceDir, clip.name)
		genErr := exec.Command(ffmpeg,
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", clip.source,
			"-f", "lavfi", "-i", "sine=frequency="+clip.freq,
			"-t", "8",
			"-c:v", "libx264", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-shortest",
			path,
		).Run()
		if genErr != nil {
			t.Fatalf("generate %s: %v", clip.name, genErr)
		}
	}

	server := httptest.NewServer(lmStudioMux(true))
	defer server.Close()

	configDir := t.TempDir()
	if cfgErr := os.WriteFile(
		filepath.Join(configDir, "config.json"),
		[]byte(`{"last_model":"local/vision-model"}`),
		0o644,
	); cfgErr != nil {
		t.Fatalf("seed config: %v", cfgErr)
	}

	whisperDir := writeFakeWhisper(t)
	outputPath := filepath.Join(workingDir, "finished.mp4")
	env := editEnvWith(t, whisperDir+string(os.PathListSeparator)+os.Getenv("PATH"), map[string]string{
		"AVE_LM_STUDIO_URL": server.URL,
		"AVE_CONFIG_DIR":    configDir,
		"AVE_NO_CACHE":      "1",
	})

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir,
		"--output", outputPath,
		"--aspect", "landscape",
		"--fps", "30",
		"--encoder", "software",
		"--language", "en",
	)
	if status != 0 {
		t.Fatalf("real-tool edit failed: %d\n%s", status, output)
	}

	if _, statErr := os.Stat(outputPath); statErr != nil {
		t.Fatalf("Finished Video was not produced: %v", statErr)
	}

	probe := probeWithRealFFprobe(t, ffprobe, outputPath)

	var video, audio *ffprobeStream
	for i := range probe.Streams {
		switch probe.Streams[i].CodecType {
		case "video":
			video = &probe.Streams[i]
		case "audio":
			audio = &probe.Streams[i]
		}
	}
	if video == nil {
		t.Fatalf("Finished Video has no video stream: %+v", probe.Streams)
	}
	if audio == nil {
		t.Fatalf("Finished Video has no audio stream: %+v", probe.Streams)
	}
	if video.CodecName != "h264" {
		t.Errorf("video codec = %q, want h264", video.CodecName)
	}
	if audio.CodecName != "aac" {
		t.Errorf("audio codec = %q, want aac", audio.CodecName)
	}
	if video.Width != 1920 || video.Height != 1080 {
		t.Errorf("video dimensions = %dx%d, want 1920x1080", video.Width, video.Height)
	}
	if video.RFrameRate != "30/1" {
		t.Errorf("video frame rate = %q, want 30/1", video.RFrameRate)
	}
	if probe.Format.Duration == "" || probe.Format.Duration == "0.000000" {
		t.Errorf("Finished Video has no playable duration: %q", probe.Format.Duration)
	}
}

// TestRealToolRendersMixedTransitionsTimeline is an environment-gated regression
// for issue #41: a timeline that mixes a hard cut (concat) and an overlap
// transition (xfade) must render without FFmpeg's "timebase do not match" error.
// It runs the genuine ffmpeg pipeline, authors a cut followed by a wipe into the
// plan, renders, and asserts a playable MP4 results.
//
// Skipped unless AVE_SMOKE_TEST is set and real ffmpeg/ffprobe are on PATH.
func TestRealToolRendersMixedTransitionsTimeline(t *testing.T) {
	if os.Getenv("AVE_SMOKE_TEST") == "" {
		t.Skip("set AVE_SMOKE_TEST=1 to run the real-tool smoke test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real ffmpeg not found on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("real ffprobe not found on PATH")
	}

	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "source")
	if mkErr := os.Mkdir(sourceDir, 0o755); mkErr != nil {
		t.Fatalf("create source folder: %v", mkErr)
	}

	// Three visually distinct, sharp, moving clips so all survive the quality
	// gates and yield three Selected Segments to join.
	clips := []struct {
		name   string
		source string
		freq   string
	}{
		{name: "a-one.mp4", source: "testsrc2=size=1280x720:rate=30", freq: "440"},
		{name: "b-two.mp4", source: "mandelbrot=size=1280x720:rate=30", freq: "660"},
		{name: "c-three.mp4", source: "testsrc=size=1280x720:rate=30", freq: "880"},
	}
	for _, clip := range clips {
		path := filepath.Join(sourceDir, clip.name)
		genErr := exec.Command(ffmpeg,
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", clip.source,
			"-f", "lavfi", "-i", "sine=frequency="+clip.freq,
			"-t", "8",
			"-c:v", "libx264", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-shortest",
			path,
		).Run()
		if genErr != nil {
			t.Fatalf("generate %s: %v", clip.name, genErr)
		}
	}

	server := httptest.NewServer(lmStudioMux(true))
	defer server.Close()

	configDir := t.TempDir()
	if cfgErr := os.WriteFile(
		filepath.Join(configDir, "config.json"),
		[]byte(`{"last_model":"local/vision-model"}`),
		0o644,
	); cfgErr != nil {
		t.Fatalf("seed config: %v", cfgErr)
	}

	whisperDir := writeFakeWhisper(t)
	env := editEnvWith(t, whisperDir+string(os.PathListSeparator)+os.Getenv("PATH"), map[string]string{
		"AVE_LM_STUDIO_URL": server.URL,
		"AVE_CONFIG_DIR":    configDir,
		"AVE_NO_CACHE":      "1",
	})

	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir,
		"--aspect", "landscape",
		"--fps", "30",
		"--encoder", "software",
		"--language", "en",
		"--plan-only",
	)
	if status != 0 {
		t.Fatalf("real-tool edit failed: %d\n%s", status, output)
	}

	// Author a hard cut into segment 1 and an xfade into segment 2 so the render
	// crosses from a concat output into an xfade input — the exact mix that
	// exposed the timebase mismatch.
	mutatePlanSegment(t, planPath, 1, func(segment map[string]any) {
		segment["transition"] = "cut"
		segment["transition_seconds"] = 0.0
		segment["transition_reason"] = "regression hard cut"
	})
	mutatePlanSegment(t, planPath, 2, func(segment map[string]any) {
		segment["transition"] = "wipeleft"
		segment["transition_seconds"] = 0.5
		segment["transition_reason"] = "regression wipeleft"
	})

	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
	if status != 0 {
		t.Fatalf("mixed-transition render failed: %d\n%s", status, output)
	}

	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read Edit Plan: %v", readErr)
	}
	var planDoc struct {
		FinishedVideo string `json:"finished_video"`
	}
	if jsonErr := json.Unmarshal(planBytes, &planDoc); jsonErr != nil {
		t.Fatalf("decode Edit Plan: %v", jsonErr)
	}
	outputPath := planDoc.FinishedVideo
	if outputPath == "" {
		t.Fatalf("plan has no Finished Video path")
	}
	if _, statErr := os.Stat(outputPath); statErr != nil {
		t.Fatalf("Finished Video was not produced: %v", statErr)
	}

	probe := probeWithRealFFprobe(t, ffprobe, outputPath)
	var hasVideo bool
	for i := range probe.Streams {
		if probe.Streams[i].CodecType == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Fatalf("Finished Video has no video stream: %+v", probe.Streams)
	}
	if probe.Format.Duration == "" || probe.Format.Duration == "0.000000" {
		t.Errorf("Finished Video has no playable duration: %q", probe.Format.Duration)
	}
}
