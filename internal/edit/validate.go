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

// approvedTransitions enumerates the Transitions the planning stage may choose:
// a hard cut, opening/closing fades, dips through black or white, directional
// wipes, and simple slides. Choices outside this set are rejected before a plan
// is written or rendered. Speed changes are intentionally excluded.
var approvedTransitions = map[string]bool{
	transitionCut:        true,
	transitionFade:       true,
	transitionDissolve:   true,
	transitionDipBlack:   true,
	transitionDipWhite:   true,
	transitionWipeLeft:   true,
	transitionWipeRight:  true,
	transitionWipeUp:     true,
	transitionWipeDown:   true,
	transitionSlideLeft:  true,
	transitionSlideRight: true,
	transitionSlideUp:    true,
	transitionSlideDown:  true,
}

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
	if err := validateDialogue(plan.Audio); err != nil {
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
		if err := validateTransitionTiming(index, plan.SelectedSegments); err != nil {
			return err
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

// dialogue continuity kinds record how related speech bridges a cut.
const (
	continuityJCut = "J-cut"
	continuityLCut = "L-cut"
)

// knownLanguageSources enumerates how a dialogue language was decided.
var knownLanguageSources = map[string]bool{"detected": true, "override": true}

// knownContinuityKinds enumerates the dialogue-carrying cut kinds.
var knownContinuityKinds = map[string]bool{continuityJCut: true, continuityLCut: true}

// validateDialogue enforces the speech-analysis provenance: a known language
// source, a supported ducking profile, and well-formed continuity records.
func validateDialogue(audio AudioSettings) error {
	if audio.Dialogue == nil {
		if len(audio.Continuity) > 0 {
			return fmt.Errorf("dialogue continuity recorded without dialogue settings")
		}
		return nil
	}
	dialogue := audio.Dialogue
	if strings.TrimSpace(dialogue.Language) == "" {
		return fmt.Errorf("dialogue is missing its language")
	}
	if !knownLanguageSources[dialogue.LanguageSource] {
		return fmt.Errorf("dialogue has unsupported language source %q", dialogue.LanguageSource)
	}
	if !knownDuckProfiles[dialogue.Ducking] {
		return fmt.Errorf("dialogue has unsupported ducking profile %q", dialogue.Ducking)
	}
	for index, cut := range audio.Continuity {
		if !knownContinuityKinds[cut.Kind] {
			return fmt.Errorf("dialogue continuity %d has unsupported kind %q", index, cut.Kind)
		}
		if cut.Seconds <= 0 {
			return fmt.Errorf("dialogue continuity %d duration %.2fs must be positive", index, cut.Seconds)
		}
		if cut.ToSegment != cut.FromSegment+1 {
			return fmt.Errorf("dialogue continuity %d must bridge adjacent segments, got %d to %d",
				index, cut.FromSegment, cut.ToSegment)
		}
	}
	return nil
}

// validateTransitionTiming enforces the Transition placement and duration
// rules: a hard cut carries no duration, a fade is reserved for the edit's
// opening, every other Transition needs a justification, and a Transition's
// duration stays within the 0.15-0.8s band and twenty percent of each adjoining
// segment so an effect never overwhelms the footage it joins.
func validateTransitionTiming(index int, segments []SelectedSegment) error {
	segment := segments[index]
	if segment.Transition == transitionCut {
		if segment.TransitionSeconds != 0 {
			return fmt.Errorf("segment %d hard cut must not carry a duration", index)
		}
		return nil
	}
	if segment.Transition == transitionFade && index != 0 {
		return fmt.Errorf("segment %d fade is only allowed as the opening Transition", index)
	}
	if index == 0 && segment.Transition != transitionFade {
		return fmt.Errorf("segment 0 opening Transition must be a fade or a cut, got %q", segment.Transition)
	}
	if strings.TrimSpace(segment.TransitionReason) == "" {
		return fmt.Errorf("segment %d Transition %q is missing its justification", index, segment.Transition)
	}

	const epsilon = 1e-6
	if segment.TransitionSeconds < transitionMinSeconds-epsilon ||
		segment.TransitionSeconds > transitionMaxSeconds+epsilon {
		return fmt.Errorf("segment %d Transition %.3fs is outside the %.2f-%.2fs band",
			index, segment.TransitionSeconds, transitionMinSeconds, transitionMaxSeconds)
	}
	this := segment.EndSecond - segment.StartSecond
	if segment.TransitionSeconds > transitionMaxSegmentFraction*this+epsilon {
		return fmt.Errorf("segment %d Transition %.3fs exceeds twenty percent of its %.2fs segment",
			index, segment.TransitionSeconds, this)
	}
	if index > 0 {
		prev := segments[index-1].EndSecond - segments[index-1].StartSecond
		if segment.TransitionSeconds > transitionMaxSegmentFraction*prev+epsilon {
			return fmt.Errorf("segment %d Transition %.3fs exceeds twenty percent of the preceding %.2fs segment",
				index, segment.TransitionSeconds, prev)
		}
	}
	return nil
}

// validatePlanTransitions re-checks the Transition vocabulary and timing of an
// externally supplied Edit Plan so a hand-written or tampered plan cannot smuggle
// an unapproved or out-of-bounds Transition into the render stage.
func validatePlanTransitions(segments []SelectedSegment) error {
	for index, segment := range segments {
		// A plan that predates the Transition vocabulary (or a minimal
		// hand-written plan) omits the field; an empty Transition is an
		// implicit hard cut and needs no further checking.
		if segment.Transition == "" {
			if segment.TransitionSeconds != 0 {
				return fmt.Errorf("segment %d implicit cut must not carry a duration", index)
			}
			continue
		}
		if !approvedTransitions[segment.Transition] {
			return fmt.Errorf("segment %d has unsupported transition %q", index, segment.Transition)
		}
		if err := validateTransitionTiming(index, segments); err != nil {
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
