package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dialoguePlan captures the #10 dialogue and source-audio provenance.
type dialoguePlan struct {
	Audio struct {
		Source   string `json:"source"`
		Dialogue *struct {
			Language       string `json:"language"`
			LanguageSource string `json:"language_source"`
			Ducking        string `json:"ducking"`
		} `json:"dialogue"`
		Continuity []struct {
			FromSegment int     `json:"from_segment"`
			ToSegment   int     `json:"to_segment"`
			Kind        string  `json:"kind"`
			Seconds     float64 `json:"seconds"`
		} `json:"continuity"`
	} `json:"audio"`
	Segments []struct {
		SourcePath string `json:"source_path"`
		Dialogue   []struct {
			StartSecond float64 `json:"start_seconds"`
			EndSecond   float64 `json:"end_seconds"`
			Text        string  `json:"text"`
		} `json:"dialogue"`
	} `json:"selected_segments"`
}

func decodeDialoguePlan(t *testing.T, planPath string) dialoguePlan {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan dialoguePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

func TestEditTranscribesDialogueAndDetectsLanguage(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeDialoguePlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Audio.Dialogue == nil {
		t.Fatalf("plan is missing dialogue settings")
	}
	if plan.Audio.Dialogue.Language != "en" {
		t.Errorf("detected language = %q, want en", plan.Audio.Dialogue.Language)
	}
	if plan.Audio.Dialogue.LanguageSource != "detected" {
		t.Errorf("language source = %q, want detected", plan.Audio.Dialogue.LanguageSource)
	}
	if plan.Audio.Dialogue.Ducking != "balanced" {
		t.Errorf("ducking = %q, want balanced (default)", plan.Audio.Dialogue.Ducking)
	}
	if len(plan.Segments) == 0 || len(plan.Segments[0].Dialogue) == 0 {
		t.Fatalf("expected per-segment dialogue spans, got %+v", plan.Segments)
	}
}

func TestEditHonorsLanguageOverride(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_WHISPER_LANG=en")

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--language", "es")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeDialoguePlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Audio.Dialogue == nil {
		t.Fatalf("plan is missing dialogue settings")
	}
	if plan.Audio.Dialogue.Language != "es" {
		t.Errorf("override language = %q, want es", plan.Audio.Dialogue.Language)
	}
	if plan.Audio.Dialogue.LanguageSource != "override" {
		t.Errorf("language source = %q, want override", plan.Audio.Dialogue.LanguageSource)
	}
}

func TestEditDucksMusicUnderDialogue(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "track.mp3")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--music", music, "--duck", "strong")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeDialoguePlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Audio.Dialogue == nil || plan.Audio.Dialogue.Ducking != "strong" {
		t.Fatalf("expected strong ducking in plan, got %+v", plan.Audio.Dialogue)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	for _, want := range []string{
		"sidechaincompress=threshold=0.050:ratio=8.0",
		"asplit=2[srckey][srcmix]",
		"amix=inputs=2:normalize=0",
		"track.mp3",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("ducked render is missing %q:\n%s", want, rendered)
		}
	}
}

func TestEditPreservesSourceAudioWithoutMusic(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	for _, want := range []string{
		"[0:a:0]asplit",
		"loudnorm=I=-16.0:TP=-1.5:LRA=11.0",
		"acrossfade=d=0.250000",
		"alimiter=limit=0.95",
		"-map [audio]",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("source-audio render is missing %q:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"sidechaincompress", "anullsrc"} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("no-music render should not contain %q:\n%s", unwanted, rendered)
		}
	}
}

func TestEditRecordsDialogueContinuity(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--plan-only")
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeDialoguePlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Audio.Continuity) == 0 {
		t.Fatalf("expected dialogue continuity across the two segments, got none")
	}
	cut := plan.Audio.Continuity[0]
	if cut.FromSegment != 0 || cut.ToSegment != 1 {
		t.Errorf("continuity bridges %d->%d, want 0->1", cut.FromSegment, cut.ToSegment)
	}
	if cut.Kind != "J-cut" && cut.Kind != "L-cut" {
		t.Errorf("continuity kind = %q, want J-cut or L-cut", cut.Kind)
	}
	if cut.Seconds <= 0 {
		t.Errorf("continuity seconds = %.3f, want positive", cut.Seconds)
	}
}

func TestEditMutesUnusableAudioSegment(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-silent.mp4", "02-clip.mp4")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeDialoguePlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Segments) != 2 {
		t.Fatalf("expected both segments kept despite bad audio, got %d", len(plan.Segments))
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	if !strings.Contains(rendered, "anullsrc") || !strings.Contains(rendered, "[sil_0]") {
		t.Errorf("unusable-audio segment should draw from a silent source:\n%s", rendered)
	}
}
