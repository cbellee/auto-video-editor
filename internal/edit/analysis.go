package edit

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// QualityProfileName identifies a named selectivity profile.
type QualityProfileName string

const (
	profileStrict   QualityProfileName = "strict"
	profileBalanced QualityProfileName = "balanced"
	profileLenient  QualityProfileName = "lenient"

	defaultQualityProfile = profileBalanced
)

// ShakeTreatment selects what happens to footage flagged as shaky.
type ShakeTreatment string

const (
	shakeReject    ShakeTreatment = "reject"
	shakeStabilize ShakeTreatment = "stabilize"

	defaultShakeTreatment = shakeReject
)

// Segmentation bounds, in seconds. Candidates target three-to-eight seconds,
// distinctive short clips stay eligible to one second, and no Selected Segment
// is shorter than one second.
const (
	minSegmentSeconds    = 1.0
	targetMinSeconds     = 3.0
	targetMaxSeconds     = 8.0
	staticMotionCeiling  = 2.0 // mean mafd below which a shot is treated as static
	sceneChangeThreshold = 8.0 // scdet score above which a frame is a cut
)

// QualityThresholds are the concrete, retained pass/fail limits applied to a
// candidate's deterministic metrics. Values are conservative MVP defaults and
// are recorded in every Edit Plan so a run can be audited and reproduced.
type QualityThresholds struct {
	// MaxBlur rejects a candidate whose mean lavfi.blur exceeds it; higher
	// blurdetect values mean a softer, more out-of-focus image.
	MaxBlur float64 `json:"max_blur"`
	// MinLuma and MaxLuma bound mean luma (signalstats YAVG, 0-255) to reject
	// severely under- or over-exposed footage.
	MinLuma float64 `json:"min_luma"`
	MaxLuma float64 `json:"max_luma"`
	// MaxMotion flags a candidate as shaky when its mean frame difference
	// (scdet mafd) exceeds it. Motion is used as the deterministic shake proxy.
	MaxMotion float64 `json:"max_motion"`
	// MinAudioRMS marks source audio as unusable below this level (dBFS); the
	// visual segment is retained but its audio is not considered useful.
	MinAudioRMS float64 `json:"min_audio_rms"`
}

// qualityThresholdsFor returns the retained thresholds for a profile.
func qualityThresholdsFor(profile QualityProfileName) QualityThresholds {
	switch profile {
	case profileStrict:
		return QualityThresholds{MaxBlur: 10, MinLuma: 35, MaxLuma: 220, MaxMotion: 18, MinAudioRMS: -45}
	case profileLenient:
		return QualityThresholds{MaxBlur: 22, MinLuma: 16, MaxLuma: 240, MaxMotion: 35, MinAudioRMS: -55}
	default:
		return QualityThresholds{MaxBlur: 15, MinLuma: 25, MaxLuma: 230, MaxMotion: 25, MinAudioRMS: -50}
	}
}

// AnalysisSettings records the selection knobs and applied thresholds so the
// Edit Plan is self-describing and reproducible.
type AnalysisSettings struct {
	QualityProfile QualityProfileName `json:"quality_profile"`
	ShakeTreatment ShakeTreatment     `json:"shake_treatment"`
	Thresholds     QualityThresholds  `json:"thresholds"`
}

// SegmentMetrics holds the raw deterministic measurements retained for a
// Candidate Segment.
type SegmentMetrics struct {
	Blur      float64 `json:"blur"`
	Motion    float64 `json:"motion"`
	LumaAvg   float64 `json:"luma_avg"`
	LumaMin   float64 `json:"luma_min"`
	LumaMax   float64 `json:"luma_max"`
	AudioRMS  float64 `json:"audio_rms"`
	AudioPeak float64 `json:"audio_peak"`
	Silent    bool    `json:"silent"`
	// AudioUsable records whether measured loudness cleared the profile's
	// MinAudioRMS floor. Weak audio never rejects the visual segment; it is
	// retained so later stages can prefer or re-sound it.
	AudioUsable bool `json:"audio_usable"`
}

// RejectedSegment records a Candidate Segment excluded by the technical gates,
// along with the reasons and whether the failure is a hard, non-overridable one.
type RejectedSegment struct {
	SourcePath  string         `json:"source_path"`
	StartSecond float64        `json:"start_seconds"`
	EndSecond   float64        `json:"end_seconds"`
	Metrics     SegmentMetrics `json:"metrics"`
	Reasons     []string       `json:"reasons"`
	HardFailure bool           `json:"hard_failure"`
}

// candidateRange is a proposed [start,end) window within a Source Clip.
type candidateRange struct {
	start float64
	end   float64
}

// frameMotion is a single sampled frame's motion reading from scdet.
type frameMotion struct {
	time float64
	mafd float64
	cut  bool
}

// parseSceneMetadata parses the metadata=print output of a scdet pass into
// per-frame motion readings, marking frames flagged as scene cuts.
func parseSceneMetadata(raw string) []frameMotion {
	var frames []frameMotion
	var current *frameMotion
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "frame:"):
			frames = append(frames, frameMotion{time: parsePTSTime(line)})
			current = &frames[len(frames)-1]
		case current == nil:
			continue
		case strings.HasPrefix(line, "lavfi.scd.mafd="):
			current.mafd = parseKeyFloat(line)
		case strings.HasPrefix(line, "lavfi.scd.time="):
			current.cut = true
		}
	}
	return frames
}

// parsePTSTime extracts the pts_time from a "frame:.. pts_time:T" line.
func parsePTSTime(line string) float64 {
	for _, field := range strings.Fields(line) {
		if value, ok := strings.CutPrefix(field, "pts_time:"); ok {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

// parseKeyFloat parses the numeric value of a "key=value" metadata line.
func parseKeyFloat(line string) float64 {
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}

// detectCuts returns the interior shot-boundary timestamps from motion frames.
func detectCuts(frames []frameMotion, clipDuration float64) []float64 {
	var cuts []float64
	for _, frame := range frames {
		if frame.cut && frame.time > minSegmentSeconds && frame.time < clipDuration-minSegmentSeconds {
			cuts = append(cuts, frame.time)
		}
	}
	sort.Float64s(cuts)
	return cuts
}

// segmentShots splits a clip into shot ranges at the detected cuts.
func segmentShots(clipDuration float64, cuts []float64) []candidateRange {
	bounds := append([]float64{0}, cuts...)
	bounds = append(bounds, clipDuration)
	shots := make([]candidateRange, 0, len(bounds)-1)
	for index := 0; index+1 < len(bounds); index++ {
		shots = append(shots, candidateRange{start: bounds[index], end: bounds[index+1]})
	}
	return shots
}

// meanMotion returns the average mafd of frames within [start,end).
func meanMotion(frames []frameMotion, start, end float64) float64 {
	var sum float64
	var count int
	for _, frame := range frames {
		if frame.time >= start && frame.time < end {
			sum += frame.mafd
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// candidatesFromShots turns shots into Candidate Segments: overlong shots are
// divided into roughly three-to-eight-second windows, long static shots collapse
// to a single representative window, and windows shorter than one second are
// dropped unless the whole clip is itself short.
func candidatesFromShots(clipDuration float64, shots []candidateRange, frames []frameMotion) []candidateRange {
	var candidates []candidateRange
	for _, shot := range shots {
		shotDuration := shot.end - shot.start
		switch {
		case shotDuration < minSegmentSeconds:
			// Too short to stand alone unless it is the entire clip.
			if clipDuration <= targetMinSeconds && shotDuration > 0 {
				candidates = append(candidates, shot)
			}
		case shotDuration <= targetMaxSeconds:
			candidates = append(candidates, shot)
		case meanMotion(frames, shot.start, shot.end) < staticMotionCeiling:
			candidates = append(candidates, representativeWindow(shot))
		default:
			candidates = append(candidates, subdivide(shot)...)
		}
	}
	return candidates
}

// representativeWindow returns a single centered target-length window for a long
// static shot so it does not dominate the edit with near-duplicates.
func representativeWindow(shot candidateRange) candidateRange {
	center := (shot.start + shot.end) / 2
	half := targetMaxSeconds / 2
	start := math.Max(shot.start, center-half)
	end := math.Min(shot.end, start+targetMaxSeconds)
	return candidateRange{start: start, end: end}
}

// subdivide splits an overlong dynamic shot into even windows within the
// three-to-eight-second target, avoiding a final sub-second sliver.
func subdivide(shot candidateRange) []candidateRange {
	duration := shot.end - shot.start
	windows := int(math.Ceil(duration / targetMaxSeconds))
	if windows < 1 {
		windows = 1
	}
	windowLength := duration / float64(windows)
	if windowLength < targetMinSeconds {
		windows = int(math.Max(1, math.Floor(duration/targetMinSeconds)))
		windowLength = duration / float64(windows)
	}
	ranges := make([]candidateRange, 0, windows)
	for index := 0; index < windows; index++ {
		start := shot.start + float64(index)*windowLength
		end := start + windowLength
		if index == windows-1 {
			end = shot.end
		}
		ranges = append(ranges, candidateRange{start: start, end: end})
	}
	return ranges
}

// parseVideoMetrics aggregates the per-frame blurdetect and signalstats
// metadata of a candidate into a single SegmentMetrics reading.
func parseVideoMetrics(raw string) SegmentMetrics {
	var blurSum, lumaSum float64
	var blurCount, lumaCount int
	lumaMin := math.Inf(1)
	lumaMax := math.Inf(-1)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "lavfi.blur="):
			blurSum += parseKeyFloat(line)
			blurCount++
		case strings.HasPrefix(line, "lavfi.signalstats.YAVG="):
			lumaSum += parseKeyFloat(line)
			lumaCount++
		case strings.HasPrefix(line, "lavfi.signalstats.YMIN="):
			lumaMin = math.Min(lumaMin, parseKeyFloat(line))
		case strings.HasPrefix(line, "lavfi.signalstats.YMAX="):
			lumaMax = math.Max(lumaMax, parseKeyFloat(line))
		}
	}
	metrics := SegmentMetrics{}
	if blurCount > 0 {
		metrics.Blur = blurSum / float64(blurCount)
	}
	if lumaCount > 0 {
		metrics.LumaAvg = lumaSum / float64(lumaCount)
	}
	if !math.IsInf(lumaMin, 1) {
		metrics.LumaMin = lumaMin
	}
	if !math.IsInf(lumaMax, -1) {
		metrics.LumaMax = lumaMax
	}
	return metrics
}

// audioFloorDB is the finite level reported when a candidate has no measurable
// audio, keeping the retained metrics JSON-encodable.
const audioFloorDB = -120.0

// parseAudioMetrics folds the astats and silencedetect metadata of a candidate
// into loudness readings and a silence flag.
func parseAudioMetrics(raw string) (rms float64, peak float64, silent bool) {
	var rmsSum float64
	var rmsCount int
	peak = audioFloorDB
	sawAudio := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, ".RMS_level="):
			value := parseKeyFloat(line)
			if !math.IsInf(value, 0) {
				rmsSum += value
				rmsCount++
				sawAudio = true
			}
		case strings.Contains(line, ".Peak_level="):
			value := parseKeyFloat(line)
			if !math.IsInf(value, 0) {
				peak = math.Max(peak, value)
				sawAudio = true
			}
		case strings.HasPrefix(line, "lavfi.silence_start"):
			silent = true
		}
	}
	if rmsCount > 0 {
		rms = rmsSum / float64(rmsCount)
	} else {
		rms = audioFloorDB
	}
	if !sawAudio {
		silent = true
	}
	return rms, peak, silent
}

// gateResult is the outcome of applying technical gates to one candidate.
type gateResult struct {
	eligible           bool
	shaky              bool
	needsStabilization bool
	hardFailure        bool
	reasons            []string
}

// gateCandidate applies the deterministic technical-quality gates to a
// candidate's metrics. Blur, exposure, corruption, and sub-second duration are
// hard failures that later AI or guidance can never override. Excessive motion
// is treated as shake: rejected by default, or retained for later stabilization
// when the user opts in.
func gateCandidate(
	duration float64,
	metrics SegmentMetrics,
	thresholds QualityThresholds,
	shake ShakeTreatment,
) gateResult {
	result := gateResult{eligible: true}

	if duration < minSegmentSeconds {
		result.hardFailure = true
		result.reasons = append(result.reasons, "shorter than one second")
	}
	if metrics.Blur > thresholds.MaxBlur {
		result.hardFailure = true
		result.reasons = append(result.reasons, "excessive blur")
	}
	if metrics.LumaAvg < thresholds.MinLuma {
		result.hardFailure = true
		result.reasons = append(result.reasons, "severely underexposed")
	}
	if metrics.LumaAvg > thresholds.MaxLuma {
		result.hardFailure = true
		result.reasons = append(result.reasons, "severely overexposed")
	}

	if metrics.Motion > thresholds.MaxMotion {
		result.shaky = true
		if shake == shakeStabilize {
			result.needsStabilization = true
		} else {
			result.reasons = append(result.reasons, "excessive camera shake")
		}
	}

	if result.hardFailure || (result.shaky && shake != shakeStabilize) {
		result.eligible = false
	}
	return result
}

// analysisConfig carries the resolved selection knobs for a run.
type analysisConfig struct {
	profile    QualityProfileName
	thresholds QualityThresholds
	shake      ShakeTreatment
}

// resolveAnalysisConfig validates the requested quality profile and shake
// treatment, applying the balanced/reject defaults.
func resolveAnalysisConfig(options Options) (analysisConfig, error) {
	profile := defaultQualityProfile
	switch options.QualityProfile {
	case "":
	case string(profileStrict), string(profileBalanced), string(profileLenient):
		profile = QualityProfileName(options.QualityProfile)
	default:
		return analysisConfig{}, fmt.Errorf(
			"invalid quality profile %q; use strict, balanced, or lenient", options.QualityProfile)
	}

	shake := defaultShakeTreatment
	switch options.ShakeTreatment {
	case "":
	case string(shakeReject), string(shakeStabilize):
		shake = ShakeTreatment(options.ShakeTreatment)
	default:
		return analysisConfig{}, fmt.Errorf(
			"invalid shake treatment %q; use reject or stabilize", options.ShakeTreatment)
	}

	return analysisConfig{profile: profile, thresholds: qualityThresholdsFor(profile), shake: shake}, nil
}

// analyzedCandidate pairs a Candidate Segment range with its retained metrics
// and technical-gate outcome.
type analyzedCandidate struct {
	rng     candidateRange
	metrics SegmentMetrics
	gate    gateResult
}

// analyzeClip runs the two-phase analysis for one Source Clip: scene-and-motion
// detection to segment the clip, then a per-candidate metric pass feeding the
// deterministic technical-quality gates.
func analyzeClip(ctx context.Context, path string, clip sourceClip, cfg analysisConfig) ([]analyzedCandidate, error) {
	frames, err := detectSceneMotion(ctx, path)
	if err != nil {
		return nil, err
	}
	cuts := detectCuts(frames, clip.duration)
	shots := segmentShots(clip.duration, cuts)
	ranges := candidatesFromShots(clip.duration, shots, frames)

	candidates := make([]analyzedCandidate, 0, len(ranges))
	for _, rng := range ranges {
		metrics, err := measureCandidate(ctx, path, rng)
		if err != nil {
			return nil, err
		}
		metrics.Motion = meanMotion(frames, rng.start, rng.end)
		metrics.AudioUsable = !metrics.Silent && metrics.AudioRMS >= cfg.thresholds.MinAudioRMS
		gate := gateCandidate(rng.end-rng.start, metrics, cfg.thresholds, cfg.shake)
		candidates = append(candidates, analyzedCandidate{rng: rng, metrics: metrics, gate: gate})
	}
	return candidates, nil
}

// detectSceneMotion runs a scene-detection pass and returns per-frame motion
// readings with scene-cut markers.
func detectSceneMotion(ctx context.Context, path string) ([]frameMotion, error) {
	metadataPath, cleanup, err := metadataFile("scene")
	if err != nil {
		return nil, err
	}
	defer cleanup()

	filter := fmt.Sprintf("scdet=threshold=%s,metadata=print:file=%s",
		strconv.FormatFloat(sceneChangeThreshold, 'f', -1, 64), escapeFilterValue(metadataPath))
	if err := runAnalysisFFmpeg(ctx, path,
		nil,
		"-filter:v", filter,
		"-an",
	); err != nil {
		return nil, fmt.Errorf("detect shot boundaries in %s: %w", path, err)
	}
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("read scene metadata for %s: %w", path, err)
	}
	return parseSceneMetadata(string(raw)), nil
}

// measureCandidate runs the blur, exposure, and audio metric passes for one
// candidate window. Audio is best-effort: clips without a usable audio stream
// are treated as silent rather than failing the run.
func measureCandidate(ctx context.Context, path string, rng candidateRange) (SegmentMetrics, error) {
	videoPath, cleanupVideo, err := metadataFile("video")
	if err != nil {
		return SegmentMetrics{}, err
	}
	defer cleanupVideo()

	duration := rng.end - rng.start
	videoFilter := fmt.Sprintf("blurdetect,signalstats,metadata=print:file=%s", escapeFilterValue(videoPath))
	if err := runAnalysisFFmpeg(ctx, path,
		[]string{"-ss", formatSeconds(rng.start), "-t", formatSeconds(duration)},
		"-filter:v", videoFilter,
		"-an",
	); err != nil {
		return SegmentMetrics{}, fmt.Errorf("measure candidate %.2f-%.2f of %s: %w", rng.start, rng.end, path, err)
	}
	rawVideo, err := os.ReadFile(videoPath)
	if err != nil {
		return SegmentMetrics{}, fmt.Errorf("read video metrics for %s: %w", path, err)
	}
	metrics := parseVideoMetrics(string(rawVideo))

	audioPath, cleanupAudio, err := metadataFile("audio")
	if err != nil {
		return SegmentMetrics{}, err
	}
	defer cleanupAudio()

	audioFilter := fmt.Sprintf(
		"silencedetect=n=-60dB:d=0.3,astats=metadata=1:reset=0,ametadata=print:file=%s", escapeFilterValue(audioPath))
	if audioErr := runAnalysisFFmpeg(ctx, path,
		[]string{"-ss", formatSeconds(rng.start), "-t", formatSeconds(duration)},
		"-filter:a", audioFilter,
		"-vn",
	); audioErr == nil {
		if rawAudio, readErr := os.ReadFile(audioPath); readErr == nil {
			metrics.AudioRMS, metrics.AudioPeak, metrics.Silent = parseAudioMetrics(string(rawAudio))
		} else {
			metrics.AudioRMS, metrics.AudioPeak, metrics.Silent = audioFloorDB, audioFloorDB, true
		}
	} else {
		metrics.AudioRMS, metrics.AudioPeak, metrics.Silent = audioFloorDB, audioFloorDB, true
	}
	return metrics, nil
}

// runAnalysisFFmpeg executes a non-encoding FFmpeg analysis pass that discards
// its output; only the emitted metadata files matter. Any preInput arguments
// (such as a seek window) are placed before -i so FFmpeg fast-seeks by input
// rather than decoding every candidate window from the start of the file.
func runAnalysisFFmpeg(ctx context.Context, path string, preInput []string, filterArgs ...string) error {
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, preInput...)
	args = append(args, "-i", path)
	args = append(args, filterArgs...)
	args = append(args, "-f", "null", "-")
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// escapeFilterValue escapes a value (such as a metadata sink path) for safe
// interpolation into an FFmpeg filtergraph option, where backslash, colon,
// single quote, and space are all significant.
func escapeFilterValue(value string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`:`, `\:`,
		`'`, `\'`,
		` `, `\ `,
	).Replace(value)
}

// metadataFile creates a temporary file for an FFmpeg metadata=print sink and
// returns its path plus a cleanup function.
func metadataFile(kind string) (string, func(), error) {
	file, err := os.CreateTemp("", "ave-"+kind+"-*.txt")
	if err != nil {
		return "", func() {}, fmt.Errorf("create %s metadata file: %w", kind, err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("close %s metadata file: %w", kind, closeErr)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

// formatSeconds renders a duration for FFmpeg's -ss/-t arguments.
func formatSeconds(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', 6, 64)
}
