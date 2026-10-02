package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// encoderPlan captures the #17 encoder/quality provenance recorded in a plan.
type encoderPlan struct {
	Render struct {
		VideoCodec   string `json:"video_codec"`
		EncoderMode  string `json:"encoder_mode"`
		VideoCRF     int    `json:"video_crf"`
		VideoBitrate string `json:"video_bitrate"`
	} `json:"render"`
}

func decodeEncoderPlan(t *testing.T, planPath string) encoderPlan {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan encoderPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

func TestEditSoftwareEncoderUsesCRF(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	// "both" makes hardware available too, proving --encoder software overrides
	// the auto hardware preference.
	env := append(os.Environ(), "PATH="+createEditTools(t),
		"AVE_TEST_FFMPEG_LOG="+logPath, "AVE_TEST_H264_ENCODER=both")

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--encoder", "software")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeEncoderPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Render.VideoCodec != "libx264" {
		t.Errorf("video codec = %q, want libx264", plan.Render.VideoCodec)
	}
	if plan.Render.EncoderMode != "software" {
		t.Errorf("encoder mode = %q, want software", plan.Render.EncoderMode)
	}
	if plan.Render.VideoCRF <= 0 {
		t.Errorf("software CRF = %d, want positive", plan.Render.VideoCRF)
	}
	if plan.Render.VideoBitrate != "" {
		t.Errorf("software bitrate = %q, want empty", plan.Render.VideoBitrate)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	for _, want := range []string{"libx264", "-crf", "-preset"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render args missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "-b:v") {
		t.Errorf("software render should not set a bitrate:\n%s", rendered)
	}
}

func TestEditAutoEncoderPrefersHardware(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t),
		"AVE_TEST_FFMPEG_LOG="+logPath, "AVE_TEST_H264_ENCODER=both")

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeEncoderPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Render.VideoCodec != "h264_videotoolbox" {
		t.Errorf("video codec = %q, want h264_videotoolbox", plan.Render.VideoCodec)
	}
	if plan.Render.EncoderMode != "auto" {
		t.Errorf("encoder mode = %q, want auto", plan.Render.EncoderMode)
	}
	if plan.Render.VideoBitrate == "" {
		t.Error("hardware encoder should record an explicit bitrate")
	}
	if plan.Render.VideoCRF != 0 {
		t.Errorf("hardware CRF = %d, want 0", plan.Render.VideoCRF)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	for _, want := range []string{"h264_videotoolbox", "-b:v", "-maxrate", "-bufsize"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render args missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "-crf") {
		t.Errorf("hardware render should not set a CRF:\n%s", rendered)
	}
}

func TestEditAutoEncoderFallsBackToSoftware(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	// Default fake advertises only libx264.
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--encoder", "auto")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeEncoderPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Render.VideoCodec != "libx264" {
		t.Errorf("video codec = %q, want libx264 fallback", plan.Render.VideoCodec)
	}
	if plan.Render.VideoCRF <= 0 {
		t.Errorf("software fallback CRF = %d, want positive", plan.Render.VideoCRF)
	}
}

func TestEditHardwareEncoderFailsWithoutHardware(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	// Default fake advertises only libx264 (no hardware encoder).
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only", "--encoder", "hardware")
	if status == 0 {
		t.Fatalf("expected failure when hardware encoder is unavailable\noutput:\n%s", output)
	}
	if !strings.Contains(output, "hardware H.264 encoder") {
		t.Errorf("expected an actionable no-hardware error, got:\n%s", output)
	}
}

func TestEditRejectsInvalidEncoderFlag(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--encoder", "turbo")
	if status == 0 {
		t.Fatalf("expected failure for an invalid encoder value\noutput:\n%s", output)
	}
	if !strings.Contains(output, "invalid --encoder value") {
		t.Errorf("expected a clear invalid-value error, got:\n%s", output)
	}
}
