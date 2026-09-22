// Package edit creates baseline Edit Plans and Finished Videos from Source Clips.
package edit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cbellee/auto-video-editor/internal/ffmpeg"
	"github.com/cbellee/auto-video-editor/internal/lmstudio"
)

const planVersion = "1"

// minUsableSeconds is the hard floor below which an edit fails rather than
// claiming success with a meaningless result.
const minUsableSeconds = 5.0

var supportedExtensions = map[string]bool{
	".avi": true,
	".mkv": true,
	".mov": true,
	".mp4": true,
}

// Options controls a baseline Chronological edit.
type Options struct {
	SourceDir string
	Output    string
	PlanOnly  bool
	Force     bool
	// FrameRate is the output frame rate; 0 selects the 30 fps default.
	FrameRate int
	// Aspect forces output orientation ("landscape" or "portrait"); empty
	// selects orientation from the majority usable-footage duration.
	Aspect string
	// Interactive allows prompting to resolve an orientation tie.
	Interactive bool
	// Stdin supplies interactive prompt answers; Prompt receives prompt text.
	Stdin  io.Reader
	Prompt io.Writer
	// QualityProfile selects gate strictness ("strict"/"balanced"/"lenient");
	// empty selects balanced.
	QualityProfile string
	// ShakeTreatment selects "reject" (default) or "stabilize" for shaky footage.
	ShakeTreatment string
	// Model names the LM Studio vision model to rank footage with. Empty uses
	// the remembered last-successful model, or an interactive choice.
	Model string
	// Guidance is optional free-text Selection Guidance folded into the ranking
	// prompt to bias selection among technically-eligible footage.
	Guidance string
	// Duration overrides the automatic target Finished Video length, in seconds.
	Duration float64
}

// allowedFrameRates enumerates the output frame rates the MVP supports.
var allowedFrameRates = map[int]bool{24: true, 25: true, 30: true, 60: true}

const defaultFrameRate = 30

// orientation is the output framing derived from usable footage.
type orientation int

const (
	orientationLandscape orientation = iota
	orientationPortrait
)

func (o orientation) String() string {
	if o == orientationPortrait {
		return "portrait"
	}
	return "landscape"
}

// Result identifies the artifacts created by an edit.
type Result struct {
	PlanPath  string
	VideoPath string
}

// Plan is the versioned baseline Edit Plan written by ave.
type Plan struct {
	Version           string             `json:"version"`
	EditIntent        string             `json:"edit_intent"`
	FinishedVideo     string             `json:"finished_video"`
	FinishedVideoSHA  string             `json:"finished_video_sha256,omitempty"`
	Analysis          AnalysisSettings   `json:"analysis"`
	Ranking           *RankingSettings   `json:"ranking,omitempty"`
	Audio             AudioSettings      `json:"audio"`
	SelectedSegments  []SelectedSegment  `json:"selected_segments"`
	RankedOutSegments []RankedOutSegment `json:"ranked_out_segments,omitempty"`
	RejectedSegments  []RejectedSegment  `json:"rejected_segments,omitempty"`
	Skipped           []SkippedClip      `json:"skipped,omitempty"`
	Render            RenderSettings     `json:"render"`
}

// RankingSettings records the AI ranking provenance so a plan is reproducible
// and auditable: which model scored the footage, from where, with what prompt
// and guidance, and the duration budget that shaped selection.
type RankingSettings struct {
	Model          string  `json:"model"`
	BaseURL        string  `json:"base_url"`
	SystemPrompt   string  `json:"system_prompt"`
	Guidance       string  `json:"guidance,omitempty"`
	TargetSeconds  float64 `json:"target_seconds"`
	SelectedSecond float64 `json:"selected_seconds"`
	DurationSource string  `json:"duration_source"`
}

// AudioSettings records the Finished Video audio decision. The baseline
// chronological edit keeps each segment's source audio.
type AudioSettings struct {
	Source string `json:"source"`
}

// SegmentScore is the AI assessment of a Candidate Segment, retained in the
// plan as provenance.
type SegmentScore struct {
	VisualInterest float64  `json:"visual_interest"`
	Energy         float64  `json:"energy"`
	Usefulness     float64  `json:"usefulness"`
	Redundancy     float64  `json:"redundancy"`
	Subjects       []string `json:"subjects,omitempty"`
	Actions        []string `json:"actions,omitempty"`
	Base           float64  `json:"base_score"`
}

// RankedOutSegment records an eligible Candidate Segment the model ranked out
// of the Finished Video for diversity or the duration budget.
type RankedOutSegment struct {
	SourcePath  string       `json:"source_path"`
	StartSecond float64      `json:"start_seconds"`
	EndSecond   float64      `json:"end_seconds"`
	Score       SegmentScore `json:"score"`
	Reason      string       `json:"reason"`
}

// SkippedClip records a Source Clip that was excluded and why.
type SkippedClip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SelectedSegment identifies a source range included in an Edit Plan.
type SelectedSegment struct {
	SourcePath         string         `json:"source_path"`
	SourceFingerprint  string         `json:"source_fingerprint,omitempty"`
	SourceIsHDR        bool           `json:"source_is_hdr,omitempty"`
	StartSecond        float64        `json:"start_seconds"`
	EndSecond          float64        `json:"end_seconds"`
	Metrics            SegmentMetrics `json:"metrics"`
	Shaky              bool           `json:"shaky,omitempty"`
	NeedsStabilization bool           `json:"needs_stabilization,omitempty"`
	// Transition into this segment; the baseline chronological edit hard-cuts.
	Transition string `json:"transition"`
	// SampleTimes are the source timestamps of the contact-sheet frames the
	// model scored, retained for traceability.
	SampleTimes []float64 `json:"sample_times,omitempty"`
	// Score is the AI assessment that ranked this segment in.
	Score *SegmentScore `json:"score,omitempty"`
	// Prompt is the user prompt sent to the model for this segment.
	Prompt string `json:"prompt,omitempty"`
	// ScoreRepaired reports whether the model's first response needed one
	// schema-guided repair before it validated.
	ScoreRepaired bool `json:"score_repaired,omitempty"`
}

// RenderSettings describes the baseline Finished Video encoding.
type RenderSettings struct {
	Container          string `json:"container"`
	VideoCodec         string `json:"video_codec"`
	AudioCodec         string `json:"audio_codec"`
	VideoWidth         int    `json:"video_width"`
	VideoHeight        int    `json:"video_height"`
	VideoFrameRate     int    `json:"video_frame_rate"`
	PixelFormat        string `json:"pixel_format"`
	AudioSampleRate    int    `json:"audio_sample_rate"`
	AudioChannels      int    `json:"audio_channels"`
	AudioChannelLayout string `json:"audio_channel_layout"`
	FastStart          bool   `json:"fast_start"`
}

// validate reports whether the render settings are complete enough to drive
// FFmpeg, guarding rerenders against malformed Edit Plans.
func (settings RenderSettings) validate() error {
	missing := make([]string, 0)
	if settings.Container == "" {
		missing = append(missing, "container")
	}
	if settings.VideoCodec == "" {
		missing = append(missing, "video_codec")
	}
	if settings.AudioCodec == "" {
		missing = append(missing, "audio_codec")
	}
	if settings.PixelFormat == "" {
		missing = append(missing, "pixel_format")
	}
	if settings.AudioChannelLayout == "" {
		missing = append(missing, "audio_channel_layout")
	}
	if settings.VideoWidth <= 0 {
		missing = append(missing, "video_width")
	}
	if settings.VideoHeight <= 0 {
		missing = append(missing, "video_height")
	}
	if settings.VideoFrameRate <= 0 {
		missing = append(missing, "video_frame_rate")
	}
	if settings.AudioSampleRate <= 0 {
		missing = append(missing, "audio_sample_rate")
	}
	if settings.AudioChannels <= 0 {
		missing = append(missing, "audio_channels")
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete Edit Plan render settings: %s", strings.Join(missing, ", "))
	}
	return nil
}

type sourceClip struct {
	path       string
	duration   float64
	capturedAt time.Time
	width      int
	height     int
	isHDR      bool
}

// orientation classifies the clip from its rotation-corrected dimensions.
func (c sourceClip) orientation() orientation {
	if c.height > c.width {
		return orientationPortrait
	}
	return orientationLandscape
}

// Run creates a baseline Chronological Edit Plan and optionally renders it.
func Run(ctx context.Context, options Options) (Result, error) {
	sourceDir, err := filepath.Abs(options.SourceDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve source folder: %w", err)
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		return Result{}, fmt.Errorf("open source folder: %w", err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("source path is not a folder: %s", sourceDir)
	}

	result, err := artifactPaths(sourceDir, options.Output)
	if err != nil {
		return Result{}, err
	}
	if err := ensureAvailable(result.PlanPath, options.Force); err != nil {
		return Result{}, err
	}
	if err := ensureAvailable(result.VideoPath, options.Force); err != nil {
		return Result{}, err
	}

	excludedPaths := make(map[string]bool)
	if options.Force && priorPlanOwnsOutput(result.PlanPath, result.VideoPath) {
		excludedPaths[result.VideoPath] = true
	}
	supportedPaths, skipped, err := sourceClips(sourceDir, excludedPaths)
	if err != nil {
		return Result{}, err
	}
	warnSkipped(options.Prompt, skipped)
	if len(supportedPaths) == 0 {
		return Result{}, fmt.Errorf("no supported Source Clips found in %s", sourceDir)
	}

	clips := make([]sourceClip, 0, len(supportedPaths))
	for _, sourcePath := range supportedPaths {
		clip, probeErr := probeSourceClip(ctx, sourcePath)
		if probeErr != nil {
			reason := probeErr.Error()
			skipped = append(skipped, SkippedClip{Path: sourcePath, Reason: reason})
			warnSkipped(options.Prompt, []SkippedClip{{Path: sourcePath, Reason: reason}})
			continue
		}
		clips = append(clips, clip)
	}

	var usableDuration float64
	for _, clip := range clips {
		usableDuration += clip.duration
	}
	if usableDuration < minUsableSeconds {
		return Result{}, fmt.Errorf(
			"less than five seconds of usable footage remains (%.2fs after skipping %d unusable clip(s))",
			usableDuration,
			len(skipped),
		)
	}

	if err := rejectSourceCollision(result, clipPaths(clips)); err != nil {
		return Result{}, err
	}

	sort.Slice(clips, func(left, right int) bool {
		if clips[left].capturedAt.Equal(clips[right].capturedAt) {
			return filepath.Base(clips[left].path) < filepath.Base(clips[right].path)
		}
		return clips[left].capturedAt.Before(clips[right].capturedAt)
	})

	outputOrientation, err := selectOrientation(clips, options)
	if err != nil {
		return Result{}, err
	}

	analysis, err := resolveAnalysisConfig(options)
	if err != nil {
		return Result{}, err
	}

	planDir := filepath.Dir(result.PlanPath)
	var eligible []eligibleCandidate
	var rejected []RejectedSegment
	var eligibleDuration float64
	order := 0
	for _, clip := range clips {
		fingerprint, fingerprintErr := sourceFingerprint(clip.path)
		if fingerprintErr != nil {
			return Result{}, fingerprintErr
		}
		candidates, analyzeErr := analyzeClip(ctx, clip.path, clip, analysis)
		if analyzeErr != nil {
			return Result{}, analyzeErr
		}
		relPath := planRelativePath(planDir, clip.path)
		for _, candidate := range candidates {
			duration := candidate.rng.end - candidate.rng.start
			if !candidate.gate.eligible {
				rejected = append(rejected, RejectedSegment{
					SourcePath:  relPath,
					StartSecond: candidate.rng.start,
					EndSecond:   candidate.rng.end,
					Metrics:     candidate.metrics,
					Reasons:     candidate.gate.reasons,
					HardFailure: candidate.gate.hardFailure,
				})
				continue
			}
			eligible = append(eligible, eligibleCandidate{
				clipPath:           clip.path,
				relPath:            relPath,
				fingerprint:        fingerprint,
				isHDR:              clip.isHDR,
				rng:                candidate.rng,
				metrics:            candidate.metrics,
				shaky:              candidate.gate.shaky,
				needsStabilization: candidate.gate.needsStabilization,
				order:              order,
			})
			order++
			eligibleDuration += duration
		}
	}

	if eligibleDuration < minUsableSeconds {
		return Result{}, fmt.Errorf(
			"less than five seconds of usable footage remains after quality gating (%.2fs eligible, %d candidate(s) rejected)",
			eligibleDuration,
			len(rejected),
		)
	}

	client, err := lmstudio.FromEnv()
	if err != nil {
		return Result{}, err
	}
	model, err := resolveModel(ctx, client, options)
	if err != nil {
		return Result{}, err
	}

	rankingClient := &ranker{client: client, model: model, guidance: options.Guidance}
	ranked, err := rankingClient.rankAll(ctx, eligible)
	if err != nil {
		return Result{}, err
	}

	durationSource := "automatic"
	if options.Duration > 0 {
		durationSource = "override"
	}
	targetSeconds := automaticDuration(eligibleDuration, options.Duration)
	selected, droppedOut := selectChronological(ranked, targetSeconds)

	segments := make([]SelectedSegment, 0, len(selected))
	renderInputs := make([]renderInput, 0, len(selected))
	var selectedDuration float64
	for _, candidate := range selected {
		score := scoreFrom(candidate)
		segments = append(segments, SelectedSegment{
			SourcePath:         candidate.relPath,
			SourceFingerprint:  candidate.fingerprint,
			SourceIsHDR:        candidate.isHDR,
			StartSecond:        candidate.rng.start,
			EndSecond:          candidate.rng.end,
			Metrics:            candidate.metrics,
			Shaky:              candidate.shaky,
			NeedsStabilization: candidate.needsStabilization,
			Transition:         transitionCut,
			SampleTimes:        candidate.sampleTimes,
			Score:              &score,
			Prompt:             candidate.prompt,
			ScoreRepaired:      candidate.repaired,
		})
		renderInputs = append(renderInputs, renderInput{
			path:  candidate.clipPath,
			start: candidate.rng.start,
			end:   candidate.rng.end,
			isHDR: candidate.isHDR,
		})
		selectedDuration += candidate.rng.end - candidate.rng.start
	}

	rankedOut := make([]RankedOutSegment, 0, len(droppedOut))
	for _, candidate := range droppedOut {
		rankedOut = append(rankedOut, RankedOutSegment{
			SourcePath:  candidate.relPath,
			StartSecond: candidate.rng.start,
			EndSecond:   candidate.rng.end,
			Score:       scoreFrom(candidate),
			Reason:      "ranked out for diversity or duration budget",
		})
	}

	renderSettings, err := renderSettingsFor(ctx, outputOrientation, options.FrameRate)
	if err != nil {
		return Result{}, err
	}
	plan := Plan{
		Version:       planVersion,
		EditIntent:    "chronological",
		FinishedVideo: result.VideoPath,
		Analysis: AnalysisSettings{
			QualityProfile: analysis.profile,
			ShakeTreatment: analysis.shake,
			Thresholds:     analysis.thresholds,
		},
		Ranking: &RankingSettings{
			Model:          model,
			BaseURL:        client.BaseURL(),
			SystemPrompt:   systemPrompt(options.Guidance),
			Guidance:       options.Guidance,
			TargetSeconds:  targetSeconds,
			SelectedSecond: selectedDuration,
			DurationSource: durationSource,
		},
		Audio:             AudioSettings{Source: audioSource},
		SelectedSegments:  segments,
		RankedOutSegments: rankedOut,
		RejectedSegments:  rejected,
		Skipped:           skipped,
		Render:            renderSettings,
	}
	if err := validatePlan(plan); err != nil {
		return Result{}, err
	}
	if err := writePlan(result.PlanPath, plan, options.Force); err != nil {
		return Result{}, err
	}
	rememberModel(model)

	if options.PlanOnly {
		result.VideoPath = ""
		return result, nil
	}
	if err := render(ctx, renderInputs, result.VideoPath, renderSettings, options.Force); err != nil {
		return Result{}, err
	}
	fingerprint, err := fileSHA256(result.VideoPath)
	if err != nil {
		return Result{}, err
	}
	plan.FinishedVideoSHA = fingerprint
	if err := replacePlan(result.PlanPath, plan); err != nil {
		if removeErr := os.Remove(result.VideoPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return Result{}, errors.Join(err, fmt.Errorf("remove unrecorded Finished Video: %w", removeErr))
		}
		return Result{}, err
	}
	return result, nil
}

func sourceClips(sourceDir string, excludedPaths map[string]bool) ([]string, []SkippedClip, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, nil, fmt.Errorf("read source folder: %w", err)
	}
	paths := make([]string, 0, len(entries))
	var skipped []SkippedClip
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(sourceDir, name)
		if !supportedExtensions[strings.ToLower(filepath.Ext(name))] {
			skipped = append(skipped, SkippedClip{Path: path, Reason: "unsupported file type"})
			continue
		}
		if excludedPaths[path] {
			continue
		}
		paths = append(paths, path)
	}
	return paths, skipped, nil
}

// clipPaths returns the filesystem paths of the usable clips.
func clipPaths(clips []sourceClip) []string {
	paths := make([]string, len(clips))
	for index, clip := range clips {
		paths[index] = clip.path
	}
	return paths
}

// warnSkipped emits a prominent warning per skipped Source Clip so a mixed
// folder does not silently drop footage.
func warnSkipped(writer io.Writer, skipped []SkippedClip) {
	if writer == nil {
		return
	}
	for _, clip := range skipped {
		_, _ = fmt.Fprintf(writer, "warning: skipping %s: %s\n", clip.Path, clip.Reason)
	}
}

// selectOrientation chooses the Finished Video orientation from an explicit
// aspect override or the majority usable-footage duration, resolving ties
// interactively or by requiring an explicit aspect.
func selectOrientation(clips []sourceClip, options Options) (orientation, error) {
	switch options.Aspect {
	case "landscape":
		return orientationLandscape, nil
	case "portrait":
		return orientationPortrait, nil
	case "":
	default:
		return orientationLandscape, fmt.Errorf("invalid aspect %q; use landscape or portrait", options.Aspect)
	}

	var landscapeDuration, portraitDuration float64
	for _, clip := range clips {
		if clip.orientation() == orientationPortrait {
			portraitDuration += clip.duration
		} else {
			landscapeDuration += clip.duration
		}
	}
	switch {
	case portraitDuration > landscapeDuration:
		return orientationPortrait, nil
	case landscapeDuration > portraitDuration:
		return orientationLandscape, nil
	default:
		return resolveOrientationTie(options)
	}
}

// resolveOrientationTie prompts for an orientation when interactive, and
// otherwise requires an explicit aspect so scripted runs stay deterministic.
func resolveOrientationTie(options Options) (orientation, error) {
	tieErr := fmt.Errorf("portrait and landscape footage are tied; pass --aspect landscape|portrait")
	if !options.Interactive || options.Prompt == nil || options.Stdin == nil {
		return orientationLandscape, tieErr
	}
	reader := bufio.NewReader(options.Stdin)
	for {
		_, _ = fmt.Fprint(options.Prompt, "Portrait and landscape footage are tied. Choose aspect [landscape/portrait]: ")
		line, readErr := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "landscape", "l":
			return orientationLandscape, nil
		case "portrait", "p":
			return orientationPortrait, nil
		}
		if readErr != nil {
			return orientationLandscape, tieErr
		}
	}
}

func priorPlanOwnsOutput(planPath, videoPath string) bool {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return false
	}
	var plan Plan
	if json.Unmarshal(data, &plan) != nil ||
		plan.Version != planVersion ||
		plan.FinishedVideo != videoPath ||
		plan.FinishedVideoSHA == "" {
		return false
	}
	for _, segment := range plan.SelectedSegments {
		if segment.SourcePath == videoPath {
			return false
		}
	}
	fingerprint, err := fileSHA256(videoPath)
	return err == nil && fingerprint == plan.FinishedVideoSHA
}

func artifactPaths(sourceDir, output string) (Result, error) {
	if output != "" {
		videoPath, err := filepath.Abs(output)
		if err != nil {
			return Result{}, fmt.Errorf("resolve output path: %w", err)
		}
		extension := filepath.Ext(videoPath)
		planPath := strings.TrimSuffix(videoPath, extension) + ".plan.json"
		return Result{PlanPath: planPath, VideoPath: videoPath}, nil
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return Result{}, fmt.Errorf("find current directory: %w", err)
	}
	baseName := filepath.Base(filepath.Clean(sourceDir)) + "-edit"
	return Result{
		PlanPath:  filepath.Join(workingDir, baseName+".plan.json"),
		VideoPath: filepath.Join(workingDir, baseName+".mp4"),
	}, nil
}

func ensureAvailable(path string, force bool) error {
	_, err := os.Stat(path)
	switch {
	case err == nil && !force:
		return fmt.Errorf("refusing to overwrite existing artifact %s; use --force", path)
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("inspect output artifact %s: %w", path, err)
	}
}

func rejectSourceCollision(result Result, sourcePaths []string) error {
	for _, artifactPath := range []string{result.PlanPath, result.VideoPath} {
		if artifactPath == "" {
			continue
		}
		artifactInfo, err := os.Stat(artifactPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect output artifact %s: %w", artifactPath, err)
		}
		for _, sourcePath := range sourcePaths {
			sourceInfo, err := os.Stat(sourcePath)
			if err != nil {
				return fmt.Errorf("inspect Source Clip %s: %w", sourcePath, err)
			}
			if os.SameFile(artifactInfo, sourceInfo) {
				return fmt.Errorf("output artifact must not overwrite Source Clip %s", sourcePath)
			}
		}
	}
	return nil
}

func probeSourceClip(ctx context.Context, sourcePath string) (sourceClip, error) {
	command := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries",
		"stream=width,height,color_transfer:stream_tags=rotate:stream_side_data=rotation:"+
			"format=duration:format_tags=creation_time",
		"-of", "json",
		sourcePath,
	)
	output, err := command.Output()
	if err != nil {
		return sourceClip{}, fmt.Errorf("probe Source Clip %s: %w", sourcePath, err)
	}
	var response struct {
		Streams []struct {
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			ColorTransfer string `json:"color_transfer"`
			Tags          struct {
				Rotate string `json:"rotate"`
			} `json:"tags"`
			SideDataList []struct {
				Rotation int `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Tags     struct {
				CreationTime string `json:"creation_time"`
			} `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return sourceClip{}, fmt.Errorf("decode ffprobe response for %s: %w", sourcePath, err)
	}
	duration, err := strconv.ParseFloat(response.Format.Duration, 64)
	if err != nil {
		return sourceClip{}, fmt.Errorf(
			"parse duration %q for Source Clip %s: %w",
			response.Format.Duration,
			sourcePath,
			err,
		)
	}
	if duration <= 0 {
		return sourceClip{}, fmt.Errorf("invalid duration %q for Source Clip %s", response.Format.Duration, sourcePath)
	}

	clip := sourceClip{path: sourcePath, duration: duration}
	if len(response.Streams) > 0 {
		stream := response.Streams[0]
		clip.width, clip.height = displayDimensions(stream.Width, stream.Height, streamRotation(stream.SideDataList, stream.Tags.Rotate))
		clip.isHDR = isHDRTransfer(stream.ColorTransfer)
	}

	capturedAt, err := time.Parse(time.RFC3339Nano, response.Format.Tags.CreationTime)
	if err != nil {
		info, statErr := os.Stat(sourcePath)
		if statErr != nil {
			return sourceClip{}, fmt.Errorf("inspect Source Clip %s: %w", sourcePath, statErr)
		}
		capturedAt = info.ModTime()
	}
	clip.capturedAt = capturedAt
	return clip, nil
}

// streamRotation resolves the display rotation in degrees from either the
// display-matrix side data or the legacy rotate tag.
func streamRotation(sideData []struct {
	Rotation int `json:"rotation"`
}, rotateTag string) int {
	if len(sideData) > 0 {
		return sideData[0].Rotation
	}
	if rotateTag != "" {
		if rotation, err := strconv.Atoi(rotateTag); err == nil {
			return rotation
		}
	}
	return 0
}

// displayDimensions returns the width and height as displayed after applying a
// quarter-turn rotation, so orientation follows what a viewer sees.
func displayDimensions(width, height, rotation int) (int, int) {
	normalized := ((rotation % 360) + 360) % 360
	if normalized == 90 || normalized == 270 {
		return height, width
	}
	return width, height
}

// isHDRTransfer reports whether a color transfer characteristic denotes HDR
// (PQ or HLG), which requires tone mapping to SDR Rec.709.
func isHDRTransfer(transfer string) bool {
	switch transfer {
	case "smpte2084", "arib-std-b67":
		return true
	default:
		return false
	}
}

func writePlan(path string, plan Plan, force bool) error {
	data, err := encodePlan(plan)
	if err != nil {
		return err
	}

	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("refusing to overwrite existing artifact %s; use --force", path)
	}
	if err != nil {
		return fmt.Errorf("create Edit Plan: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return closeWithError(file, fmt.Errorf("write Edit Plan: %w", err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Edit Plan: %w", err)
	}
	return nil
}

func replacePlan(path string, plan Plan) (resultErr error) {
	data, err := encodePlan(plan)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ave-plan-*.json")
	if err != nil {
		return fmt.Errorf("create replacement Edit Plan: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) && resultErr == nil {
			resultErr = fmt.Errorf("remove temporary Edit Plan: %w", err)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return closeWithError(file, fmt.Errorf("write replacement Edit Plan: %w", err))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close replacement Edit Plan: %w", err)
	}
	if err := os.Chmod(tempPath, 0o644); err != nil {
		return fmt.Errorf("set replacement Edit Plan permissions: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace Edit Plan: %w", err)
	}
	return nil
}

func encodePlan(plan Plan) ([]byte, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Edit Plan: %w", err)
	}
	return append(data, '\n'), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Finished Video for fingerprinting: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", closeWithError(file, fmt.Errorf("fingerprint Finished Video: %w", err))
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close Finished Video after fingerprinting: %w", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// canvasDimensions returns the 1080p-capped output dimensions for the chosen
// orientation, preserving 16:9 (landscape) or 9:16 (portrait).
func canvasDimensions(o orientation) (int, int) {
	if o == orientationPortrait {
		return 1080, 1920
	}
	return 1920, 1080
}

func renderSettingsFor(ctx context.Context, o orientation, frameRate int) (RenderSettings, error) {
	if frameRate == 0 {
		frameRate = defaultFrameRate
	}
	if !allowedFrameRates[frameRate] {
		return RenderSettings{}, fmt.Errorf("unsupported frame rate %d; use 24, 25, 30, or 60", frameRate)
	}

	output, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return RenderSettings{}, fmt.Errorf(
			"inspect FFmpeg encoders: %s: %w",
			strings.TrimSpace(string(output)),
			err,
		)
	}
	encoders := ffmpeg.CapabilityNames(string(output))
	videoCodec := ""
	switch {
	case encoders["h264_videotoolbox"]:
		videoCodec = "h264_videotoolbox"
	case encoders["libx264"]:
		videoCodec = "libx264"
	default:
		return RenderSettings{}, fmt.Errorf("FFmpeg does not provide a supported H.264 encoder")
	}

	width, height := canvasDimensions(o)
	return RenderSettings{
		Container:          "mp4",
		VideoCodec:         videoCodec,
		AudioCodec:         "aac",
		VideoWidth:         width,
		VideoHeight:        height,
		VideoFrameRate:     frameRate,
		PixelFormat:        "yuv420p",
		AudioSampleRate:    48000,
		AudioChannels:      2,
		AudioChannelLayout: "stereo",
		FastStart:          true,
	}, nil
}

func render(
	ctx context.Context,
	inputs []renderInput,
	outputPath string,
	settings RenderSettings,
	force bool,
) error {
	args := encodeArgs(inputs, settings)
	if force {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-f", settings.Container, outputPath)
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render Finished Video: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// renderInput is a resolved Source Clip range to include in a Finished Video.
type renderInput struct {
	path  string
	start float64
	end   float64
	isHDR bool
}

// encodeArgs builds the FFmpeg arguments common to every render, excluding the
// trailing overwrite flag and output path. Source metadata is discarded so that
// location, device, and capture details never reach the Finished Video. Each
// clip is fit onto the canvas without distortion and letterboxed with a blurred
// copy of itself, HDR footage is tone mapped to SDR, and the result is tagged
// Rec.709.
func encodeArgs(inputs []renderInput, settings RenderSettings) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}

	// Dedupe Source Clips: opening the same file as several -i inputs can
	// deadlock FFmpeg, so each unique clip is opened once and split into one
	// branch per Candidate Segment that draws from it.
	inputIndex := make(map[string]int)
	var uniquePaths []string
	segmentCount := make(map[string]int)
	for _, input := range inputs {
		if _, seen := inputIndex[input.path]; !seen {
			inputIndex[input.path] = len(uniquePaths)
			uniquePaths = append(uniquePaths, input.path)
		}
		segmentCount[input.path]++
	}

	var totalDuration float64
	for _, input := range inputs {
		totalDuration += input.end - input.start
	}
	for _, path := range uniquePaths {
		args = append(args, "-i", path)
	}
	audioInputIndex := len(uniquePaths)
	args = append(
		args,
		"-f", "lavfi",
		"-i", fmt.Sprintf(
			"anullsrc=channel_layout=%s:sample_rate=%d",
			settings.AudioChannelLayout,
			settings.AudioSampleRate,
		),
	)

	var filter strings.Builder

	// Split each unique input into one labelled branch per Candidate Segment so
	// that a decoded pad is never consumed more than once.
	sourceBranches := make([][]string, len(uniquePaths))
	for unique, path := range uniquePaths {
		count := segmentCount[path]
		fmt.Fprintf(&filter, "[%d:v:0]split=%d", unique, count)
		branches := make([]string, count)
		for branch := 0; branch < count; branch++ {
			label := fmt.Sprintf("src%d_%d", unique, branch)
			branches[branch] = label
			fmt.Fprintf(&filter, "[%s]", label)
		}
		filter.WriteString(";")
		sourceBranches[unique] = branches
	}

	nextBranch := make([]int, len(uniquePaths))
	for index, input := range inputs {
		unique := inputIndex[input.path]
		sourceLabel := sourceBranches[unique][nextBranch[unique]]
		nextBranch[unique]++

		source := fmt.Sprintf("base%d", index)
		fmt.Fprintf(
			&filter,
			"[%s]trim=start=%s:end=%s,setpts=PTS-STARTPTS",
			sourceLabel,
			strconv.FormatFloat(input.start, 'f', 6, 64),
			strconv.FormatFloat(input.end, 'f', 6, 64),
		)
		if input.isHDR {
			filter.WriteString(
				",zscale=t=linear:npl=100,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv",
			)
		}
		fmt.Fprintf(&filter, "[%s];", source)
		fmt.Fprintf(&filter, "[%s]split=2[fg%d][bg%d];", source, index, index)
		fmt.Fprintf(
			&filter,
			"[bg%d]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=20:1,setsar=1[bgb%d];",
			index,
			settings.VideoWidth,
			settings.VideoHeight,
			settings.VideoWidth,
			settings.VideoHeight,
			index,
		)
		fmt.Fprintf(
			&filter,
			"[fg%d]scale=%d:%d:force_original_aspect_ratio=decrease,setsar=1[fgs%d];",
			index,
			settings.VideoWidth,
			settings.VideoHeight,
			index,
		)
		fmt.Fprintf(
			&filter,
			"[bgb%d][fgs%d]overlay=(W-w)/2:(H-h)/2,fps=%d,format=%s[v%d];",
			index,
			index,
			settings.VideoFrameRate,
			settings.PixelFormat,
			index,
		)
	}
	for index := range inputs {
		fmt.Fprintf(&filter, "[v%d]", index)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0[video]", len(inputs))

	args = append(args,
		"-filter_complex", filter.String(),
		"-map", "[video]",
		"-map", fmt.Sprintf("%d:a:0", audioInputIndex),
		"-t", strconv.FormatFloat(totalDuration, 'f', 6, 64),
		"-map_metadata", "-1",
		"-c:v", settings.VideoCodec,
		"-pix_fmt", settings.PixelFormat,
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-colorspace", "bt709",
		"-c:a", settings.AudioCodec,
		"-ar", strconv.Itoa(settings.AudioSampleRate),
		"-ac", strconv.Itoa(settings.AudioChannels),
	)
	if settings.FastStart {
		args = append(args, "-movflags", "+faststart")
	}
	return args
}

// fingerprintSampleSize bounds how much of each Source Clip is hashed so that
// identity checks stay fast on multi-gigabyte footage.
const fingerprintSampleSize = 4 << 20

// planRelativePath expresses target relative to planDir when possible so that a
// project directory can be moved with its media, falling back to the absolute
// path when no relative form exists.
func planRelativePath(planDir, target string) string {
	rel, err := filepath.Rel(planDir, target)
	if err != nil {
		return target
	}
	return rel
}

// sourceFingerprint derives a fast, content-derived identity for a Source Clip
// from its size and hashed head and tail samples. A missing file yields an
// error satisfying errors.Is(err, os.ErrNotExist).
func sourceFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Source Clip %s: %w", path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Source Clip %s: %w", path, err)
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, fingerprintSampleSize); err != nil && !errors.Is(err, io.EOF) {
		return "", closeWithError(file, fmt.Errorf("read Source Clip %s: %w", path, err))
	}
	if info.Size() > fingerprintSampleSize {
		if _, err := file.Seek(-fingerprintSampleSize, io.SeekEnd); err != nil {
			return "", closeWithError(file, fmt.Errorf("seek Source Clip %s: %w", path, err))
		}
		if _, err := io.CopyN(hash, file, fingerprintSampleSize); err != nil && !errors.Is(err, io.EOF) {
			return "", closeWithError(file, fmt.Errorf("read Source Clip %s: %w", path, err))
		}
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close Source Clip %s: %w", path, err)
	}
	return fmt.Sprintf("v1:%d:%x", info.Size(), hash.Sum(nil)), nil
}

func closeWithError(file *os.File, prior error) error {
	if err := file.Close(); err != nil {
		return errors.Join(prior, fmt.Errorf("close file: %w", err))
	}
	return prior
}
