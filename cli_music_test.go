package ave_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// musicPlan captures the #9 Music Track provenance and per-segment cue snapping.
type musicPlan struct {
	Audio struct {
		Source string `json:"source"`
		Music  *struct {
			Path            string  `json:"path"`
			Fingerprint     string  `json:"fingerprint"`
			DurationSeconds float64 `json:"duration_seconds"`
			FadeOutSeconds  float64 `json:"fade_out_seconds"`
			Cues            struct {
				Beats    []float64 `json:"beats"`
				Onsets   []float64 `json:"onsets"`
				Phrases  []float64 `json:"phrases"`
				Sections []float64 `json:"sections"`
			} `json:"cues"`
		} `json:"music"`
	} `json:"audio"`
	Ranking *struct {
		TargetSeconds  float64 `json:"target_seconds"`
		SelectedSecond float64 `json:"selected_seconds"`
	} `json:"ranking"`
	Segments []struct {
		SourcePath string  `json:"source_path"`
		Start      float64 `json:"start_seconds"`
		End        float64 `json:"end_seconds"`
		MusicCue   string  `json:"music_cue"`
	} `json:"selected_segments"`
}

func decodeMusicPlan(t *testing.T, planPath string) musicPlan {
	t.Helper()
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read Edit Plan: %v", err)
	}
	var plan musicPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode Edit Plan: %v", err)
	}
	return plan
}

// writeMusicTrack creates a fake Music Track file whose name marks the length
// the fake ffprobe/aubio report (default 60s; "short" 6s; "long" 300s).
func writeMusicTrack(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("music-track-fixture"), 0o644); err != nil {
		t.Fatalf("write Music Track: %v", err)
	}
	return path
}

func TestEditRecordsMusicCuesAndProvenance(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "track.mp3")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--music", music)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeMusicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Audio.Source != "music" {
		t.Fatalf("audio source = %q, want music", plan.Audio.Source)
	}
	if plan.Audio.Music == nil {
		t.Fatal("plan is missing Music Track settings")
	}
	settings := plan.Audio.Music
	if filepath.Base(settings.Path) != "track.mp3" {
		t.Errorf("music path = %q, want a relative track.mp3", settings.Path)
	}
	if settings.Fingerprint == "" {
		t.Error("music fingerprint should be recorded for reproducibility")
	}
	if settings.DurationSeconds != 60.0 {
		t.Errorf("music duration = %.2f, want 60.00", settings.DurationSeconds)
	}
	if settings.FadeOutSeconds <= 0 {
		t.Error("music fade-out should be positive")
	}
	if len(settings.Cues.Beats) == 0 {
		t.Error("expected recorded beats")
	}
	if len(settings.Cues.Onsets) == 0 {
		t.Error("expected recorded onsets")
	}
	if len(settings.Cues.Phrases) == 0 {
		t.Error("expected recorded coarse phrase cues")
	}
	if len(settings.Cues.Sections) == 0 {
		t.Error("expected recorded coarse section cues")
	}
}

func TestEditSnapsCutsToMusicCues(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "track.mp3")
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--music", music)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeMusicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if len(plan.Segments) < 2 {
		t.Fatalf("expected at least two segments to have an internal cut, got %d", len(plan.Segments))
	}
	snapped := false
	for _, segment := range plan.Segments {
		switch segment.MusicCue {
		case "", "beat", "phrase", "section":
			if segment.MusicCue != "" {
				snapped = true
			}
		default:
			t.Errorf("segment has unexpected music cue %q", segment.MusicCue)
		}
	}
	if !snapped {
		t.Error("expected at least one cut to be aligned to a Music Cue")
	}
	// The last segment ends the video with the music fade and is never snapped.
	if last := plan.Segments[len(plan.Segments)-1]; last.MusicCue != "" {
		t.Errorf("last segment music cue = %q, want empty", last.MusicCue)
	}
}

func TestEditShortMusicShortensEdit(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "short.mp3") // 6s track
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--music", music)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeMusicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Ranking == nil {
		t.Fatal("plan is missing ranking provenance")
	}
	if plan.Ranking.TargetSeconds > 6.0+0.01 {
		t.Errorf("target = %.2f, want it clamped to the 6s track", plan.Ranking.TargetSeconds)
	}
	if plan.Ranking.SelectedSecond > 6.0+0.01 {
		t.Errorf("selected = %.2f, want it within the 6s track (never looped)", plan.Ranking.SelectedSecond)
	}
}

func TestEditRejectsDurationLongerThanMusic(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "short.mp3") // 6s track
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--music", music, "--duration", "60")
	if status == 0 {
		t.Fatalf("expected failure when the duration exceeds the track\noutput:\n%s", output)
	}
	if !strings.Contains(output, "longer than the Music Track") {
		t.Errorf("expected a clear too-long error, got:\n%s", output)
	}
}

func TestEditRendersMusicBedWithoutLooping(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "track.mp3")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--music", music)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	rendered := string(log)
	for _, want := range []string{"atrim=0:", "afade=t=out", "track.mp3", "[audio]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("music render is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "stream_loop") {
		t.Errorf("music must never be looped:\n%s", rendered)
	}
	if strings.Contains(rendered, "anullsrc") {
		t.Errorf("music render should not fall back to a silent bed:\n%s", rendered)
	}
}

func TestEditClampsEditToShortMusicTrack(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "tiny.mp3") // 3s track, shorter than one 6s clip
	env := append(os.Environ(), "PATH="+createEditTools(t))

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir, "--plan-only", "--music", music)
	if status != 0 {
		t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
	}

	plan := decodeMusicPlan(t, filepath.Join(workingDir, "source-edit.plan.json"))
	if plan.Ranking == nil {
		t.Fatal("plan is missing ranking provenance")
	}
	// The Finished Video must never outlast the Music Track, or its tail would
	// be silent and un-faded.
	if plan.Ranking.SelectedSecond > 3.0+0.01 {
		t.Errorf("selected = %.2f, want it clamped to the 3s track", plan.Ranking.SelectedSecond)
	}
	var edited float64
	for _, segment := range plan.Segments {
		if segment.End < segment.Start {
			t.Errorf("segment end %.2f precedes start %.2f", segment.End, segment.Start)
		}
		edited += segment.End - segment.Start
	}
	if edited > 3.0+0.01 {
		t.Errorf("edited footage = %.2fs, want it trimmed to the 3s track", edited)
	}
	if edited < 3.0-0.01 {
		t.Errorf("edited footage = %.2fs, want the full 3s track filled", edited)
	}
}

func TestRenderPlanReproducesMusicAndDetectsTamper(t *testing.T) {
	binary := buildCLI(t)
	workingDir, sourceDir := makeEditSource(t, "01-clip.mp4", "02-clip.mp4")
	music := writeMusicTrack(t, workingDir, "track.mp3")
	logPath := filepath.Join(workingDir, "ffmpeg.log")
	env := append(os.Environ(), "PATH="+createEditTools(t), "AVE_TEST_FFMPEG_LOG="+logPath)

	status, output := runCLIInDir(t, binary, workingDir, env, "edit", sourceDir, "--music", music)
	if status != 0 {
		t.Fatalf("edit status = %d, want 0\noutput:\n%s", status, output)
	}
	planPath := filepath.Join(workingDir, "source-edit.plan.json")

	// A clean rerender reproduces the music bed from the saved plan alone.
	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath, "--force")
	if status != 0 {
		t.Fatalf("render status = %d, want 0\noutput:\n%s", status, output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read FFmpeg log: %v", err)
	}
	if !strings.Contains(string(log), "afade=t=out") || strings.Contains(string(log), "stream_loop") {
		t.Errorf("rerender did not reproduce the faded, non-looped music bed:\n%s", log)
	}

	// Changing the Music Track invalidates the plan's reproducibility guarantee.
	if err := os.WriteFile(music, []byte("tampered-music-track-fixture"), 0o644); err != nil {
		t.Fatalf("tamper Music Track: %v", err)
	}
	status, output = runCLIInDir(t, binary, workingDir, env, "render", planPath, "--force")
	if status == 0 {
		t.Fatalf("expected a fingerprint failure after tampering\noutput:\n%s", output)
	}
	if !strings.Contains(output, "changed") {
		t.Errorf("expected a changed-track error, got:\n%s", output)
	}
}
