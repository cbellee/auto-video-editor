package edit

import (
	"fmt"
	"strconv"
	"strings"
)

// Transition vocabulary the planning stage may choose from. A hard cut is the
// default join; fades are reserved for the edit's opening and closing; dips
// pass through black or white without overlapping neighbors; dissolves, wipes,
// and slides overlap adjacent segments. Speed changes are deliberately absent.
const (
	// transitionCut is defined in validate.go as the baseline join.
	transitionFade       = "fade"
	transitionDissolve   = "dissolve"
	transitionDipBlack   = "dip-black"
	transitionDipWhite   = "dip-white"
	transitionWipeLeft   = "wipeleft"
	transitionWipeRight  = "wiperight"
	transitionWipeUp     = "wipeup"
	transitionWipeDown   = "wipedown"
	transitionSlideLeft  = "slideleft"
	transitionSlideRight = "slideright"
	transitionSlideUp    = "slideup"
	transitionSlideDown  = "slidedown"
)

// transitionDuration bounds. Transitions stay short so they punctuate rather
// than dominate the Selected Segments they join.
const (
	transitionMinSeconds         = 0.15
	transitionMaxSeconds         = 0.8
	transitionDefaultSeconds     = 0.4
	transitionMaxSegmentFraction = 0.2
)

// overlapTransitions are the Transitions that overlap two segments via xfade,
// shortening the timeline by their duration. The others are length preserving.
var overlapTransitions = map[string]bool{
	transitionDissolve:   true,
	transitionWipeLeft:   true,
	transitionWipeRight:  true,
	transitionWipeUp:     true,
	transitionWipeDown:   true,
	transitionSlideLeft:  true,
	transitionSlideRight: true,
	transitionSlideUp:    true,
	transitionSlideDown:  true,
}

// dipTransitions pass through a solid color at the boundary without overlap.
var dipTransitions = map[string]bool{
	transitionDipBlack: true,
	transitionDipWhite: true,
}

// isOverlapTransition reports whether a Transition overlaps its two segments.
func isOverlapTransition(transition string) bool {
	return overlapTransitions[transition]
}

// xfadeStyle maps an overlap Transition to its FFmpeg xfade transition name.
func xfadeStyle(transition string) string {
	if transition == transitionDissolve {
		return "fade"
	}
	return transition
}

// dipColor returns the solid color a dip Transition passes through.
func dipColor(transition string) string {
	if transition == transitionDipWhite {
		return "white"
	}
	return "black"
}

// boundTransitionSeconds clamps a requested Transition duration to the global
// band and to twenty percent of each adjoining segment. prevSeconds is zero for
// the opening Transition, which only has one adjoining segment.
func boundTransitionSeconds(requested, prevSeconds, nextSeconds float64) float64 {
	seconds := requested
	if seconds > transitionMaxSeconds {
		seconds = transitionMaxSeconds
	}
	if limit := transitionMaxSegmentFraction * nextSeconds; seconds > limit {
		seconds = limit
	}
	if prevSeconds > 0 {
		if limit := transitionMaxSegmentFraction * prevSeconds; seconds > limit {
			seconds = limit
		}
	}
	if seconds < transitionMinSeconds {
		seconds = transitionMinSeconds
	}
	return seconds
}

// segmentFadeChain returns the FFmpeg fade filters appended to a segment's
// video so dips and the edit's opening/closing fades are realized without
// overlapping neighbors. Overlap Transitions add nothing here because xfade
// performs their blend. The returned string is empty or begins with a comma so
// it can be spliced directly into the segment's filter chain.
func segmentFadeChain(inputs []renderInput, index int) string {
	var chain strings.Builder
	if color, seconds, ok := headFade(inputs, index); ok {
		fmt.Fprintf(&chain, ",fade=t=in:st=0:d=%s:color=%s",
			strconv.FormatFloat(seconds, 'f', 6, 64), color)
	}
	duration := inputs[index].end - inputs[index].start
	if color, seconds, ok := tailFade(inputs, index, duration); ok {
		start := duration - seconds
		if start < 0 {
			start = 0
		}
		fmt.Fprintf(&chain, ",fade=t=out:st=%s:d=%s:color=%s",
			strconv.FormatFloat(start, 'f', 6, 64),
			strconv.FormatFloat(seconds, 'f', 6, 64),
			color)
	}
	return chain.String()
}

// headFade reports the fade-in applied to a segment's opening: the edit's
// opening fade, or a dip's fade up from its color.
func headFade(inputs []renderInput, index int) (string, float64, bool) {
	transition := inputs[index].transition
	if transition == transitionFade {
		return "black", inputs[index].transitionSeconds, true
	}
	if dipTransitions[transition] {
		return dipColor(transition), inputs[index].transitionSeconds, true
	}
	return "", 0, false
}

// tailFade reports the fade-out applied to a segment's end: the edit's closing
// fade on the final segment, or a dip's fade down into the next segment's color.
func tailFade(inputs []renderInput, index int, duration float64) (string, float64, bool) {
	if index == len(inputs)-1 {
		return "black", boundTransitionSeconds(transitionDefaultSeconds+0.1, 0, duration), true
	}
	next := inputs[index+1].transition
	if dipTransitions[next] {
		return dipColor(next), inputs[index+1].transitionSeconds, true
	}
	return "", 0, false
}

// every Selected Segment. The opening segment fades in; interior boundaries
// dissolve when the footage changes source (a coherent scene change) and hard
// cut within continuous footage; durations shorten near strong Music Cues so a
// transition never softens a deliberate musical hit.
// canHostTransition reports whether the adjoining segments are long enough to
// host a minimum-length Transition without breaching the twenty-percent ceiling.
// When they are not, the only self-consistent choice is a hard cut: any non-cut
// Transition would need at least transitionMinSeconds, which would exceed twenty
// percent of a sub-0.75s segment and be rejected by validateTransitionTiming.
// prevSeconds is zero for the opening Transition, which has one adjoining segment.
func canHostTransition(prevSeconds, nextSeconds float64) bool {
	const epsilon = 1e-6
	if transitionMaxSegmentFraction*nextSeconds < transitionMinSeconds-epsilon {
		return false
	}
	if prevSeconds > 0 && transitionMaxSegmentFraction*prevSeconds < transitionMinSeconds-epsilon {
		return false
	}
	return true
}

func planTransitions(segments []SelectedSegment) {
	for index := range segments {
		this := segments[index].EndSecond - segments[index].StartSecond
		if index == 0 {
			if !canHostTransition(0, this) {
				// Too short to host an opening fade; open on a hard cut.
				segments[index].Transition = transitionCut
				segments[index].TransitionSeconds = 0
				segments[index].TransitionReason = ""
				continue
			}
			seconds := boundTransitionSeconds(transitionDefaultSeconds+0.1, 0, this)
			segments[index].Transition = transitionFade
			segments[index].TransitionSeconds = seconds
			segments[index].TransitionReason = "opening fade in from black"
			continue
		}

		prev := segments[index-1].EndSecond - segments[index-1].StartSecond
		requested := transitionDefaultSeconds
		reason := ""
		switch segments[index].MusicCue {
		case musicCueSection:
			requested = transitionMinSeconds
			reason = "shortened at a section Music Cue"
		case musicCuePhrase:
			requested = 0.25
			reason = "shortened at a phrase Music Cue"
		case musicCueBeat:
			requested = 0.3
			reason = "tightened to a beat Music Cue"
		}

		if segments[index].SourcePath == segments[index-1].SourcePath || !canHostTransition(prev, this) {
			// Continuous footage from one Source Clip reads best as a hard cut
			// (a dissolve would blur a single continuous action), and a segment
			// too short to host a bounded Transition must also hard cut.
			segments[index].Transition = transitionCut
			segments[index].TransitionSeconds = 0
			segments[index].TransitionReason = ""
			continue
		}

		if reason == "" {
			reason = "cross-dissolve across a Source Clip change"
		}
		segments[index].Transition = transitionDissolve
		segments[index].TransitionSeconds = boundTransitionSeconds(requested, prev, this)
		segments[index].TransitionReason = reason
	}
}
