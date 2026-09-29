package edit

import (
	"fmt"
	"sort"
	"strings"
)

const transitionCut = "cut"

// Edit Intent values ave can produce.
const (
	intentChronological = "chronological"
	intentThematic      = "thematic"
)

// approvedTransitions enumerates the transitions the baseline chronological
// edit may use. Model-selected transitions arrive in a later ticket.
var approvedTransitions = map[string]bool{transitionCut: true}

// knownEditIntents enumerates the Edit Intents ave can produce today.
var knownEditIntents = map[string]bool{intentChronological: true, intentThematic: true}

// audioSource is the baseline audio decision: keep each segment's source audio.
// audioMusic replaces the bed with a supplied Music Track.
const (
	audioSource = "source"
	audioMusic  = "music"
)

// knownAudioSources enumerates the audio decisions ave can produce.
var knownAudioSources = map[string]bool{audioSource: true, audioMusic: true}

// knownMusicCues enumerates the Music Cue kinds a Selected Segment may snap to.
var knownMusicCues = map[string]bool{musicCueBeat: true, musicCuePhrase: true, musicCueSection: true}

// validatePlan rejects malformed or impossible Edit Plans before they are
// written or rendered. It guards against the model or ranking producing a plan
// that cannot be realized, rather than silently guessing a fallback.
func validatePlan(plan Plan) error {
	if !knownEditIntents[plan.EditIntent] {
		return fmt.Errorf("unknown edit intent %q", plan.EditIntent)
	}
	if !knownAudioSources[plan.Audio.Source] {
		return fmt.Errorf("unsupported audio source %q", plan.Audio.Source)
	}
	if err := validateMusic(plan.Audio); err != nil {
		return err
	}
	if plan.Ranking == nil {
		return fmt.Errorf("plan is missing ranking provenance")
	}
	if strings.TrimSpace(plan.Ranking.Model) == "" {
		return fmt.Errorf("plan is missing the ranking model identity")
	}
	if plan.Ranking.TargetSeconds <= 0 {
		return fmt.Errorf("plan target duration %.2fs must be positive", plan.Ranking.TargetSeconds)
	}
	if len(plan.SelectedSegments) == 0 {
		return fmt.Errorf("plan has no selected segments")
	}

	perSource := make(map[string][]SelectedSegment)
	for index, segment := range plan.SelectedSegments {
		if segment.StartSecond < 0 {
			return fmt.Errorf("segment %d start %.2fs is negative", index, segment.StartSecond)
		}
		if segment.EndSecond <= segment.StartSecond {
			return fmt.Errorf("segment %d end %.2fs is not after start %.2fs",
				index, segment.EndSecond, segment.StartSecond)
		}
		if !approvedTransitions[segment.Transition] {
			return fmt.Errorf("segment %d has unsupported transition %q", index, segment.Transition)
		}
		if segment.Score == nil {
			return fmt.Errorf("segment %d is missing its AI score", index)
		}
		if segment.MusicCue != "" && !knownMusicCues[segment.MusicCue] {
			return fmt.Errorf("segment %d has unsupported music cue %q", index, segment.MusicCue)
		}
		perSource[segment.SourcePath] = append(perSource[segment.SourcePath], segment)
	}

	for source, segments := range perSource {
		if err := rejectOverlaps(source, segments); err != nil {
			return err
		}
	}
	return nil
}

// validateMusic enforces the Music Track provenance needed to reproduce the
// Finished Video: a fingerprint, a positive duration, a non-negative fade, and
// recorded beats. A plain source-audio plan carries no Music Track.
func validateMusic(audio AudioSettings) error {
	if audio.Source == audioMusic {
		if audio.Music == nil {
			return fmt.Errorf("music audio source is missing its Music Track settings")
		}
		music := audio.Music
		if strings.TrimSpace(music.Path) == "" {
			return fmt.Errorf("the Music Track is missing its path")
		}
		if strings.TrimSpace(music.Fingerprint) == "" {
			return fmt.Errorf("the Music Track is missing its fingerprint")
		}
		if music.DurationSeconds <= 0 {
			return fmt.Errorf("the Music Track duration %.2fs must be positive", music.DurationSeconds)
		}
		if music.FadeOutSeconds < 0 {
			return fmt.Errorf("the Music Track fade %.2fs must not be negative", music.FadeOutSeconds)
		}
		if len(music.Cues.Beats) == 0 {
			return fmt.Errorf("the Music Track has no recorded beats")
		}
		return nil
	}
	if audio.Music != nil {
		return fmt.Errorf("audio source %q must not carry Music Track settings", audio.Source)
	}
	return nil
}

// rejectOverlaps fails when two selected ranges from the same source overlap,
// which would double-count footage in the timeline.
func rejectOverlaps(source string, segments []SelectedSegment) error {
	sorted := make([]SelectedSegment, len(segments))
	copy(sorted, segments)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].StartSecond < sorted[right].StartSecond
	})
	for index := 1; index < len(sorted); index++ {
		if sorted[index].StartSecond < sorted[index-1].EndSecond {
			return fmt.Errorf("source %s has overlapping selected ranges %.2f-%.2f and %.2f-%.2f",
				source,
				sorted[index-1].StartSecond, sorted[index-1].EndSecond,
				sorted[index].StartSecond, sorted[index].EndSecond)
		}
	}
	return nil
}
