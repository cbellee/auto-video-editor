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
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (exitCode int) {
	var writeErr error
	stdout = &trackingWriter{writer: stdout, firstErr: &writeErr}
	stderr = &trackingWriter{writer: stderr, firstErr: &writeErr}
	defer func() {
		if writeErr != nil {
			exitCode = exitFailure
		}
	}()

	args, mode, err := parseOutputMode(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	if len(args) == 0 {
		printRootHelp(stdout)
		return exitSuccess
	}

	switch args[0] {
	case "-h", "--help", "help":
		printRootHelp(stdout)
		return exitSuccess
	case "edit":
		return runPlaceholder(ctx, "edit", "<folder>", args[1:], stdout, stderr)
	case "render":
		return runPlaceholder(ctx, "render", "<plan.json>", args[1:], stdout, stderr)
	case "doctor":
		if hasHelp(args[1:]) {
			fmt.Fprintln(stdout, "usage: ave doctor [--verbose | --quiet]")
			return exitSuccess
		}
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: ave doctor [--verbose | --quiet]")
			return exitUsage
		}
		return runDoctor(ctx, mode, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printRootHelp(stderr)
		return exitUsage
	}
}

type trackingWriter struct {
	writer   io.Writer
	firstErr *error
}

func (writer *trackingWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	if err != nil && *writer.firstErr == nil {
		*writer.firstErr = err
	}
	return written, err
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
		fmt.Fprintf(stdout, "usage: ave %s %s [options]\n", command, argument)
		return exitSuccess
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "usage: ave %s %s [options]\n", command, argument)
		return exitUsage
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "%s cancelled: %v\n", command, err)
		return exitFailure
	}
	fmt.Fprintf(stderr, "%s is not implemented yet\n", command)
	return exitFailure
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

func printRootHelp(writer io.Writer) {
	fmt.Fprintln(writer, `Usage: ave <command> [options]

Commands:
  edit <folder>       Analyze Source Clips and create a Finished Video
  render <plan.json>  Render a previously created Edit Plan
  doctor              Check local media and AI dependencies

Global options:
  -v, --verbose  Show dependency and subprocess details
  -q, --quiet    Show errors only
  -h, --help     Show help`)
}
