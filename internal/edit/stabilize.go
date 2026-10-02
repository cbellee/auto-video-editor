package edit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// stabilizeInputs pre-renders a stabilized copy of every Selected Segment that
// the plan flagged for stabilization, using a two-pass vidstabdetect then
// vidstabtransform, and rewrites those inputs to draw from the stabilized copy
// (full length, from zero). Any detection or transform failure is surfaced as
// an error; unstabilized footage is never silently substituted for footage the
// plan promised to stabilize. The returned cleanup removes the temporary files.
func stabilizeInputs(ctx context.Context, inputs []renderInput) ([]renderInput, func(), error) {
	needsWork := false
	for _, input := range inputs {
		if input.needsStabilization {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return inputs, func() {}, nil
	}

	workDir, err := os.MkdirTemp("", "ave-stabilize-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create stabilization workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workDir) }

	stabilized := make([]renderInput, len(inputs))
	copy(stabilized, inputs)
	for index, input := range inputs {
		if !input.needsStabilization {
			continue
		}
		output, err := stabilizeSegment(ctx, workDir, index, input)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		stabilized[index] = renderInput{
			path:              output,
			start:             0,
			end:               input.end - input.start,
			isHDR:             input.isHDR,
			audioUsable:       input.audioUsable,
			transition:        input.transition,
			transitionSeconds: input.transitionSeconds,
		}
	}
	return stabilized, cleanup, nil
}

// stabilizeSegment runs the two-pass vidstab stabilization for one segment and
// returns the path to the stabilized clip.
func stabilizeSegment(ctx context.Context, workDir string, index int, input renderInput) (string, error) {
	duration := input.end - input.start
	transforms := filepath.Join(workDir, fmt.Sprintf("segment-%d.trf", index))
	output := filepath.Join(workDir, fmt.Sprintf("segment-%d.mp4", index))

	detectArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(input.start, 'f', 6, 64),
		"-i", input.path,
		"-t", strconv.FormatFloat(duration, 'f', 6, 64),
		"-vf", fmt.Sprintf("vidstabdetect=shakiness=8:result=%s", transforms),
		"-f", "null", "-",
	}
	if out, err := exec.CommandContext(ctx, "ffmpeg", detectArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("stabilization detect pass failed for %s: %s: %w",
			input.path, strings.TrimSpace(string(out)), err)
	}

	transformArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(input.start, 'f', 6, 64),
		"-i", input.path,
		"-t", strconv.FormatFloat(duration, 'f', 6, 64),
		"-vf", fmt.Sprintf("vidstabtransform=input=%s:smoothing=15", transforms),
		"-y", output,
	}
	if out, err := exec.CommandContext(ctx, "ffmpeg", transformArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("stabilization transform pass failed for %s: %s: %w",
			input.path, strings.TrimSpace(string(out)), err)
	}

	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("stabilization produced no output for %s", input.path)
	}
	return output, nil
}
