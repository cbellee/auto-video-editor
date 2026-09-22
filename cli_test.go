package ave_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditCreatesChronologicalPlanAndFinishedVideo(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "holiday")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	for _, name := range []string{"01-later.mp4", "02-earlier.mov", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write source fixture: %v", err)
		}
	}

	toolDir := createEditTools(t)
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+toolDir, "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	planPath := filepath.Join(workingDir, "holiday-edit.plan.json")
	videoPath := filepath.Join(workingDir, "holiday-edit.mp4")
	for _, path := range []string{planPath, videoPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected artifact %s: %v", path, err)
		}
	}

	var plan struct {
		Version          string `json:"version"`
		EditIntent       string `json:"edit_intent"`
		FinishedVideoSHA string `json:"finished_video_sha256"`
		Segments         []struct {
			SourcePath string  `json:"source_path"`
			Start      float64 `json:"start_seconds"`
			End        float64 `json:"end_seconds"`
		} `json:"selected_segments"`
		Render struct {
			Container          string `json:"container"`
			VideoCodec         string `json:"video_codec"`
			AudioCodec         string `json:"audio_codec"`
			VideoWidth         int    `json:"video_width"`
			VideoHeight        int    `json:"video_height"`
			VideoFrameRate     int    `json:"video_frame_rate"`
			PixelFormat        string `json:"pixel_format"`
			AudioSampleRate    int    `json:"audio_sample_rate"`
			AudioChannels      int    `json:"audio_channels"`
			AudioChannelLayout string `json:"audio_channel_layout"`
			FastStart          bool   `json:"fast_start"`
		} `json:"render"`
	}
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v\n%s", err, planData)
	}
	if plan.Version != "1" {
		t.Errorf("plan version = %q, want 1", plan.Version)
	}
	if plan.EditIntent != "chronological" {
		t.Errorf("Edit Intent = %q, want chronological", plan.EditIntent)
	}
	if len(plan.FinishedVideoSHA) != 64 {
		t.Errorf("Finished Video fingerprint length = %d, want 64", len(plan.FinishedVideoSHA))
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("selected segments = %d, want 2", len(plan.Segments))
	}
	if got := filepath.Base(plan.Segments[0].SourcePath); got != "02-earlier.mov" {
		t.Errorf("first Source Clip = %q, want 02-earlier.mov", got)
	}
	if got := filepath.Base(plan.Segments[1].SourcePath); got != "01-later.mp4" {
		t.Errorf("second Source Clip = %q, want 01-later.mp4", got)
	}
	for index, segment := range plan.Segments {
		if segment.Start != 0 || segment.End != 6.0 {
			t.Errorf("segment %d range = %.1f-%.1f, want 0.0-6.0", index, segment.Start, segment.End)
		}
	}
	if plan.Render.Container != "mp4" ||
		plan.Render.VideoCodec != "libx264" ||
		plan.Render.AudioCodec != "aac" ||
		plan.Render.VideoWidth != 1920 ||
		plan.Render.VideoHeight != 1080 ||
		plan.Render.VideoFrameRate != 30 ||
		plan.Render.PixelFormat != "yuv420p" ||
		plan.Render.AudioSampleRate != 48000 ||
		plan.Render.AudioChannels != 2 ||
		plan.Render.AudioChannelLayout != "stereo" ||
		!plan.Render.FastStart {
		t.Errorf("unexpected render settings: %+v", plan.Render)
	}

	ffmpegLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	for _, want := range []string{
		"-filter_complex",
		"concat=n=2:v=1:a=0[video]",
		"anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v libx264",
		"-c:a aac",
		"-f mp4",
		videoPath,
	} {
		if !strings.Contains(string(ffmpegLog), want) {
			t.Errorf("FFmpeg invocation does not contain %q:\n%s", want, ffmpegLog)
		}
	}
	for _, want := range []string{"Edit Plan:", planPath, "Finished Video:", videoPath} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestEditDiscoversAVISourceClips(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "avi-footage")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	// One lowercase .avi and one uppercase .AVI to prove case-insensitive
	// discovery of the AVI container.
	for _, name := range []string{"a-clip.avi", "b-clip.AVI"} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write source fixture: %v", err)
		}
	}

	toolDir := createEditTools(t)
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+toolDir, "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	planPath := filepath.Join(workingDir, "avi-footage-edit.plan.json")
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan struct {
		Segments []struct {
			SourcePath string `json:"source_path"`
		} `json:"selected_segments"`
	}
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v\n%s", err, planData)
	}

	found := make(map[string]bool)
	for _, segment := range plan.Segments {
		found[filepath.Base(segment.SourcePath)] = true
	}
	for _, want := range []string{"a-clip.avi", "b-clip.AVI"} {
		if !found[want] {
			t.Errorf("AVI Source Clip %q not discovered; plan segments: %v", want, found)
		}
	}
}

func TestEditPlanOnlyAndOutputSafety(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "weekend")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "clip.mkv"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write Source Clip: %v", err)
	}

	toolDir := createEditTools(t)
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+toolDir, "AVE_TEST_FFMPEG_LOG="+logPath)
	outputPath := filepath.Join(workingDir, "custom.mp4")
	planPath := filepath.Join(workingDir, "custom.plan.json")

	status, output := runCLIInDir(
		t,
		binary,
		workingDir,
		env,
		"edit",
		sourceDir,
		"--output",
		outputPath,
		"--plan-only",
	)
	if status != 0 {
		t.Fatalf("plan-only status = %d, want 0\noutput:\n%s", status, output)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("expected Edit Plan: %v", err)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Finished Video exists after plan-only run: %v", err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("FFmpeg was invoked during plan-only run: %v", err)
	}
	if !strings.Contains(output, "Edit Plan: "+planPath) || strings.Contains(output, "Finished Video:") {
		t.Errorf("unexpected plan-only output:\n%s", output)
	}

	status, output = runCLIInDir(
		t,
		binary,
		workingDir,
		env,
		"edit",
		sourceDir,
		"--output",
		outputPath,
		"--plan-only",
	)
	if status != 1 {
		t.Fatalf("overwrite status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "refusing to overwrite existing artifact") || !strings.Contains(output, "--force") {
		t.Errorf("overwrite failure is not actionable:\n%s", output)
	}

	if err := os.Remove(planPath); err != nil {
		t.Fatalf("remove Edit Plan: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("existing video"), 0o644); err != nil {
		t.Fatalf("write existing Finished Video: %v", err)
	}
	status, output = runCLIInDir(
		t,
		binary,
		workingDir,
		env,
		"edit",
		sourceDir,
		"--output",
		outputPath,
		"--plan-only",
	)
	if status != 1 {
		t.Fatalf("plan-only video overwrite status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "refusing to overwrite existing artifact") || !strings.Contains(output, outputPath) {
		t.Errorf("plan-only video overwrite failure is not actionable:\n%s", output)
	}

	status, output = runCLIInDir(
		t,
		binary,
		workingDir,
		env,
		"edit",
		sourceDir,
		"--output",
		outputPath,
		"--force",
	)
	if status != 0 {
		t.Fatalf("forced render status = %d, want 0\noutput:\n%s", status, output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("expected forced Finished Video: %v", err)
	}
	if !strings.Contains(output, "Finished Video: "+outputPath) {
		t.Errorf("forced render output omits Finished Video:\n%s", output)
	}

	sourcePath := filepath.Join(sourceDir, "clip.mkv")
	status, output = runCLIInDir(
		t,
		binary,
		workingDir,
		env,
		"edit",
		sourceDir,
		"--output",
		sourcePath,
		"--force",
	)
	if status != 1 {
		t.Fatalf("source collision status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "output artifact must not overwrite Source Clip") {
		t.Errorf("source collision failure is not actionable:\n%s", output)
	}
}

func TestEditCanForceReplaceItsOwnOutputInsideSourceFolder(t *testing.T) {
	binary := buildCLI(t)
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "clip.mov"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write Source Clip: %v", err)
	}
	toolDir := createEditTools(t)
	env := append(
		os.Environ(),
		"PATH="+toolDir,
		"AVE_TEST_FFMPEG_LOG="+filepath.Join(sourceDir, "ffmpeg.log"),
	)

	status, output := runCLIInDir(t, binary, sourceDir, env, "edit", ".")
	if status != 0 {
		t.Fatalf("first edit status = %d, want 0\noutput:\n%s", status, output)
	}
	status, output = runCLIInDir(t, binary, sourceDir, env, "edit", ".", "--force")
	if status != 0 {
		t.Fatalf("forced edit status = %d, want 0\noutput:\n%s", status, output)
	}

	planPath := filepath.Join(sourceDir, filepath.Base(sourceDir)+"-edit.plan.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read replaced Edit Plan: %v", err)
	}
	var plan struct {
		Segments []struct {
			SourcePath string `json:"source_path"`
		} `json:"selected_segments"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode replaced Edit Plan: %v", err)
	}
	if len(plan.Segments) != 1 || filepath.Base(plan.Segments[0].SourcePath) != "clip.mov" {
		t.Errorf("forced rerun selected generated output: %+v", plan.Segments)
	}
}

func TestEditUsesAvailableVideoToolboxEncoder(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "source")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "clip.mov"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write Source Clip: %v", err)
	}
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(
		os.Environ(),
		"PATH="+createEditTools(t),
		"AVE_TEST_FFMPEG_LOG="+logPath,
		"AVE_TEST_H264_ENCODER=videotoolbox",
	)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	ffmpegLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	if !strings.Contains(string(ffmpegLog), "-c:v h264_videotoolbox") {
		t.Errorf("FFmpeg did not use available VideoToolbox encoder:\n%s", ffmpegLog)
	}

	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	var plan struct {
		Render struct {
			VideoCodec string `json:"video_codec"`
		} `json:"render"`
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	if plan.Render.VideoCodec != "h264_videotoolbox" {
		t.Errorf("plan video codec = %q, want h264_videotoolbox", plan.Render.VideoCodec)
	}
}

func TestEditDoesNotTrustStalePlanOutputOwnership(t *testing.T) {
	binary := buildCLI(t)
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "clip.mov"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write Source Clip: %v", err)
	}
	toolDir := createEditTools(t)
	env := append(
		os.Environ(),
		"PATH="+toolDir,
		"AVE_TEST_FFMPEG_LOG="+filepath.Join(sourceDir, "ffmpeg.log"),
	)

	status, output := runCLIInDir(t, binary, sourceDir, env, "edit", ".")
	if status != 0 {
		t.Fatalf("first edit status = %d, want 0\noutput:\n%s", status, output)
	}
	videoPath := filepath.Join(sourceDir, filepath.Base(sourceDir)+"-edit.mp4")
	const replacement = "user replacement"
	if err := os.WriteFile(videoPath, []byte(replacement), 0o644); err != nil {
		t.Fatalf("replace prior Finished Video: %v", err)
	}

	status, output = runCLIInDir(t, binary, sourceDir, env, "edit", ".", "--force")
	if status != 1 {
		t.Fatalf("stale ownership status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "output artifact must not overwrite Source Clip") {
		t.Errorf("stale ownership failure is not actionable:\n%s", output)
	}
	data, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatalf("read replacement video: %v", err)
	}
	if string(data) != replacement {
		t.Errorf("replacement video was overwritten: %q", data)
	}
}

func TestEditRejectsInvalidInputs(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	emptyDir := filepath.Join(workingDir, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("create empty directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(emptyDir, "notes.txt"), []byte("not video"), 0o644); err != nil {
		t.Fatalf("write non-video fixture: %v", err)
	}
	notDirectory := filepath.Join(workingDir, "clip.mp4")
	if err := os.WriteFile(notDirectory, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write file fixture: %v", err)
	}

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput string
	}{
		{
			name:       "unknown option",
			args:       []string{"edit", emptyDir, "--unknown"},
			wantStatus: 2,
			wantOutput: `unknown edit option "--unknown"`,
		},
		{
			name:       "output requires path",
			args:       []string{"edit", emptyDir, "--output"},
			wantStatus: 2,
			wantOutput: "--output requires a file path",
		},
		{
			name:       "only one source folder",
			args:       []string{"edit", emptyDir, workingDir},
			wantStatus: 2,
			wantOutput: "edit accepts one source folder",
		},
		{
			name:       "source must be folder",
			args:       []string{"edit", notDirectory},
			wantStatus: 1,
			wantOutput: "source path is not a folder",
		},
		{
			name:       "folder needs supported clips",
			args:       []string{"edit", emptyDir},
			wantStatus: 1,
			wantOutput: "no supported Source Clips found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLIInDir(t, binary, workingDir, os.Environ(), test.args...)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d\noutput:\n%s", status, test.wantStatus, output)
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Errorf("output does not contain %q:\n%s", test.wantOutput, output)
			}
		})
	}
}

func TestEditReportsProbeFailures(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)

	tests := []struct {
		name       string
		mode       string
		wantStatus int
		wantOutput string
	}{
		{
			name:       "ffprobe command fails",
			mode:       "fail",
			wantStatus: 1,
			wantOutput: "probe Source Clip",
		},
		{
			name:       "ffprobe returns malformed JSON",
			mode:       "malformed",
			wantStatus: 1,
			wantOutput: "decode ffprobe response",
		},
		{
			name:       "ffprobe returns invalid duration",
			mode:       "invalid-duration",
			wantStatus: 1,
			wantOutput: `invalid duration "0"`,
		},
		{
			name:       "ffprobe returns malformed duration",
			mode:       "malformed-duration",
			wantStatus: 1,
			wantOutput: `parse duration "unknown"`,
		},
		{
			name:       "missing capture timestamp uses file modification time",
			mode:       "missing-capture",
			wantStatus: 0,
			wantOutput: "Edit Plan:",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			sourceDir := filepath.Join(workingDir, "source")
			if err := os.Mkdir(sourceDir, 0o755); err != nil {
				t.Fatalf("create source folder: %v", err)
			}
			if err := os.WriteFile(filepath.Join(sourceDir, "clip.mov"), []byte("fixture"), 0o644); err != nil {
				t.Fatalf("write Source Clip: %v", err)
			}
			env := append(os.Environ(), "PATH="+toolDir, "AVE_TEST_FFPROBE_MODE="+test.mode)

			status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d\noutput:\n%s", status, test.wantStatus, output)
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Errorf("output does not contain %q:\n%s", test.wantOutput, output)
			}
		})
	}
}

// makeEditSource creates a source folder populated with the named fixtures.
func makeEditSource(t *testing.T, names ...string) (string, string) {
	t.Helper()
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "source")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return workingDir, sourceDir
}

func decodeEditPlan(t *testing.T, planPath string) editPlanDocument {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan editPlanDocument
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

type editPlanDocument struct {
	Segments []struct {
		SourcePath         string  `json:"source_path"`
		IsHDR              bool    `json:"source_is_hdr"`
		StartSecond        float64 `json:"start_seconds"`
		EndSecond          float64 `json:"end_seconds"`
		Shaky              bool    `json:"shaky"`
		NeedsStabilization bool    `json:"needs_stabilization"`
		Metrics            struct {
			Blur    float64 `json:"blur"`
			LumaAvg float64 `json:"luma_avg"`
			Motion  float64 `json:"motion"`
		} `json:"metrics"`
	} `json:"selected_segments"`
	Rejected []struct {
		SourcePath  string   `json:"source_path"`
		StartSecond float64  `json:"start_seconds"`
		EndSecond   float64  `json:"end_seconds"`
		Reasons     []string `json:"reasons"`
		HardFailure bool     `json:"hard_failure"`
		Metrics     struct {
			Blur    float64 `json:"blur"`
			LumaAvg float64 `json:"luma_avg"`
			Motion  float64 `json:"motion"`
		} `json:"metrics"`
	} `json:"rejected_segments"`
	Analysis struct {
		QualityProfile string `json:"quality_profile"`
		ShakeTreatment string `json:"shake_treatment"`
		Thresholds     struct {
			MaxBlur   float64 `json:"max_blur"`
			MinLuma   float64 `json:"min_luma"`
			MaxLuma   float64 `json:"max_luma"`
			MaxMotion float64 `json:"max_motion"`
		} `json:"thresholds"`
	} `json:"analysis"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Render struct {
		VideoCodec     string `json:"video_codec"`
		VideoWidth     int    `json:"video_width"`
		VideoHeight    int    `json:"video_height"`
		VideoFrameRate int    `json:"video_frame_rate"`
	} `json:"render"`
}

func TestEditSkipsUnsupportedAndCorruptClipsWithWarnings(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "good.mp4", "broken-corrupt.mp4", "notes.txt")
	toolDir := createEditTools(t)
	env := append(os.Environ(), "PATH="+toolDir)

	planPath := filepath.Join(sourceDir, "..", "source-edit.plan.json")
	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	for _, want := range []string{
		"warning: skipping",
		"broken-corrupt.mp4",
		"notes.txt",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}

	plan := decodeEditPlan(t, filepath.Clean(planPath))
	if len(plan.Segments) != 1 || filepath.Base(plan.Segments[0].SourcePath) != "good.mp4" {
		t.Fatalf("segments = %+v, want only good.mp4", plan.Segments)
	}
	reasons := map[string]string{}
	for _, skip := range plan.Skipped {
		reasons[filepath.Base(skip.Path)] = skip.Reason
	}
	if reasons["notes.txt"] != "unsupported file type" {
		t.Errorf("notes.txt reason = %q, want unsupported file type", reasons["notes.txt"])
	}
	if reasons["broken-corrupt.mp4"] == "" {
		t.Errorf("expected recorded reason for broken-corrupt.mp4, got %+v", plan.Skipped)
	}
}

func TestEditFailsWhenUsableFootageBelowFloor(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "one-corrupt.mp4", "two-corrupt.mov")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 1 {
		t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "less than five seconds of usable footage") {
		t.Errorf("output does not report the five second floor:\n%s", output)
	}
}

func TestEditRecordsAnalysisSettingsAndDefaults(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if plan.Analysis.QualityProfile != "balanced" {
		t.Errorf("default quality profile = %q, want balanced", plan.Analysis.QualityProfile)
	}
	if plan.Analysis.ShakeTreatment != "reject" {
		t.Errorf("default shake treatment = %q, want reject", plan.Analysis.ShakeTreatment)
	}
	if plan.Analysis.Thresholds.MaxBlur == 0 || plan.Analysis.Thresholds.MinLuma == 0 {
		t.Errorf("thresholds not recorded in plan: %+v", plan.Analysis.Thresholds)
	}
	if len(plan.Segments) != 1 {
		t.Fatalf("segments = %+v, want one eligible candidate", plan.Segments)
	}
	seg := plan.Segments[0]
	if seg.Metrics.LumaAvg == 0 {
		t.Errorf("segment metrics not retained: %+v", seg.Metrics)
	}
}

func TestEditQualityProfileSelectsStrictThresholds(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--quality-profile", "strict")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if plan.Analysis.QualityProfile != "strict" {
		t.Errorf("quality profile = %q, want strict", plan.Analysis.QualityProfile)
	}
	if plan.Analysis.Thresholds.MaxBlur != 10 {
		t.Errorf("strict MaxBlur = %v, want 10", plan.Analysis.Thresholds.MaxBlur)
	}
}

func TestEditRejectsInvalidQualityProfile(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--quality-profile", "nonsense")
	if status != 1 {
		t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "quality profile") {
		t.Errorf("output does not explain invalid quality profile:\n%s", output)
	}
}

func TestEditSubdividesLongShotIntoCandidates(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "long-action.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if len(plan.Segments) < 2 {
		t.Fatalf("long shot produced %d candidates, want it subdivided into several", len(plan.Segments))
	}
	for _, seg := range plan.Segments {
		length := seg.EndSecond - seg.StartSecond
		if length < 1.0 {
			t.Errorf("candidate %.2f-%.2f shorter than one second", seg.StartSecond, seg.EndSecond)
		}
		if length > 8.01 {
			t.Errorf("candidate %.2f-%.2f longer than eight seconds", seg.StartSecond, seg.EndSecond)
		}
	}
}

func TestEditCollapsesLongStaticShotToRepresentative(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "long-static.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if len(plan.Segments) != 1 {
		t.Fatalf("long static shot produced %d candidates, want a single representative", len(plan.Segments))
	}
}

func TestEditSplitsSceneCutsIntoSeparateCandidates(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "twoshots.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if len(plan.Segments) != 2 {
		t.Fatalf("scene cut produced %d candidates, want two shots", len(plan.Segments))
	}
}

func TestEditRejectsHardFailuresWithReasons(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "good.mp4", "blurry-bad.mp4", "dark-bad.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if len(plan.Segments) != 1 || filepath.Base(plan.Segments[0].SourcePath) != "good.mp4" {
		t.Fatalf("segments = %+v, want only good.mp4 eligible", plan.Segments)
	}
	rejectedBases := map[string][]string{}
	for _, rej := range plan.Rejected {
		if !rej.HardFailure {
			t.Errorf("rejection %+v should be a hard failure", rej)
		}
		rejectedBases[filepath.Base(rej.SourcePath)] = rej.Reasons
	}
	if len(rejectedBases["blurry-bad.mp4"]) == 0 {
		t.Errorf("blurry clip not rejected with reasons: %+v", plan.Rejected)
	}
	if len(rejectedBases["dark-bad.mp4"]) == 0 {
		t.Errorf("dark clip not rejected with reasons: %+v", plan.Rejected)
	}
}

func TestEditShakeRejectVersusStabilize(t *testing.T) {
	binary := buildCLI(t)

	t.Run("reject drops unstable footage", func(t *testing.T) {
		workingDir, sourceDir := makeEditSource(t, "good.mp4", "shaky-clip.mp4")
		planPath := filepath.Join(workingDir, "source-edit.plan.json")
		env := append(os.Environ(), "PATH="+createEditTools(t))

		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--shake", "reject")
		if status != 0 {
			t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
		}
		plan := decodeEditPlan(t, planPath)
		if len(plan.Segments) != 1 || filepath.Base(plan.Segments[0].SourcePath) != "good.mp4" {
			t.Fatalf("segments = %+v, want shaky footage rejected", plan.Segments)
		}
		var found bool
		for _, rej := range plan.Rejected {
			if filepath.Base(rej.SourcePath) == "shaky-clip.mp4" {
				found = true
			}
		}
		if !found {
			t.Errorf("shaky clip not recorded as rejected: %+v", plan.Rejected)
		}
	})

	t.Run("stabilize retains flagged footage", func(t *testing.T) {
		workingDir, sourceDir := makeEditSource(t, "good.mp4", "shaky-clip.mp4")
		planPath := filepath.Join(workingDir, "source-edit.plan.json")
		env := append(os.Environ(), "PATH="+createEditTools(t))

		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--shake", "stabilize")
		if status != 0 {
			t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
		}
		plan := decodeEditPlan(t, planPath)
		if plan.Analysis.ShakeTreatment != "stabilize" {
			t.Errorf("shake treatment = %q, want stabilize", plan.Analysis.ShakeTreatment)
		}
		var flagged bool
		for _, seg := range plan.Segments {
			if filepath.Base(seg.SourcePath) == "shaky-clip.mp4" {
				if !seg.Shaky || !seg.NeedsStabilization {
					t.Errorf("shaky segment not flagged for stabilization: %+v", seg)
				}
				flagged = true
			}
		}
		if !flagged {
			t.Errorf("shaky clip not retained under stabilize: %+v", plan.Segments)
		}
	})
}

func TestEditOrientationFollowsPortraitMajority(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-portrait.mp4", "b-portrait.mp4", "c-landscape.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if plan.Render.VideoWidth != 1080 || plan.Render.VideoHeight != 1920 {
		t.Errorf("portrait majority canvas = %dx%d, want 1080x1920", plan.Render.VideoWidth, plan.Render.VideoHeight)
	}
}

func TestEditAspectOverride(t *testing.T) {
	binary := buildCLI(t)

	t.Run("landscape overrides portrait footage", func(t *testing.T) {
		workingDir, sourceDir := makeEditSource(t, "a-portrait.mp4", "b-portrait.mp4")
		planPath := filepath.Join(workingDir, "source-edit.plan.json")
		env := append(os.Environ(), "PATH="+createEditTools(t))
		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--aspect", "landscape")
		if status != 0 {
			t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
		}
		plan := decodeEditPlan(t, planPath)
		if plan.Render.VideoWidth != 1920 || plan.Render.VideoHeight != 1080 {
			t.Errorf("canvas = %dx%d, want 1920x1080", plan.Render.VideoWidth, plan.Render.VideoHeight)
		}
	})

	t.Run("invalid aspect is rejected", func(t *testing.T) {
		workingDir, sourceDir := makeEditSource(t, "a-landscape.mp4", "b-landscape.mp4")
		env := append(os.Environ(), "PATH="+createEditTools(t))
		status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--aspect", "square")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "invalid aspect") {
			t.Errorf("output does not report invalid aspect:\n%s", output)
		}
	})
}

func TestEditOrientationTieRequiresAspect(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-portrait.mp4", "b-landscape.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 1 {
		t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "pass --aspect landscape|portrait") {
		t.Errorf("output does not require an explicit aspect:\n%s", output)
	}
}

func TestEditRejectsUnsupportedFrameRate(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-landscape.mp4", "b-landscape.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--fps", "48")
	if status == 0 {
		t.Fatalf("status = %d, want non-zero\noutput:\n%s", status, output)
	}
	if !strings.Contains(output, "48") {
		t.Errorf("output does not report the rejected frame rate:\n%s", output)
	}
}

func TestEditHonorsFrameRateOverride(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-landscape.mp4", "b-landscape.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--fps", "60")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if plan.Render.VideoFrameRate != 60 {
		t.Errorf("frame rate = %d, want 60", plan.Render.VideoFrameRate)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	if !strings.Contains(string(log), "fps=60") {
		t.Errorf("ffmpeg filter does not set fps=60:\n%s", log)
	}
}

func TestEditRendersBlurredPaddingAndRec709(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-landscape.mp4", "b-landscape.mp4")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	for _, want := range []string{
		"boxblur",
		"overlay=(W-w)/2:(H-h)/2",
		"-color_primaries bt709",
		"-color_trc bt709",
		"-colorspace bt709",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("ffmpeg invocation missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(string(log), "pad=1920:1080") {
		t.Errorf("expected blurred padding to replace black pad:\n%s", log)
	}
}

func TestEditTonemapsHDRFootage(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-hdr-landscape.mp4", "b-landscape.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	if !strings.Contains(string(log), "tonemap") {
		t.Errorf("ffmpeg invocation does not tone map HDR footage:\n%s", log)
	}
	plan := decodeEditPlan(t, planPath)
	var hdrPersisted bool
	for _, segment := range plan.Segments {
		if strings.Contains(segment.SourcePath, "hdr") && segment.IsHDR {
			hdrPersisted = true
		}
	}
	if !hdrPersisted {
		t.Errorf("HDR flag not persisted for reproducible render:\n%+v", plan.Segments)
	}
}

func TestEditPrefersVideoToolboxEncoder(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "a-landscape.mp4", "b-landscape.mp4")
	planPath := filepath.Join(workingDir, "source-edit.plan.json")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_H264_ENCODER=both")

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}
	plan := decodeEditPlan(t, planPath)
	if plan.Render.VideoCodec != "h264_videotoolbox" {
		t.Errorf("video codec = %q, want h264_videotoolbox when both encoders exist", plan.Render.VideoCodec)
	}
}

func TestRenderRebuildsFinishedVideoOffline(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "clips")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	writeTestFile(t, filepath.Join(sourceDir, "a-earlier.mov"), "alpha-source-bytes")
	writeTestFile(t, filepath.Join(sourceDir, "b-later.mp4"), "bravo-source-bytes")

	tools := createEditTools(t)
	ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
	outputPath := filepath.Join(workingDir, "story.mp4")
	planPath := filepath.Join(workingDir, "story.plan.json")
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--output", outputPath)
	if status != 0 {
		t.Fatalf("edit failed: %d\n%s", status, output)
	}

	// Prove render is offline: delete the Finished Video and the ffmpeg log,
	// then rerender purely from the saved Edit Plan.
	if err := os.Remove(outputPath); err != nil {
		t.Fatalf("remove Finished Video: %v", err)
	}
	if err := os.Remove(ffmpegLog); err != nil {
		t.Fatalf("remove ffmpeg log: %v", err)
	}
	probeCalls := filepath.Join(workingDir, "render-ffprobe.calls")
	renderEnv := append(env, "AVE_TEST_FFPROBE_CALLS="+probeCalls)

	status, output = runCLIInDir(t, binary, workingDir, renderEnv, "render", planPath)
	if status != 0 {
		t.Fatalf("render failed: %d\n%s", status, output)
	}
	if !strings.Contains(output, "Finished Video: "+outputPath) {
		t.Errorf("render did not report the Finished Video path:\n%s", output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("render did not recreate the Finished Video: %v", err)
	}
	if info, err := os.Stat(outputPath); err == nil && info.Mode().Perm() != 0o644 {
		t.Errorf("Finished Video mode = %v, want 0644", info.Mode().Perm())
	}
	if _, err := os.Stat(probeCalls); !errors.Is(err, os.ErrNotExist) {
		data, _ := os.ReadFile(probeCalls)
		t.Errorf("render performed analysis (invoked ffprobe):\n%s", data)
	}
	ffmpegArgs, err := os.ReadFile(ffmpegLog)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	for _, want := range []string{"-map_metadata -1", "-c:v libx264", "-f mp4"} {
		if !strings.Contains(string(ffmpegArgs), want) {
			t.Errorf("render ffmpeg invocation missing %q:\n%s", want, ffmpegArgs)
		}
	}
	if !strings.Contains(string(ffmpegArgs), ".ave-render-") {
		t.Errorf("render did not encode into a temporary file:\n%s", ffmpegArgs)
	}
}

func TestRenderDetectsMissingAndChangedSources(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, "clips")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("create source folder: %v", err)
	}
	missingSource := filepath.Join(sourceDir, "a-earlier.mov")
	changedSource := filepath.Join(sourceDir, "b-later.mp4")
	writeTestFile(t, missingSource, "alpha-source-bytes")
	writeTestFile(t, changedSource, "bravo-source-bytes")

	tools := createEditTools(t)
	ffmpegLog := filepath.Join(workingDir, "ffmpeg.log")
	outputPath := filepath.Join(workingDir, "story.mp4")
	planPath := filepath.Join(workingDir, "story.plan.json")
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--output", outputPath, "--plan-only")
	if status != 0 {
		t.Fatalf("edit failed: %d\n%s", status, output)
	}

	if err := os.Remove(missingSource); err != nil {
		t.Fatalf("remove Source Clip: %v", err)
	}
	writeTestFile(t, changedSource, "bravo-source-bytes-CHANGED")

	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath)
	if status != 1 {
		t.Fatalf("expected render failure, got %d\n%s", status, output)
	}
	if !strings.Contains(output, "a-earlier.mov") || !strings.Contains(output, "missing") {
		t.Errorf("render did not report the missing Source Clip:\n%s", output)
	}
	if !strings.Contains(output, "b-later.mp4") || !strings.Contains(output, "changed") {
		t.Errorf("render did not report the changed Source Clip:\n%s", output)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("render produced a Finished Video despite source problems")
	}
}

func TestRenderRelocatesSourcesWithMediaRoot(t *testing.T) {
	binary := buildCLI(t)
	projectDir := t.TempDir()
	sourceA := filepath.Join(projectDir, "a-earlier.mov")
	sourceB := filepath.Join(projectDir, "b-later.mp4")
	writeTestFile(t, sourceA, "alpha-source-bytes")
	writeTestFile(t, sourceB, "bravo-source-bytes")

	tools := createEditTools(t)
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	outputPath := filepath.Join(projectDir, "story.mp4")
	planPath := filepath.Join(projectDir, "story.plan.json")
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, projectDir, env, "edit", projectDir, "--output", outputPath, "--plan-only")
	if status != 0 {
		t.Fatalf("edit failed: %d\n%s", status, output)
	}

	newRoot := t.TempDir()
	copyTestFile(t, filepath.Join(newRoot, "a-earlier.mov"), sourceA)
	copyTestFile(t, filepath.Join(newRoot, "b-later.mp4"), sourceB)
	if err := os.Remove(sourceA); err != nil {
		t.Fatalf("remove Source Clip: %v", err)
	}
	if err := os.Remove(sourceB); err != nil {
		t.Fatalf("remove Source Clip: %v", err)
	}

	status, output = runCLIInDir(t, binary, projectDir, env, "render", planPath)
	if status != 1 {
		t.Fatalf("expected failure without media root, got %d\n%s", status, output)
	}

	status, output = runCLIInDir(t, binary, projectDir, env, "render", planPath, "--media-root", newRoot)
	if status != 0 {
		t.Fatalf("relocation render failed: %d\n%s", status, output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("relocated render did not create the Finished Video: %v", err)
	}

	if err := os.Remove(outputPath); err != nil {
		t.Fatalf("remove Finished Video: %v", err)
	}
	writeTestFile(t, filepath.Join(newRoot, "b-later.mp4"), "bravo-source-bytes-TAMPERED")
	status, output = runCLIInDir(t, binary, projectDir, env, "render", planPath, "--media-root", newRoot)
	if status != 1 {
		t.Fatalf("expected failure for tampered relocated source, got %d\n%s", status, output)
	}
	if !strings.Contains(output, "b-later.mp4") || !strings.Contains(output, "changed") {
		t.Errorf("render did not report the tampered relocated Source Clip:\n%s", output)
	}
}

func TestRenderRequiresForceToReplaceFinishedVideo(t *testing.T) {
	binary := buildCLI(t)
	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, "clip.mov"), "only-source-bytes")

	tools := createEditTools(t)
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	outputPath := filepath.Join(projectDir, "story.mp4")
	planPath := filepath.Join(projectDir, "story.plan.json")
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, projectDir, env, "edit", projectDir, "--output", outputPath)
	if status != 0 {
		t.Fatalf("edit failed: %d\n%s", status, output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("edit did not create the Finished Video: %v", err)
	}

	status, output = runCLIInDir(t, binary, projectDir, env, "render", planPath)
	if status != 1 {
		t.Fatalf("expected overwrite refusal, got %d\n%s", status, output)
	}
	if !strings.Contains(output, "refusing to overwrite existing artifact") || !strings.Contains(output, "--force") {
		t.Errorf("overwrite refusal was not actionable:\n%s", output)
	}

	if err := os.WriteFile(outputPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("stage stale Finished Video: %v", err)
	}
	status, output = runCLIInDir(t, binary, projectDir, env, "render", planPath, "--force")
	if status != 0 {
		t.Fatalf("forced render failed: %d\n%s", status, output)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read Finished Video: %v", err)
	}
	if string(data) != "finished video" {
		t.Errorf("forced render did not replace the Finished Video: %q", data)
	}
}

func TestRenderRemovesIncompleteOutputOnFailure(t *testing.T) {
	binary := buildCLI(t)
	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, "clip.mov"), "only-source-bytes")

	tools := createEditTools(t)
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	outputPath := filepath.Join(projectDir, "story.mp4")
	planPath := filepath.Join(projectDir, "story.plan.json")
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "AVE_TEST_FFMPEG_LOG="+ffmpegLog)

	status, output := runCLIInDir(t, binary, projectDir, env, "edit", projectDir, "--output", outputPath, "--plan-only")
	if status != 0 {
		t.Fatalf("edit failed: %d\n%s", status, output)
	}

	failEnv := append(env, "AVE_TEST_FFMPEG_FAIL=1")
	status, output = runCLIInDir(t, binary, projectDir, failEnv, "render", planPath)
	if status != 1 {
		t.Fatalf("expected render failure, got %d\n%s", status, output)
	}
	if !strings.Contains(output, "render failed") {
		t.Errorf("render failure was not reported:\n%s", output)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("incomplete Finished Video was left behind")
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatalf("read project folder: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ave-render-") {
			t.Errorf("temporary render artifact left behind: %s", entry.Name())
		}
	}
}

func TestRenderRejectsInvalidInputs(t *testing.T) {
	binary := buildCLI(t)
	workingDir := t.TempDir()
	badVersion := filepath.Join(workingDir, "bad.plan.json")
	writeTestFile(t, badVersion, `{"version":"999","finished_video":"out.mp4","selected_segments":[{"source_path":"x.mov","start_seconds":0,"end_seconds":1}]}`)
	malformed := filepath.Join(workingDir, "malformed.plan.json")
	writeTestFile(t, malformed, "not json")

	validRender := `"render":{"container":"mp4","video_codec":"libx264","audio_codec":"aac","video_width":1280,"video_height":720,"video_frame_rate":30,"pixel_format":"yuv420p","audio_sample_rate":48000,"audio_channels":2,"audio_channel_layout":"stereo","fast_start":true}`
	incompleteRender := filepath.Join(workingDir, "incomplete-render.plan.json")
	writeTestFile(t, incompleteRender, `{"version":"1","finished_video":"out.mp4","selected_segments":[{"source_path":"x.mov","source_fingerprint":"v1:1:aa","start_seconds":0,"end_seconds":1}],"render":{}}`)
	emptyRange := filepath.Join(workingDir, "empty-range.plan.json")
	writeTestFile(t, emptyRange, `{"version":"1","finished_video":"out.mp4","selected_segments":[{"source_path":"x.mov","source_fingerprint":"v1:1:aa","start_seconds":2,"end_seconds":2}],`+validRender+`}`)
	missingFingerprint := filepath.Join(workingDir, "missing-fingerprint.plan.json")
	writeTestFile(t, missingFingerprint, `{"version":"1","finished_video":"out.mp4","selected_segments":[{"source_path":"x.mov","start_seconds":0,"end_seconds":1}],`+validRender+`}`)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput string
	}{
		{"unknown option", []string{"render", "plan.json", "--unknown"}, 2, `unknown render option "--unknown"`},
		{"media-root requires value", []string{"render", "plan.json", "--media-root"}, 2, "--media-root requires a folder path"},
		{"single plan only", []string{"render", "a.json", "b.json"}, 2, "render accepts one Edit Plan"},
		{"missing plan path", []string{"render"}, 2, "missing Edit Plan path"},
		{"missing plan file", []string{"render", filepath.Join(workingDir, "nope.plan.json")}, 1, "open Edit Plan"},
		{"malformed plan", []string{"render", malformed}, 1, "decode Edit Plan"},
		{"unsupported version", []string{"render", badVersion}, 1, "unsupported Edit Plan version"},
		{"incomplete render settings", []string{"render", incompleteRender}, 1, "incomplete Edit Plan render settings"},
		{"empty segment range", []string{"render", emptyRange}, 1, "empty range"},
		{"missing source fingerprint", []string{"render", missingFingerprint}, 1, "missing a source fingerprint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLIInDir(t, binary, workingDir, os.Environ(), test.args...)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", status, test.wantStatus, output)
			}
			if !strings.Contains(output, test.wantOutput) {
				t.Errorf("output does not contain %q:\n%s", test.wantOutput, output)
			}
		})
	}
}

func TestCLICommandContracts(t *testing.T) {
	binary := buildCLI(t)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput []string
	}{
		{
			name:       "root help lists commands",
			args:       []string{"--help"},
			wantStatus: 0,
			wantOutput: []string{"Usage: ave", "edit", "render", "doctor"},
		},
		{
			name:       "no arguments shows root help",
			wantStatus: 0,
			wantOutput: []string{"Usage: ave", "doctor"},
		},
		{
			name:       "edit help shows source argument",
			args:       []string{"edit", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave edit <folder>"},
		},
		{
			name:       "render help shows plan argument",
			args:       []string{"render", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave render <plan.json>"},
		},
		{
			name:       "doctor help shows output modes",
			args:       []string{"doctor", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave doctor", "--verbose", "--quiet"},
		},
		{
			name:       "edit requires source folder",
			args:       []string{"edit"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave edit <folder>"},
		},
		{
			name:       "render requires edit plan",
			args:       []string{"render"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave render <plan.json>"},
		},
		{
			name:       "doctor rejects positional arguments",
			args:       []string{"doctor", "unexpected"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave doctor"},
		},
		{
			name:       "unknown command is rejected",
			args:       []string{"unknown"},
			wantStatus: 2,
			wantOutput: []string{"unknown command", "Usage: ave"},
		},
		{
			name:       "verbose and quiet conflict",
			args:       []string{"doctor", "--verbose", "--quiet"},
			wantStatus: 2,
			wantOutput: []string{"--verbose and --quiet cannot be used together"},
		},
		{
			name:       "quiet mode still reports command errors",
			args:       []string{"edit", "clips", "--quiet"},
			wantStatus: 1,
			wantOutput: []string{"edit failed"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLI(t, binary, test.args...)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d\noutput:\n%s", status, test.wantStatus, output)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("output does not contain %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestDoctorReportsReadyDependencies(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createFakeTools(t, true)
	server := newFakeLMStudio(t, true)

	baseEnv := append(
		os.Environ(),
		"PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AVE_LM_STUDIO_URL="+server.URL,
		"AVE_CONFIG_DIR="+filepath.Join(t.TempDir(), "config"),
		"AVE_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)

	tests := []struct {
		name       string
		args       []string
		wantOutput []string
		wantEmpty  bool
	}{
		{
			name: "normal output",
			args: []string{"doctor"},
			wantOutput: []string{
				"[ok] ffmpeg",
				"[ok] ffprobe",
				"[ok] LM Studio",
				"[ok] whisper.cpp",
				"[ok] aubio",
				"All required dependencies are ready.",
			},
		},
		{
			name: "verbose output",
			args: []string{"doctor", "--verbose"},
			wantOutput: []string{
				"vision model: local/vision-model",
				"config directory:",
				"cache directory:",
				toolDir,
			},
		},
		{
			name:      "quiet success",
			args:      []string{"doctor", "--quiet"},
			wantEmpty: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLIWithEnv(t, binary, baseEnv, test.args...)
			if status != 0 {
				t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
			}
			if test.wantEmpty && output != "" {
				t.Fatalf("output = %q, want empty", output)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("output does not contain %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestDoctorReportsActionableFailures(t *testing.T) {
	binary := buildCLI(t)

	t.Run("missing helper tools", func(t *testing.T) {
		toolDir := createFakeTools(t, false)
		server := newFakeLMStudio(t, true)
		env := doctorEnv(t, toolDir, server.URL)

		status, output := runCLIWithEnv(t, binary, env, "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		for _, want := range []string{
			"[missing] whisper.cpp: not found in PATH",
			"brew install whisper-cpp",
			"[missing] aubio: not found in PATH",
			"brew install aubio",
		} {
			if !strings.Contains(output, want) {
				t.Errorf("output does not contain %q:\n%s", want, output)
			}
		}
	})

	t.Run("quiet output contains failures only", func(t *testing.T) {
		toolDir := createFakeTools(t, false)
		server := newFakeLMStudio(t, true)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor", "--quiet")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if strings.Contains(output, "[ok]") || strings.Contains(output, "All required") {
			t.Errorf("quiet output includes successful checks:\n%s", output)
		}
		if !strings.Contains(output, "[missing] whisper.cpp") {
			t.Errorf("quiet output omits failure:\n%s", output)
		}
	})

	t.Run("ffmpeg lacks required capability", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		writeExecutable(t, toolDir, "ffmpeg", `#!/bin/sh
	case "$*" in
	  *"-filters"*)
	    echo "... scdet V->V"
	    echo "... blurdetect_opencl V->V"
	    echo "... xfade_opencl VV->V"
	    ;;
	  *"-encoders"*)
	    echo "V....D h264_videotoolbox_extra"
	    echo "A....D aac_at"
	    ;;
	  *) echo "ffmpeg version 7.1.1" ;;
	esac
	`)
		server := newFakeLMStudio(t, true)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] ffmpeg: missing required capabilities") {
			t.Errorf("output does not identify incompatible ffmpeg:\n%s", output)
		}
		if !strings.Contains(output, "filter blurdetect") {
			t.Errorf("output does not identify missing filter:\n%s", output)
		}
	})

	t.Run("LM Studio has no vision model", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := newFakeLMStudio(t, false)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: no compatible vision model installed") {
			t.Errorf("output does not identify missing vision model:\n%s", output)
		}
	})

	t.Run("remote model endpoint is rejected", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		env := doctorEnv(t, toolDir, "https://models.example.com")

		status, output := runCLIWithEnv(t, binary, env, "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: server URL is not loopback") {
			t.Errorf("output does not reject remote endpoint:\n%s", output)
		}
	})

	t.Run("LM Studio returns invalid JSON", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			if _, err := writer.Write([]byte("not-json")); err != nil {
				t.Errorf("write invalid response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: model API returned invalid JSON") {
			t.Errorf("output does not identify invalid model response:\n%s", output)
		}
	})

	t.Run("LM Studio returns HTTP error", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: model API returned HTTP 503") {
			t.Errorf("output does not identify model server error:\n%s", output)
		}
	})
}

func buildCLI(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "ave")
	args := []string{"build"}
	if os.Getenv("AVE_COVER_DIR") != "" {
		args = append(args, "-race", "-cover")
	}
	args = append(args, "-o", binary, "./cmd/ave")
	command := exec.Command("go", args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output.String())
	}
	return binary
}

func runCLI(t *testing.T, binary string, args ...string) (int, string) {
	t.Helper()
	return runCLIWithEnv(t, binary, os.Environ(), args...)
}

func runCLIWithEnv(t *testing.T, binary string, env []string, args ...string) (int, string) {
	t.Helper()
	return runCLIInDir(t, binary, "", env, args...)
}

func runCLIInDir(t *testing.T, binary, dir string, env []string, args ...string) (int, string) {
	t.Helper()

	command := exec.Command(binary, args...)
	command.Dir = dir
	if coverageDir := os.Getenv("AVE_COVER_DIR"); coverageDir != "" {
		env = append(env, "GOCOVERDIR="+coverageDir)
	}
	command.Env = env
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err == nil {
		return 0, output.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run CLI: %v", err)
	}
	return exitErr.ExitCode(), output.String()
}

func createEditTools(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeExecutable(t, dir, "ffprobe", `#!/bin/sh
if [ -n "$AVE_TEST_FFPROBE_CALLS" ]; then echo call >> "$AVE_TEST_FFPROBE_CALLS"; fi
case "$AVE_TEST_FFPROBE_MODE" in
  fail) exit 1 ;;
  malformed) printf 'not-json\n'; exit ;;
  invalid-duration) printf '{"format":{"duration":"0"}}\n'; exit ;;
  malformed-duration) printf '{"format":{"duration":"unknown"}}\n'; exit ;;
  missing-capture) printf '{"streams":[{"width":1920,"height":1080}],"format":{"duration":"6.0","tags":{}}}\n'; exit ;;
esac
for source do :; done
case "$source" in
  *corrupt*) echo "ffprobe: corrupt input" >&2; exit 1 ;;
esac
case "$source" in
  *earlier*) captured="2026-01-01T10:00:00Z" ;;
  *later*) captured="2026-01-01T11:00:00Z" ;;
  *) captured="2026-01-01T10:00:00Z" ;;
esac
case "$source" in
  *portrait*) width=1080; height=1920 ;;
  *) width=1920; height=1080 ;;
esac
case "$source" in
  *hdr*) transfer="smpte2084" ;;
  *) transfer="bt709" ;;
esac
case "$source" in
  *long*) duration="30.0" ;;
  *) duration="6.0" ;;
esac
printf '{"streams":[{"width":%s,"height":%s,"color_transfer":"%s"}],"format":{"duration":"%s","tags":{"creation_time":"%s"}}}\n' "$width" "$height" "$transfer" "$duration" "$captured"
`)
	writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$*" in
  *"-encoders"*)
    if [ "$AVE_TEST_H264_ENCODER" = "videotoolbox" ]; then
      echo "V....D h264_videotoolbox"
    elif [ "$AVE_TEST_H264_ENCODER" = "both" ]; then
      echo "V....D libx264"
      echo "V....D h264_videotoolbox"
    else
      echo "V....D libx264"
    fi
    echo "A....D aac"
    exit
    ;;
esac
# Analysis metadata passes (scene detection + per-candidate metrics) write to a
# metadata=print sink and use the null muxer. Detect the sink path and source,
# then emit deterministic detector output keyed on filename markers.
metafile=""
input=""
prev=""
for a in "$@"; do
  case "$a" in
    *metadata=print:file=*) metafile="${a##*metadata=print:file=}" ;;
  esac
  if [ "$prev" = "-i" ]; then input="$a"; fi
  prev="$a"
done
if [ -n "$metafile" ]; then
  case "$*" in
    *scdet*)
      case "$input" in
        *static*) m="0.2" ;;
        *shaky*) m="40.0" ;;
        *) m="5.0" ;;
      esac
      {
        printf 'frame:0 pts_time:0.0\n'
        printf 'lavfi.scd.mafd=%s\n' "$m"
        printf 'lavfi.scd.score=%s\n' "$m"
        printf 'frame:1 pts_time:2.0\n'
        printf 'lavfi.scd.mafd=%s\n' "$m"
        printf 'lavfi.scd.score=%s\n' "$m"
        printf 'frame:2 pts_time:4.0\n'
        printf 'lavfi.scd.mafd=%s\n' "$m"
        printf 'lavfi.scd.score=%s\n' "$m"
        case "$input" in
          *twoshots*)
            printf 'frame:3 pts_time:3.0\n'
            printf 'lavfi.scd.mafd=32.0\n'
            printf 'lavfi.scd.score=32.0\n'
            printf 'lavfi.scd.time=3.0\n'
            ;;
        esac
        case "$input" in
          *long*)
            printf 'frame:4 pts_time:10.0\n'
            printf 'lavfi.scd.mafd=%s\n' "$m"
            printf 'frame:5 pts_time:20.0\n'
            printf 'lavfi.scd.mafd=%s\n' "$m"
            printf 'frame:6 pts_time:28.0\n'
            printf 'lavfi.scd.mafd=%s\n' "$m"
            ;;
        esac
      } > "$metafile"
      exit 0
      ;;
    *blurdetect*)
      case "$input" in
        *blurry*) blur="30.0" ;;
        *) blur="5.0" ;;
      esac
      case "$input" in
        *dark*) yavg="10.0" ;;
        *bright*) yavg="250.0" ;;
        *) yavg="120.0" ;;
      esac
      {
        printf 'frame:0 pts_time:0.0\n'
        printf 'lavfi.blur=%s\n' "$blur"
        printf 'lavfi.signalstats.YAVG=%s\n' "$yavg"
        printf 'lavfi.signalstats.YMIN=6.0\n'
        printf 'lavfi.signalstats.YMAX=240.0\n'
      } > "$metafile"
      exit 0
      ;;
    *ametadata*)
      case "$input" in
        *silent*) : ;;
        *)
          {
            printf 'frame:0 pts_time:0.0\n'
            printf 'lavfi.astats.1.RMS_level=-20.0\n'
            printf 'lavfi.astats.1.Peak_level=-10.0\n'
          } > "$metafile"
          ;;
      esac
      exit 0
      ;;
  esac
  exit 0
fi
printf '%s\n' "$*" > "$AVE_TEST_FFMPEG_LOG"
for output do :; done
if [ -n "$AVE_TEST_FFMPEG_FAIL" ]; then
  printf 'partial' > "$output"
  echo "ffmpeg: induced failure" >&2
  exit 1
fi
printf 'finished video' > "$output"
`)
	return dir
}

func createFakeTools(t *testing.T, includeOptional bool) string {
	t.Helper()

	dir := t.TempDir()
	writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$*" in
  *"-filters"*)
    echo "... blurdetect V->V"
    echo "... scdet V->V"
    echo "... vidstabdetect V->V"
    echo "... vidstabtransform V->V"
    echo "... silencedetect A->A"
    echo "... ebur128 A->N"
    echo "... xfade VV->V"
    echo "... acrossfade AA->A"
    ;;
  *"-encoders"*)
    echo "V....D h264_videotoolbox"
    echo "V....D libx264"
    echo "A....D aac"
    ;;
  *)
    echo "ffmpeg version 7.1.1"
    ;;
esac
`)
	writeExecutable(t, dir, "ffprobe", "#!/bin/sh\necho 'ffprobe version 7.1.1'\n")
	if includeOptional {
		writeExecutable(t, dir, "whisper-cli", "#!/bin/sh\necho 'whisper.cpp 1.7.0'\n")
		writeExecutable(t, dir, "aubio", "#!/bin/sh\necho 'aubio 0.4.9'\n")
	}
	return dir
}

func doctorEnv(t *testing.T, toolDir, serverURL string) []string {
	t.Helper()

	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "AVE_LM_STUDIO_URL=") ||
			strings.HasPrefix(entry, "AVE_CONFIG_DIR=") ||
			strings.HasPrefix(entry, "AVE_CACHE_DIR=") {
			continue
		}
		env = append(env, entry)
	}
	return append(
		env,
		"PATH="+toolDir,
		"AVE_LM_STUDIO_URL="+serverURL,
		"AVE_CONFIG_DIR="+filepath.Join(t.TempDir(), "config"),
		"AVE_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func copyTestFile(t *testing.T, dst, src string) {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func newFakeLMStudio(t *testing.T, withVisionModel bool) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		models := `[]`
		if withVisionModel {
			models = `[{"type":"llm","key":"local/vision-model","capabilities":{"vision":true}}]`
		}
		if _, err := fmt.Fprintf(writer, `{"models":%s}`, models); err != nil {
			t.Errorf("write model response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
