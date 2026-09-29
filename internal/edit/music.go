package edit

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// beatsPerPhrase and beatsPerSection derive coarse musical structure from
	// the beat grid. aubio does not detect phrases or sections directly, so ave
	// groups beats into a regular meter: a phrase every beatsPerPhrase beats and
	// a section every beatsPerSection beats. These are deliberately coarse
	// timing references, not a claim of true musical analysis.
	beatsPerPhrase  = 8
	beatsPerSection = 32

	// musicSnapTolerance bounds how far a cut may be nudged to reach a Music Cue
	// before the app leaves the cut where visual selection placed it. Keeping it
	// small means ave does not cut on every beat.
	musicSnapTolerance = 0.5

	// minSnappedSegmentSeconds keeps a snapped segment from being trimmed away
	// to almost nothing when a nearby cue would otherwise pull the cut in.
	minSnappedSegmentSeconds = 1.0

	// musicFadeOutSeconds is the fade applied to the tail of the Music Track so
	// a trimmed track never ends abruptly.
	musicFadeOutSeconds = 2.0

	// majorChangeScoreGap is the base-score difference above which an adjacent
	// pair of segments counts as a major visual change that should prefer a
	// phrase or section boundary over an ordinary beat.
	majorChangeScoreGap = 0.25

	// musicEpsilon absorbs floating-point noise when comparing durations.
	musicEpsilon = 0.01
)

// MusicCues are the locally detected timing references extracted from a Music
// Track. Beats and onsets come straight from aubio; phrases and sections are
// coarse groupings of the beat grid.
type MusicCues struct {
	Beats    []float64 `json:"beats"`
	Onsets   []float64 `json:"onsets"`
	Phrases  []float64 `json:"phrases"`
	Sections []float64 `json:"sections"`
}

// MusicSettings records the optional Music Track decision so the Finished Video
// timing and audio bed are reproducible from the plan alone.
type MusicSettings struct {
	Path            string    `json:"path"`
	Fingerprint     string    `json:"fingerprint"`
	DurationSeconds float64   `json:"duration_seconds"`
	FadeOutSeconds  float64   `json:"fade_out_seconds"`
	Cues            MusicCues `json:"cues"`
}

// musicAnalysis is the fully resolved Music Track: its identity, length, and
// detected cues, ready to shape duration and cut timing.
type musicAnalysis struct {
	absPath     string
	fingerprint string
	duration    float64
	cues        MusicCues
}

// resolveMusic analyzes the optional Music Track and enforces the duration
// contract: an explicit duration may not exceed the track length. It returns
// nil when no Music Track was requested.
func resolveMusic(ctx context.Context, options Options) (*musicAnalysis, error) {
	if strings.TrimSpace(options.Music) == "" {
		return nil, nil
	}
	absPath, err := filepath.Abs(options.Music)
	if err != nil {
		return nil, fmt.Errorf("resolve Music Track path: %w", err)
	}
	analyzed, err := analyzeMusicTrack(ctx, absPath)
	if err != nil {
		return nil, err
	}
	if options.Duration > 0 && options.Duration > analyzed.duration+musicEpsilon {
		return nil, fmt.Errorf(
			"requested duration %.2fs is longer than the Music Track %.2fs; choose a duration within the track",
			options.Duration, analyzed.duration)
	}
	return &analyzed, nil
}

// analyzeMusicTrack fingerprints the Music Track, measures its length, and
// detects its Music Cues. A missing or unreadable track is a hard error: the
// user asked for music, so ave does not silently continue without it.
func analyzeMusicTrack(ctx context.Context, absPath string) (musicAnalysis, error) {
	fingerprint, err := sourceFingerprint(absPath)
	if err != nil {
		return musicAnalysis{}, fmt.Errorf("fingerprint Music Track: %w", err)
	}
	duration, err := probeMusicDuration(ctx, absPath)
	if err != nil {
		return musicAnalysis{}, err
	}
	cues, err := detectMusicCues(ctx, absPath, duration)
	if err != nil {
		return musicAnalysis{}, err
	}
	return musicAnalysis{absPath: absPath, fingerprint: fingerprint, duration: duration, cues: cues}, nil
}

// probeMusicDuration reads the Music Track length with ffprobe.
func probeMusicDuration(ctx context.Context, absPath string) (float64, error) {
	command := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		absPath,
	)
	output, err := command.Output()
	if err != nil {
		return 0, fmt.Errorf("probe Music Track %s: %w", absPath, err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse Music Track duration %q: %w", strings.TrimSpace(string(output)), err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("the Music Track %s has a non-positive duration", absPath)
	}
	return duration, nil
}

// detectMusicCues runs aubio to extract beats and onsets, then derives coarse
// phrase and section cues from the beat grid.
func detectMusicCues(ctx context.Context, absPath string, duration float64) (MusicCues, error) {
	beats, err := runAubio(ctx, "beat", absPath)
	if err != nil {
		return MusicCues{}, err
	}
	onsets, err := runAubio(ctx, "onset", absPath)
	if err != nil {
		return MusicCues{}, err
	}
	beats = cleanCues(beats, duration)
	onsets = cleanCues(onsets, duration)
	phrases, sections := derivePhrasesAndSections(beats)
	return MusicCues{Beats: beats, Onsets: onsets, Phrases: phrases, Sections: sections}, nil
}

// runAubio invokes an aubio subcommand that prints one timestamp per line and
// returns the parsed seconds.
func runAubio(ctx context.Context, subcommand, absPath string) ([]float64, error) {
	command := exec.CommandContext(ctx, "aubio", subcommand, absPath)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("detect %s cues with aubio for %s: %w", subcommand, absPath, err)
	}
	var times []float64
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		field := strings.TrimSpace(scanner.Text())
		if field == "" {
			continue
		}
		// Some aubio builds print "time value"; the first field is the time.
		field = strings.Fields(field)[0]
		value, convErr := strconv.ParseFloat(field, 64)
		if convErr != nil {
			continue
		}
		times = append(times, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read aubio %s output: %w", subcommand, err)
	}
	return times, nil
}

// cleanCues sorts, de-duplicates, and clamps cues to the track, keeping the
// recorded timing references monotonic and inside the audio.
func cleanCues(times []float64, duration float64) []float64 {
	filtered := make([]float64, 0, len(times))
	for _, value := range times {
		if value < 0 || value > duration+musicEpsilon {
			continue
		}
		filtered = append(filtered, value)
	}
	sort.Float64s(filtered)
	deduped := make([]float64, 0, len(filtered))
	for _, value := range filtered {
		if len(deduped) > 0 && value-deduped[len(deduped)-1] < musicEpsilon {
			continue
		}
		deduped = append(deduped, value)
	}
	return deduped
}

// derivePhrasesAndSections groups the beat grid into coarse phrase and section
// boundaries. The first beat always anchors both so an edit can open on
// structure.
func derivePhrasesAndSections(beats []float64) (phrases, sections []float64) {
	for index, beat := range beats {
		if index%beatsPerPhrase == 0 {
			phrases = append(phrases, beat)
		}
		if index%beatsPerSection == 0 {
			sections = append(sections, beat)
		}
	}
	return phrases, sections
}

// snappedCut is the outcome of aligning one Selected Segment's out-point to the
// Music Track: the adjusted source end and which cue kind it reached.
type snappedCut struct {
	end float64
	cue string
}

// Music Cue kinds recorded per Selected Segment.
const (
	musicCueBeat    = "beat"
	musicCuePhrase  = "phrase"
	musicCueSection = "section"
)

// snapCutsToMusic nudges each internal cut toward a Music Cue on the Finished
// Video timeline. Ordinary cuts prefer nearby beats; major visual changes
// prefer phrase or section boundaries. Snapping only ever trims a segment (it
// never invents footage) and never shortens a segment below the floor, so
// strong footage and continuity can keep a cut off an exact cue. The last
// segment is never snapped: the Music Track fade ends the video.
func snapCutsToMusic(selected []rankedCandidate, cues MusicCues) []snappedCut {
	snaps := make([]snappedCut, len(selected))
	timeline := 0.0
	for index, candidate := range selected {
		naturalDuration := candidate.rng.end - candidate.rng.start
		end := candidate.rng.end
		cue := ""
		duration := naturalDuration
		if index < len(selected)-1 {
			boundary := timeline + naturalDuration
			preferred := beatPriority(cues)
			if isMajorChange(candidate, selected[index+1]) {
				preferred = phrasePriority(cues)
			}
			if target, kind, ok := nearestPriorCue(preferred, boundary); ok {
				snappedDuration := target - timeline
				if snappedDuration >= minSnappedSegmentSeconds {
					end = candidate.rng.start + snappedDuration
					cue = kind
					duration = snappedDuration
				}
			}
		}
		snaps[index] = snappedCut{end: end, cue: cue}
		timeline += duration
	}
	return snaps
}

// cuePriority pairs a cue kind with the times to search, so a major change can
// fall back from sections to phrases to beats.
type cuePriority struct {
	kind  string
	times []float64
}

func beatPriority(cues MusicCues) []cuePriority {
	return []cuePriority{{musicCueBeat, cues.Beats}}
}

func phrasePriority(cues MusicCues) []cuePriority {
	return []cuePriority{
		{musicCueSection, cues.Sections},
		{musicCuePhrase, cues.Phrases},
		{musicCueBeat, cues.Beats},
	}
}

// nearestPriorCue finds the latest cue at or before boundary within tolerance,
// trying each priority in order. Trimming only (cue <= boundary) keeps the cut
// inside footage that already passed selection.
func nearestPriorCue(priorities []cuePriority, boundary float64) (float64, string, bool) {
	for _, priority := range priorities {
		best := -1.0
		for _, cue := range priority.times {
			if cue > boundary+musicEpsilon {
				break
			}
			if boundary-cue <= musicSnapTolerance && cue > best {
				best = cue
			}
		}
		if best >= 0 {
			return best, priority.kind, true
		}
	}
	return 0, "", false
}

// isMajorChange reports whether the visual content changes enough between two
// adjacent segments to justify a phrase or section cut: a different Source Clip
// or a large gap in base score.
func isMajorChange(current, next rankedCandidate) bool {
	if current.relPath != next.relPath {
		return true
	}
	gap := current.base - next.base
	if gap < 0 {
		gap = -gap
	}
	return gap >= majorChangeScoreGap
}

// musicRender is the resolved Music Track audio bed passed to the encoder: an
// absolute path and the tail fade to apply after trimming to the edit length.
type musicRender struct {
	path    string
	fadeOut float64
}
