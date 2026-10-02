package ave_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepresentativeProjectCompletesUnattended proves AC4/AC5: a mixed,
// realistic project runs to a Finished Video without interaction, excludes
// obvious technical failures, avoids near-duplicates, aligns cuts to Music Cues
// without forcing a cut on every beat, and preserves the selected dialogue.
func TestRepresentativeProjectCompletesUnattended(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)

	// A representative spread: strong "hero" footage with dialogue, a dull but
	// usable clip, two obvious technical failures (blur, darkness), unstable
	// footage, and a pair of near-duplicates.
	names := []string{
		"01-hero-clip.mp4",
		"02-hero-clip.mp4",
		"03-boring-clip.mp4",
		"04-blurry-bad.mp4",
		"05-dark-bad.mp4",
		"06-shaky-clip.mp4",
		"07-dup.mp4",
		"08-dup.mp4",
	}
	workingDir, sourceDir := makeEditSource(t, names...)
	music := writeMusicTrack(t, workingDir, "soundtrack.mp3")
	outputPath := filepath.Join(workingDir, "story.mp4")
	env := append(os.Environ(), "PATH="+toolDir)

	// Unattended: every choice is supplied up front (intent + music), so the run
	// never needs to prompt.
	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir,
		"--output", outputPath,
		"--intent", "chronological",
		"--music", music,
	)
	if status != 0 {
		t.Fatalf("representative edit failed: %d\n%s", status, output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("Finished Video was not produced: %v", err)
	}

	planPath := filepath.Join(workingDir, "story.plan.json")
	plan := decodeEditPlan(t, planPath)

	selected := map[string]bool{}
	for _, segment := range plan.Segments {
		selected[filepath.Base(segment.SourcePath)] = true
	}
	if len(plan.Segments) == 0 {
		t.Fatalf("no segments were selected for a project with usable footage")
	}

	// Obvious technical failures are excluded and recorded as hard failures.
	rejected := map[string]bool{}
	for _, rej := range plan.Rejected {
		rejected[filepath.Base(rej.SourcePath)] = true
	}
	for _, bad := range []string{"04-blurry-bad.mp4", "05-dark-bad.mp4"} {
		if selected[bad] {
			t.Errorf("technical failure %s was selected", bad)
		}
		if !rejected[bad] {
			t.Errorf("technical failure %s was not rejected with a reason", bad)
		}
	}
	// Unstable footage is dropped under the default reject policy.
	if selected["06-shaky-clip.mp4"] {
		t.Errorf("unstable footage 06-shaky-clip.mp4 was selected under default reject")
	}

	// Near-duplicates: at most one of the paired clips survives.
	if selected["07-dup.mp4"] && selected["08-dup.mp4"] {
		t.Errorf("both near-duplicate clips were selected")
	}

	// Strong footage is chosen.
	if !selected["01-hero-clip.mp4"] && !selected["02-hero-clip.mp4"] {
		t.Errorf("no hero footage was selected: %v", selected)
	}

	// Selected dialogue is preserved.
	dialogue := decodeDialoguePlan(t, planPath)
	if dialogue.Audio.Dialogue == nil || dialogue.Audio.Dialogue.Language == "" {
		t.Errorf("plan did not record detected dialogue language")
	}
	withDialogue := 0
	for _, segment := range dialogue.Segments {
		if len(segment.Dialogue) > 0 {
			withDialogue++
		}
	}
	if withDialogue == 0 {
		t.Errorf("no selected segment preserved dialogue spans")
	}

	// Music Cues drive alignment without forcing a cut on every beat.
	musicPlan := decodeMusicPlan(t, planPath)
	if musicPlan.Audio.Music == nil {
		t.Fatalf("plan did not record Music Track provenance")
	}
	beats := musicPlan.Audio.Music.Cues.Beats
	if len(beats) == 0 {
		t.Fatalf("plan recorded no Music Cues")
	}
	if len(musicPlan.Segments) >= len(beats) {
		t.Errorf("selected %d segments for %d beats; cuts appear forced onto every beat",
			len(musicPlan.Segments), len(beats))
	}
	aligned := 0
	for _, segment := range musicPlan.Segments {
		if strings.TrimSpace(segment.MusicCue) != "" {
			aligned++
		}
	}
	if aligned == 0 {
		t.Errorf("no selected segment was aligned to a Music Cue")
	}
}

// TestRepresentativeProjectHonorsThematicIntent proves both Edit Intents work
// end to end on the same representative footage (AC4).
func TestRepresentativeProjectHonorsThematicIntent(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createEditTools(t)

	workingDir, sourceDir := makeEditSource(t,
		"01-hero-calm-clip.mp4",
		"02-hero-peak-clip.mp4",
		"03-boring-clip.mp4",
	)
	outputPath := filepath.Join(workingDir, "story.mp4")
	env := append(os.Environ(), "PATH="+toolDir)

	status, output := runCLIInDir(t, binary, workingDir, env,
		"edit", sourceDir,
		"--output", outputPath,
		"--intent", "thematic",
		"--theme", "sunrise adventure",
	)
	if status != 0 {
		t.Fatalf("thematic edit failed: %d\n%s", status, output)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("Finished Video was not produced: %v", err)
	}

	planPath := filepath.Join(workingDir, "story.plan.json")
	intent := decodeThematicPlan(t, planPath)
	if intent.EditIntent != "thematic" {
		t.Errorf("edit_intent = %q, want thematic", intent.EditIntent)
	}
}
