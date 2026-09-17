package edit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RenderOptions controls rerendering a previously saved Edit Plan.
type RenderOptions struct {
	PlanPath  string
	MediaRoot string
	Force     bool
}

// RenderPlan rerenders a saved Edit Plan into its Finished Video without
// performing analysis or contacting any model. It verifies that every Source
// Clip still matches the fingerprints recorded in the plan, supports relocating
// relative media with an explicit media root, and never leaves incomplete
// output behind.
func RenderPlan(ctx context.Context, options RenderOptions) (Result, error) {
	planPath, err := filepath.Abs(options.PlanPath)
	if err != nil {
		return Result{}, fmt.Errorf("resolve Edit Plan path: %w", err)
	}
	plan, err := loadPlan(planPath)
	if err != nil {
		return Result{}, err
	}

	planDir := filepath.Dir(planPath)
	baseDir := planDir
	if options.MediaRoot != "" {
		baseDir, err = filepath.Abs(options.MediaRoot)
		if err != nil {
			return Result{}, fmt.Errorf("resolve media root: %w", err)
		}
		info, statErr := os.Stat(baseDir)
		if statErr != nil {
			return Result{}, fmt.Errorf("open media root: %w", statErr)
		}
		if !info.IsDir() {
			return Result{}, fmt.Errorf("media root is not a folder: %s", baseDir)
		}
	}

	outputPath := plan.FinishedVideo
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(planDir, outputPath)
	}

	inputs, err := verifyPlanSources(plan, baseDir)
	if err != nil {
		return Result{}, err
	}
	if err := ensureAvailable(outputPath, options.Force); err != nil {
		return Result{}, err
	}
	if err := renderPlanToFile(ctx, inputs, plan.Render, outputPath); err != nil {
		return Result{}, err
	}
	return Result{PlanPath: planPath, VideoPath: outputPath}, nil
}

func loadPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, fmt.Errorf("open Edit Plan: %w", err)
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Plan{}, fmt.Errorf("decode Edit Plan %s: %w", path, err)
	}
	if plan.Version != planVersion {
		return Plan{}, fmt.Errorf("unsupported Edit Plan version %q; expected %q", plan.Version, planVersion)
	}
	if len(plan.SelectedSegments) == 0 {
		return Plan{}, fmt.Errorf("no Selected Segments in Edit Plan")
	}
	if plan.FinishedVideo == "" {
		return Plan{}, fmt.Errorf("missing Finished Video in Edit Plan")
	}
	if err := plan.Render.validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// verifyPlanSources resolves and fingerprint-checks every Selected Segment,
// reporting all missing or changed Source Clips together so a single run fixes
// the whole project.
func verifyPlanSources(plan Plan, baseDir string) ([]renderInput, error) {
	inputs := make([]renderInput, 0, len(plan.SelectedSegments))
	var problems []string
	for _, segment := range plan.SelectedSegments {
		resolved := segment.SourcePath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(baseDir, resolved)
		}
		if segment.SourceFingerprint == "" {
			problems = append(problems, fmt.Sprintf("%s: Edit Plan is missing a source fingerprint", segment.SourcePath))
			continue
		}
		if segment.EndSecond <= segment.StartSecond {
			problems = append(problems, fmt.Sprintf("%s: Selected Segment has an empty range", segment.SourcePath))
			continue
		}
		actual, err := sourceFingerprint(resolved)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("%s: Source Clip is missing (looked in %s)", segment.SourcePath, resolved))
			} else {
				problems = append(problems, fmt.Sprintf("%s: %v", segment.SourcePath, err))
			}
			continue
		}
		if actual != segment.SourceFingerprint {
			problems = append(problems, fmt.Sprintf("%s: Source Clip changed since the Edit Plan was created", segment.SourcePath))
			continue
		}
		inputs = append(inputs, renderInput{path: resolved, start: segment.StartSecond, end: segment.EndSecond})
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf(
			"cannot render Edit Plan; %d Source Clip problem(s):\n  %s",
			len(problems),
			strings.Join(problems, "\n  "),
		)
	}
	return inputs, nil
}

// renderPlanToFile renders into a temporary file and atomically promotes it on
// success, removing the incomplete file when rendering fails.
func renderPlanToFile(
	ctx context.Context,
	inputs []renderInput,
	settings RenderSettings,
	outputPath string,
) (resultErr error) {
	temp, err := os.CreateTemp(filepath.Dir(outputPath), ".ave-render-*"+filepath.Ext(outputPath))
	if err != nil {
		return fmt.Errorf("create temporary Finished Video: %w", err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, fmt.Errorf("remove temporary Finished Video: %w", removeErr))
		}
		return fmt.Errorf("prepare temporary Finished Video: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) && resultErr == nil {
			resultErr = fmt.Errorf("remove incomplete Finished Video: %w", err)
		}
	}()

	args := encodeArgs(inputs, settings)
	args = append(args, "-y", "-f", settings.Container, tempPath)
	command := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render Finished Video: %s: %w", strings.TrimSpace(string(output)), err)
	}
	// os.CreateTemp yields 0600; match the 0644 mode ave edit produces so both
	// render paths write the Finished Video with identical permissions.
	if err := os.Chmod(tempPath, 0o644); err != nil {
		return fmt.Errorf("set Finished Video permissions: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("finalize Finished Video: %w", err)
	}
	committed = true
	return nil
}
