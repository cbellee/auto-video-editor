// Package edit creates baseline Edit Plans and Finished Videos from Source Clips.
package edit

import (
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
)

const planVersion = "1"

var supportedExtensions = map[string]bool{
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
}

// Result identifies the artifacts created by an edit.
type Result struct {
	PlanPath  string
	VideoPath string
}

// Plan is the versioned baseline Edit Plan written by ave.
type Plan struct {
	Version          string            `json:"version"`
	EditIntent       string            `json:"edit_intent"`
	FinishedVideo    string            `json:"finished_video"`
	FinishedVideoSHA string            `json:"finished_video_sha256,omitempty"`
	SelectedSegments []SelectedSegment `json:"selected_segments"`
	Render           RenderSettings    `json:"render"`
}

// SelectedSegment identifies a source range included in an Edit Plan.
type SelectedSegment struct {
	SourcePath        string  `json:"source_path"`
	SourceFingerprint string  `json:"source_fingerprint,omitempty"`
	StartSecond       float64 `json:"start_seconds"`
	EndSecond         float64 `json:"end_seconds"`
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
	sourcePaths, err := sourceClips(sourceDir, excludedPaths)
	if err != nil {
		return Result{}, err
	}
	if len(sourcePaths) == 0 {
		return Result{}, fmt.Errorf("no supported Source Clips found in %s", sourceDir)
	}

	if err := rejectSourceCollision(result, sourcePaths); err != nil {
		return Result{}, err
	}

	clips := make([]sourceClip, 0, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		clip, probeErr := probeSourceClip(ctx, sourcePath)
		if probeErr != nil {
			return Result{}, probeErr
		}
		clips = append(clips, clip)
	}
	sort.Slice(clips, func(left, right int) bool {
		if clips[left].capturedAt.Equal(clips[right].capturedAt) {
			return clips[left].path < clips[right].path
		}
		return clips[left].capturedAt.Before(clips[right].capturedAt)
	})

	segments := make([]SelectedSegment, 0, len(clips))
	planDir := filepath.Dir(result.PlanPath)
	for _, clip := range clips {
		fingerprint, fingerprintErr := sourceFingerprint(clip.path)
		if fingerprintErr != nil {
			return Result{}, fingerprintErr
		}
		segments = append(segments, SelectedSegment{
			SourcePath:        planRelativePath(planDir, clip.path),
			SourceFingerprint: fingerprint,
			StartSecond:       0,
			EndSecond:         clip.duration,
		})
	}

	renderSettings, err := baselineRenderSettings(ctx)
	if err != nil {
		return Result{}, err
	}
	plan := Plan{
		Version:          planVersion,
		EditIntent:       "chronological",
		FinishedVideo:    result.VideoPath,
		SelectedSegments: segments,
		Render:           renderSettings,
	}
	if err := writePlan(result.PlanPath, plan, options.Force); err != nil {
		return Result{}, err
	}

	if options.PlanOnly {
		result.VideoPath = ""
		return result, nil
	}
	if err := render(ctx, clips, result.VideoPath, renderSettings, options.Force); err != nil {
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

func sourceClips(sourceDir string, excludedPaths map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("read source folder: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !supportedExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			continue
		}
		path := filepath.Join(sourceDir, entry.Name())
		if excludedPaths[path] {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
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
		"-show_entries", "format=duration:format_tags=creation_time",
		"-of", "json",
		sourcePath,
	)
	output, err := command.Output()
	if err != nil {
		return sourceClip{}, fmt.Errorf("probe Source Clip %s: %w", sourcePath, err)
	}
	var response struct {
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

	capturedAt, err := time.Parse(time.RFC3339Nano, response.Format.Tags.CreationTime)
	if err != nil {
		info, statErr := os.Stat(sourcePath)
		if statErr != nil {
			return sourceClip{}, fmt.Errorf("inspect Source Clip %s: %w", sourcePath, statErr)
		}
		capturedAt = info.ModTime()
	}
	return sourceClip{path: sourcePath, duration: duration, capturedAt: capturedAt}, nil
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

func baselineRenderSettings(ctx context.Context) (RenderSettings, error) {
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
	case encoders["libx264"]:
		videoCodec = "libx264"
	case encoders["h264_videotoolbox"]:
		videoCodec = "h264_videotoolbox"
	default:
		return RenderSettings{}, fmt.Errorf("FFmpeg does not provide a supported H.264 encoder")
	}

	return RenderSettings{
		Container:          "mp4",
		VideoCodec:         videoCodec,
		AudioCodec:         "aac",
		VideoWidth:         1280,
		VideoHeight:        720,
		VideoFrameRate:     30,
		PixelFormat:        "yuv420p",
		AudioSampleRate:    48000,
		AudioChannels:      2,
		AudioChannelLayout: "stereo",
		FastStart:          true,
	}, nil
}

func render(
	ctx context.Context,
	clips []sourceClip,
	outputPath string,
	settings RenderSettings,
	force bool,
) error {
	inputs := make([]renderInput, len(clips))
	for index, clip := range clips {
		inputs[index] = renderInput{path: clip.path, start: 0, end: clip.duration}
	}
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
}

// encodeArgs builds the FFmpeg arguments common to every render, excluding the
// trailing overwrite flag and output path. Source metadata is discarded so that
// location, device, and capture details never reach the Finished Video.
func encodeArgs(inputs []renderInput, settings RenderSettings) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	var totalDuration float64
	for _, input := range inputs {
		args = append(args, "-i", input.path)
		totalDuration += input.end - input.start
	}
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
	for index, input := range inputs {
		fmt.Fprintf(
			&filter,
			"[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,"+
				"scale=%d:%d:force_original_aspect_ratio=decrease,"+
				"pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,format=%s[v%d];",
			index,
			strconv.FormatFloat(input.start, 'f', 6, 64),
			strconv.FormatFloat(input.end, 'f', 6, 64),
			settings.VideoWidth,
			settings.VideoHeight,
			settings.VideoWidth,
			settings.VideoHeight,
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
		"-map", fmt.Sprintf("%d:a:0", len(inputs)),
		"-t", strconv.FormatFloat(totalDuration, 'f', 6, 64),
		"-map_metadata", "-1",
		"-c:v", settings.VideoCodec,
		"-pix_fmt", settings.PixelFormat,
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
