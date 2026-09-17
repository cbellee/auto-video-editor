// Package cli implements the public ave command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
)

type outputMode int

const (
	outputNormal outputMode = iota
	outputVerbose
	outputQuiet
)

// Run executes ave with args and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
		return runPlaceholder(ctx, "edit", "<folder>", args[1:], stdout, stderr)
	case "render":
		return runPlaceholder(ctx, "render", "<plan.json>", args[1:], stdout, stderr)
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

func runPlaceholder(
	ctx context.Context,
	command string,
	argument string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if hasHelp(args) {
		return writeExitCode(stdout, exitSuccess, "usage: ave %s %s [options]\n", command, argument)
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return writeExitCode(stderr, exitUsage, "usage: ave %s %s [options]\n", command, argument)
	}
	if err := ctx.Err(); err != nil {
		return writeExitCode(stderr, exitFailure, "%s cancelled: %v\n", command, err)
	}
	return writeExitCode(stderr, exitFailure, "%s is not implemented yet\n", command)
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
