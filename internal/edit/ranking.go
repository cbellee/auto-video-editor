package edit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/cbellee/auto-video-editor/internal/lmstudio"
)

// Ranking constants tune how AI scores are combined and how much footage the
// automatic duration targets.
const (
	// contactSheetFrames is the number of timestamped tiles sampled per
	// Candidate Segment (a 2x2 grid).
	contactSheetFrames = 4
	contactSheetCols   = 2
	contactSheetRows   = 2

	// automaticFraction targets roughly this share of usable footage.
	automaticFraction = 0.15
	minTargetSeconds  = 30.0
	maxTargetSeconds  = 120.0

	// redundancyThreshold is the self-assessed redundancy at or above which a
	// candidate is eligible to be dropped as a near-duplicate.
	redundancyThreshold = 0.6
)

// scorer is the subset of the LM Studio client used for ranking, isolated so
// the selection pipeline can be tested without a live server.
type scorer interface {
	Score(ctx context.Context, req lmstudio.ScoreRequest) (lmstudio.ScoreResult, error)
}

// eligibleCandidate is a technically-passed Candidate Segment awaiting AI
// ranking. It carries the absolute source path for frame extraction plus the
// provenance fields needed to build a Selected Segment.
type eligibleCandidate struct {
	clipPath           string
	relPath            string
	fingerprint        string
	isHDR              bool
	rng                candidateRange
	metrics            SegmentMetrics
	shaky              bool
	needsStabilization bool
	order              int
}

// rankedCandidate augments an eligible candidate with its model assessment.
type rankedCandidate struct {
	eligibleCandidate
	score       lmstudio.Score
	base        float64
	prompt      string
	rawResponse string
	repaired    bool
	sampleTimes []float64
}

// ranker performs AI ranking of eligible candidates using a scorer and FFmpeg
// contact-sheet extraction.
type ranker struct {
	client   scorer
	model    string
	intent   string
	guidance string
	theme    string
}

// rankAll extracts a contact sheet for each candidate, scores it, and returns
// the ranked candidates in their original chronological order. A candidate
// whose model output cannot be parsed into a valid score (even after the
// client's one repair attempt) is dropped and returned in skipped rather than
// aborting the whole run; genuine infrastructure errors still abort. If every
// candidate fails to score, an error is returned so a broken model is loud.
func (r *ranker) rankAll(ctx context.Context, candidates []eligibleCandidate) (ranked []rankedCandidate, skipped []eligibleCandidate, err error) {
	ranked = make([]rankedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		times := sampleTimestamps(candidate.rng, contactSheetFrames)
		sheet, sheetErr := contactSheet(ctx, candidate.clipPath, candidate.rng)
		if sheetErr != nil {
			return nil, nil, sheetErr
		}
		prompt := candidatePrompt(candidate, times, r.guidance, r.theme)
		result, scoreErr := r.client.Score(ctx, lmstudio.ScoreRequest{
			Model:        r.model,
			SystemPrompt: systemPrompt(r.intent, r.guidance, r.theme),
			UserPrompt:   prompt,
			ImagePNG:     sheet,
		})
		if scoreErr != nil {
			if errors.Is(scoreErr, lmstudio.ErrUnparseableScore) {
				skipped = append(skipped, candidate)
				continue
			}
			return nil, nil, fmt.Errorf("rank candidate %s %.2f-%.2f: %w",
				candidate.relPath, candidate.rng.start, candidate.rng.end, scoreErr)
		}
		ranked = append(ranked, rankedCandidate{
			eligibleCandidate: candidate,
			score:             result.Score,
			base:              baseScore(result.Score),
			prompt:            prompt,
			rawResponse:       result.RawResponse,
			repaired:          result.Repaired,
			sampleTimes:       times,
		})
	}
	if len(ranked) == 0 && len(candidates) > 0 {
		return nil, nil, fmt.Errorf(
			"every candidate segment failed scoring (%d of %d); check the vision model output",
			len(skipped), len(candidates))
	}
	return ranked, skipped, nil
}

// baseScore blends the model's scores into a single ranking value that favors
// interesting, useful, energetic footage.
func baseScore(s lmstudio.Score) float64 {
	return 0.5*s.VisualInterest + 0.3*s.Usefulness + 0.2*s.Energy
}

// automaticDuration returns the target Finished Video duration. An explicit
// override is honored as-is (it may finish short); otherwise the target is
// roughly automaticFraction of usable footage, clamped to the 30-120s band and
// never exceeding the footage actually available.
func automaticDuration(eligibleSeconds, override float64) float64 {
	if override > 0 {
		return override
	}
	target := automaticFraction * eligibleSeconds
	if target < minTargetSeconds {
		target = minTargetSeconds
	}
	if target > maxTargetSeconds {
		target = maxTargetSeconds
	}
	if target > eligibleSeconds {
		target = eligibleSeconds
	}
	return target
}

// selectChronological chooses the strongest candidates up to targetSeconds while
// penalizing near-duplicates, then returns them in capture order alongside the
// candidates dropped for diversity. Chronology is always preserved in the
// returned selection.
// highInterestShakyThreshold is the minimum model Visual Interest a shaky
// candidate must reach to be worth stabilizing. Footage below the bar is
// dropped rather than smoothed; average-or-better footage is retained.
// Non-shaky candidates are never filtered here.
const highInterestShakyThreshold = 0.5

// retainHighInterestShaky drops shaky candidates whose model Visual Interest is
// below the high-interest bar, so stabilize mode spends effort only on footage
// worth keeping. Steady candidates always pass through. The dropped shaky
// candidates are returned so the caller can record why they were rejected.
func retainHighInterestShaky(candidates []rankedCandidate) (kept, dropped []rankedCandidate) {
	for _, candidate := range candidates {
		if candidate.shaky && candidate.score.VisualInterest < highInterestShakyThreshold {
			dropped = append(dropped, candidate)
			continue
		}
		kept = append(kept, candidate)
	}
	return kept, dropped
}

func selectChronological(candidates []rankedCandidate, targetSeconds float64) (selected, dropped []rankedCandidate) {
	byScore := make([]rankedCandidate, len(candidates))
	copy(byScore, candidates)
	sort.SliceStable(byScore, func(left, right int) bool {
		if byScore[left].base != byScore[right].base {
			return byScore[left].base > byScore[right].base
		}
		return byScore[left].order < byScore[right].order
	})

	var chosen []rankedCandidate
	var total float64
	for _, candidate := range byScore {
		if total >= targetSeconds {
			dropped = append(dropped, candidate)
			continue
		}
		if isNearDuplicate(candidate, chosen) {
			dropped = append(dropped, candidate)
			continue
		}
		chosen = append(chosen, candidate)
		total += candidate.rng.end - candidate.rng.start
	}

	sort.SliceStable(chosen, func(left, right int) bool {
		return chosen[left].order < chosen[right].order
	})
	return chosen, dropped
}

// isNearDuplicate reports whether candidate repeats an already-chosen shot: it
// must share a subject, contribute no distinct action, and be flagged redundant
// by the model. Repeated subjects that show a distinct action are retained.
func isNearDuplicate(candidate rankedCandidate, chosen []rankedCandidate) bool {
	for _, other := range chosen {
		if candidate.score.Redundancy < redundancyThreshold && other.score.Redundancy < redundancyThreshold {
			continue
		}
		if !overlaps(candidate.score.Subjects, other.score.Subjects) {
			continue
		}
		if hasDistinctItem(candidate.score.Actions, other.score.Actions) {
			continue
		}
		return true
	}
	return false
}

// overlaps reports whether two string sets share any case-insensitive element.
func overlaps(left, right []string) bool {
	set := make(map[string]bool, len(left))
	for _, item := range left {
		set[strings.ToLower(strings.TrimSpace(item))] = true
	}
	for _, item := range right {
		if set[strings.ToLower(strings.TrimSpace(item))] {
			return true
		}
	}
	return false
}

// hasDistinctItem reports whether left contains an element absent from right.
func hasDistinctItem(left, right []string) bool {
	set := make(map[string]bool, len(right))
	for _, item := range right {
		set[strings.ToLower(strings.TrimSpace(item))] = true
	}
	for _, item := range left {
		if !set[strings.ToLower(strings.TrimSpace(item))] {
			return true
		}
	}
	return false
}

// sampleTimestamps returns the source times of the frames the contact-sheet
// fps filter extracts: fps=count/duration selects one frame at the start of each
// of count equal slices, i.e. start + index*duration/count.
func sampleTimestamps(rng candidateRange, count int) []float64 {
	duration := rng.end - rng.start
	times := make([]float64, 0, count)
	for index := 0; index < count; index++ {
		times = append(times, rng.start+float64(index)*duration/float64(count))
	}
	return times
}

// systemPrompt instructs the model to score a single Candidate Segment and to
// respect but never override technical selection. The intent and optional theme
// and guidance shape what footage the model should prefer.
func systemPrompt(intent, guidance, theme string) string {
	var builder strings.Builder
	context := "a chronological highlight edit"
	if intent == intentThematic {
		context = "a thematic highlight montage organized around a theme"
	}
	builder.WriteString(
		"You assess a single video Candidate Segment for " + context + ". " +
			"You are shown a contact sheet of timestamped frames sampled from the segment. " +
			"Rate visual_interest, energy, usefulness, and redundancy from 0 to 1, and list the " +
			"main subjects and actions. Higher redundancy means the shot looks generic or repetitive. " +
			"Judge only what is visible; do not try to rescue footage that was already rejected for " +
			"technical quality. Respond with only the JSON object.")
	if strings.TrimSpace(theme) != "" {
		builder.WriteString("\nThe montage theme is: ")
		builder.WriteString(strings.TrimSpace(theme))
		builder.WriteString(
			"\nRaise usefulness and visual_interest for footage that expresses this theme, but never " +
				"rescue technically rejected footage.")
	}
	if strings.TrimSpace(guidance) != "" {
		builder.WriteString("\nSelection guidance from the user: ")
		builder.WriteString(strings.TrimSpace(guidance))
		builder.WriteString(
			"\nLet this guidance influence visual_interest and usefulness among otherwise comparable footage.")
	}
	return builder.String()
}

// candidatePrompt describes one candidate, naming the source and the exact
// source timestamps of each contact-sheet tile so decisions stay traceable.
func candidatePrompt(candidate eligibleCandidate, times []float64, guidance, theme string) string {
	labels := make([]string, len(times))
	for index, time := range times {
		labels[index] = fmt.Sprintf("%.2fs", time)
	}
	prompt := fmt.Sprintf(
		"Source clip: %s\nSegment: %.2fs to %.2fs.\nContact sheet tiles (left to right, top to bottom) "+
			"were sampled at source times %s.\nScore this segment.",
		candidate.relPath, candidate.rng.start, candidate.rng.end, strings.Join(labels, ", "))
	if strings.TrimSpace(theme) != "" {
		prompt += "\nRemember the montage theme when scoring."
	}
	if strings.TrimSpace(guidance) != "" {
		prompt += "\nRemember the user's selection guidance when scoring."
	}
	return prompt
}

// orderThematic arranges the selected segments into an establishing opening,
// rising visual energy, and a conclusive high-energy ending by sorting on the
// model's energy score ascending, breaking ties toward stronger footage and
// then capture order.
func orderThematic(selected []rankedCandidate) []rankedCandidate {
	ordered := make([]rankedCandidate, len(selected))
	copy(ordered, selected)
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].score.Energy != ordered[right].score.Energy {
			return ordered[left].score.Energy < ordered[right].score.Energy
		}
		if ordered[left].base != ordered[right].base {
			return ordered[left].base > ordered[right].base
		}
		return ordered[left].order < ordered[right].order
	})
	return ordered
}

// themeDiscoverer discovers a recurring theme from aggregated assessments.
type themeDiscoverer interface {
	DiscoverTheme(ctx context.Context, req lmstudio.ThemeRequest) (lmstudio.ThemeResult, error)
}

// discoverTheme asks the model to name the strongest recurring theme across the
// ranked candidates' subjects and actions.
func discoverTheme(ctx context.Context, discoverer themeDiscoverer, model, guidance string, ranked []rankedCandidate) (lmstudio.ThemeResult, error) {
	return discoverer.DiscoverTheme(ctx, lmstudio.ThemeRequest{
		Model:        model,
		SystemPrompt: themeSystemPrompt(guidance),
		UserPrompt:   themeUserPrompt(ranked),
	})
}

// themeSystemPrompt instructs the model to name a single recurring theme.
func themeSystemPrompt(guidance string) string {
	prompt := "You are given the subjects and actions observed across a set of video segments. " +
		"Identify the single strongest recurring theme that ties them together, as a short phrase. " +
		"Respond with only a JSON object of the form {\"theme\":\"...\"}."
	if strings.TrimSpace(guidance) != "" {
		prompt += "\nFavor a theme consistent with this user guidance: " + strings.TrimSpace(guidance)
	}
	return prompt
}

// themeUserPrompt lists the observed subjects and actions by descending
// frequency for theme discovery.
func themeUserPrompt(ranked []rankedCandidate) string {
	subjects := aggregateTerms(ranked, func(c rankedCandidate) []string { return c.score.Subjects })
	actions := aggregateTerms(ranked, func(c rankedCandidate) []string { return c.score.Actions })
	return fmt.Sprintf("Subjects (most frequent first): %s\nActions (most frequent first): %s\nName the recurring theme.",
		strings.Join(subjects, ", "), strings.Join(actions, ", "))
}

// aggregateTerms returns distinct terms ordered by descending frequency, then
// alphabetically, so the theme prompt is deterministic.
func aggregateTerms(ranked []rankedCandidate, extract func(rankedCandidate) []string) []string {
	counts := make(map[string]int)
	for _, candidate := range ranked {
		seen := make(map[string]bool)
		for _, term := range extract(candidate) {
			normalized := strings.ToLower(strings.TrimSpace(term))
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			counts[normalized]++
		}
	}
	terms := make([]string, 0, len(counts))
	for term := range counts {
		terms = append(terms, term)
	}
	sort.SliceStable(terms, func(left, right int) bool {
		if counts[terms[left]] != counts[terms[right]] {
			return counts[terms[left]] > counts[terms[right]]
		}
		return terms[left] < terms[right]
	})
	return terms
}

// contactSheet extracts a tiled grid of frames from a candidate window as PNG
// bytes. FFmpeg samples the window at contactSheetFrames evenly spaced points
// and tiles them into one image.
func contactSheet(ctx context.Context, path string, rng candidateRange) ([]byte, error) {
	sheetPath, cleanup, err := tempImageFile()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	duration := rng.end - rng.start
	if duration <= 0 {
		return nil, fmt.Errorf("candidate window %.2f-%.2f is empty", rng.start, rng.end)
	}
	rate := float64(contactSheetFrames) / duration
	filter := fmt.Sprintf(
		"fps=%s,scale=480:-1,tile=%dx%d",
		strconv.FormatFloat(rate, 'f', 6, 64), contactSheetCols, contactSheetRows)

	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", formatSeconds(rng.start),
		"-i", path,
		"-t", formatSeconds(duration),
		"-frames:v", "1",
		"-vf", filter,
		"-y", sheetPath,
	}
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("extract contact sheet for %s: %s: %w", path, strings.TrimSpace(string(output)), err)
	}
	data, err := os.ReadFile(sheetPath)
	if err != nil {
		return nil, fmt.Errorf("read contact sheet for %s: %w", path, err)
	}
	return data, nil
}

// tempImageFile creates a temporary PNG sink and returns its path plus cleanup.
func tempImageFile() (string, func(), error) {
	file, err := os.CreateTemp("", "ave-sheet-*.png")
	if err != nil {
		return "", func() {}, fmt.Errorf("create contact sheet file: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("close contact sheet file: %w", closeErr)
	}
	return path, func() { _ = os.Remove(path) }, nil
}
