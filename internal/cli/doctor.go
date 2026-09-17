package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/cbellee/auto-video-editor/internal/doctor"
)

func runDoctor(ctx context.Context, mode outputMode, stdout, stderr io.Writer) int {
	report := doctor.Check(ctx)

	if mode == outputVerbose {
		fmt.Fprintf(stdout, "config directory: %s\n", report.ConfigDir)
		fmt.Fprintf(stdout, "cache directory: %s\n", report.CacheDir)
	}

	for _, result := range report.Results {
		if mode == outputQuiet && result.Ready {
			continue
		}

		writer := stdout
		if !result.Ready {
			writer = stderr
		}
		state := "ok"
		if !result.Ready {
			state = "missing"
		}
		fmt.Fprintf(writer, "[%s] %s: %s\n", state, result.Name, result.Summary)
		if mode == outputVerbose && result.Detail != "" {
			fmt.Fprintf(writer, "  %s\n", result.Detail)
		}
		if !result.Ready && result.Remedy != "" {
			fmt.Fprintf(writer, "  Fix: %s\n", result.Remedy)
		}
	}

	if !report.Ready() {
		if mode != outputQuiet {
			fmt.Fprintln(stderr, "Doctor found missing or incompatible dependencies.")
		}
		return exitFailure
	}

	if mode != outputQuiet {
		fmt.Fprintln(stdout, "All required dependencies are ready.")
	}
	return exitSuccess
}
