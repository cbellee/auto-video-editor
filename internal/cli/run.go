// Package cli implements the public ave command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/cbellee/auto-video-editor/internal/edit"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
	editUsage   = "usage: ave edit <folder> [--output <file>] [--plan-only] [--force] [--fps <24|25|30|60>] [--aspect <landscape|portrait>] [--quality-profile <strict|balanced|lenient>] [--shake <reject|stabilize>] [--model <name>] [--guidance <text>] [--duration <seconds>]\n"
	renderUsage = "usage: ave render <plan.json> [--media-root <dir>] [--force]\n"
)

type outputMode int

const (
	outputNormal outputMode = iota
	outputVerbose
	outputQuiet
)

// Run executes ave with args and returns a process exit code. stdin supplies
// interactive prompt answers and interactive reports whether stdin is a
// terminal, so orientation ties can be resolved without blocking scripts.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool) int {
	args, mode, err := parseOutputMode(args)
	if err != nil {
		return writeExitCode(stderr, exitUsage, "%v\n", err)
	}

	if len(args) == 0 {
		return helpExitCode(stdout)
	}

	switch args[0] {
	case "-h", "--help", "help":
		return helpExitCode(stdout)
	case "edit":
		return runEdit(ctx, args[1:], mode, stdin, stdout, stderr, interactive)
	case "render":
		return runRender(ctx, args[1:], mode, stdout, stderr)
	case "doctor":
		if hasHelp(args[1:]) {
			return writeExitCode(stdout, exitSuccess, "usage: ave doctor [--verbose | --quiet]\n")
		}
		if len(args) != 1 {
			return writeExitCode(stderr, exitUsage, "usage: ave doctor [--verbose | --quiet]\n")
		}
		return runDoctor(ctx, mode, stdout, stderr)
	default:
		if _, err := fmt.Fprintf(stderr, "unknown command %q\n\n", args[0]); err != nil {
			return exitFailure
		}
		if err := printRootHelp(stderr); err != nil {
			return exitFailure
		}
		return exitUsage
	}
}

func runEdit(ctx context.Context, args []string, mode outputMode, stdin io.Reader, stdout, stderr io.Writer, interactive bool) int {
	if hasHelp(args) {
		return writeExitCode(stdout, exitSuccess, editUsage)
	}
	options, err := parseEditOptions(args)
	if err != nil {
		return writeExitCode(
			stderr,
			exitUsage,
			"%v\n%s",
			err,
			editUsage,
		)
	}
	options.Interactive = interactive
	options.Stdin = stdin
	options.Prompt = stderr

	result, err := edit.Run(ctx, options)
	if err != nil {
		return writeExitCode(stderr, exitFailure, "edit failed: %v\n", err)
	}
	if mode != outputQuiet {
		if code := writeExitCode(stdout, exitSuccess, "Edit Plan: %s\n", result.PlanPath); code != exitSuccess {
			return code
		}
		if result.VideoPath != "" {
			return writeExitCode(stdout, exitSuccess, "Finished Video: %s\n", result.VideoPath)
		}
	}
	return exitSuccess
}

func parseEditOptions(args []string) (edit.Options, error) {
	var options edit.Options
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--plan-only":
			options.PlanOnly = true
		case "--force":
			options.Force = true
		case "--output":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--output requires a file path")
			}
			options.Output = args[index]
		case "--fps":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--fps requires a frame rate")
			}
			fps, convErr := strconv.Atoi(args[index])
			if convErr != nil {
				return edit.Options{}, fmt.Errorf("invalid --fps value %q; use 24, 25, 30, or 60", args[index])
			}
			options.FrameRate = fps
		case "--aspect":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--aspect requires landscape or portrait")
			}
			options.Aspect = args[index]
		case "--quality-profile":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--quality-profile requires strict, balanced, or lenient")
			}
			options.QualityProfile = args[index]
		case "--shake":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--shake requires reject or stabilize")
			}
			options.ShakeTreatment = args[index]
		case "--model":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--model requires a vision model name")
			}
			options.Model = args[index]
		case "--guidance":
			index++
			if index >= len(args) {
				return edit.Options{}, fmt.Errorf("--guidance requires text")
			}
			options.Guidance = args[index]
		case "--duration":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("--duration requires a length in seconds")
			}
			seconds, convErr := strconv.ParseFloat(args[index], 64)
			if convErr != nil || seconds <= 0 {
				return edit.Options{}, fmt.Errorf("invalid --duration value %q; use a positive number of seconds", args[index])
			}
			options.Duration = seconds
		default:
			if strings.HasPrefix(args[index], "-") {
				return edit.Options{}, fmt.Errorf("unknown edit option %q", args[index])
			}
			if options.SourceDir != "" {
				return edit.Options{}, fmt.Errorf("edit accepts one source folder")
			}
			options.SourceDir = args[index]
		}
	}
	if options.SourceDir == "" {
		return edit.Options{}, fmt.Errorf("source folder is required")
	}
	return options, nil
}

func helpExitCode(writer io.Writer) int {
	if err := printRootHelp(writer); err != nil {
		return exitFailure
	}
	return exitSuccess
}

func writeExitCode(writer io.Writer, successCode int, format string, args ...any) int {
	if _, err := fmt.Fprintf(writer, format, args...); err != nil {
		return exitFailure
	}
	return successCode
}

func runRender(ctx context.Context, args []string, mode outputMode, stdout, stderr io.Writer) int {
	if hasHelp(args) {
		return writeExitCode(stdout, exitSuccess, renderUsage)
	}
	options, err := parseRenderOptions(args)
	if err != nil {
		return writeExitCode(stderr, exitUsage, "%v\n%s", err, renderUsage)
	}

	result, err := edit.RenderPlan(ctx, options)
	if err != nil {
		return writeExitCode(stderr, exitFailure, "render failed: %v\n", err)
	}
	if mode != outputQuiet {
		return writeExitCode(stdout, exitSuccess, "Finished Video: %s\n", result.VideoPath)
	}
	return exitSuccess
}

func parseRenderOptions(args []string) (edit.RenderOptions, error) {
	var options edit.RenderOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--force":
			options.Force = true
		case "--media-root":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "-") {
				return edit.RenderOptions{}, fmt.Errorf("--media-root requires a folder path")
			}
			options.MediaRoot = args[index]
		default:
			if strings.HasPrefix(args[index], "-") {
				return edit.RenderOptions{}, fmt.Errorf("unknown render option %q", args[index])
			}
			if options.PlanPath != "" {
				return edit.RenderOptions{}, fmt.Errorf("render accepts one Edit Plan")
			}
			options.PlanPath = args[index]
		}
	}
	if options.PlanPath == "" {
		return edit.RenderOptions{}, fmt.Errorf("missing Edit Plan path")
	}
	return options, nil
}

func parseOutputMode(args []string) ([]string, outputMode, error) {
	mode := outputNormal
	filtered := make([]string, 0, len(args))

	for _, arg := range args {
		switch arg {
		case "-v", "--verbose":
			if mode == outputQuiet {
				return nil, outputNormal, fmt.Errorf("--verbose and --quiet cannot be used together")
			}
			mode = outputVerbose
		case "-q", "--quiet":
			if mode == outputVerbose {
				return nil, outputNormal, fmt.Errorf("--verbose and --quiet cannot be used together")
			}
			mode = outputQuiet
		default:
			filtered = append(filtered, arg)
		}
	}

	return filtered, mode, nil
}

func hasHelp(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}

func printRootHelp(writer io.Writer) error {
	_, err := fmt.Fprintln(writer, `Usage: ave <command> [options]

Commands:
  edit <folder>       Analyze Source Clips and create a Finished Video
  render <plan.json>  Render a previously created Edit Plan
  doctor              Check local media and AI dependencies

Global options:
  -v, --verbose  Show dependency and subprocess details
  -q, --quiet    Show errors only
  -h, --help     Show help`)
	if err != nil {
		return fmt.Errorf("write help: %w", err)
	}
	return nil
}
