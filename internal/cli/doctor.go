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
		if _, err := fmt.Fprintf(stdout, "config directory: %s\n", report.ConfigDir); err != nil {
			return exitFailure
		}
		if _, err := fmt.Fprintf(stdout, "cache directory: %s\n", report.CacheDir); err != nil {
			return exitFailure
		}
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
		if _, err := fmt.Fprintf(writer, "[%s] %s: %s\n", state, result.Name, result.Summary); err != nil {
			return exitFailure
		}
		if mode == outputVerbose && result.Detail != "" {
			if _, err := fmt.Fprintf(writer, "  %s\n", result.Detail); err != nil {
				return exitFailure
			}
		}
		if !result.Ready && result.Remedy != "" {
			if _, err := fmt.Fprintf(writer, "  Fix: %s\n", result.Remedy); err != nil {
				return exitFailure
			}
		}
	}

	if !report.Ready() {
		if mode != outputQuiet {
			if _, err := fmt.Fprintln(stderr, "Doctor found missing or incompatible dependencies."); err != nil {
				return exitFailure
			}
		}
		return exitFailure
	}

	if mode != outputQuiet {
		if _, err := fmt.Fprintln(stdout, "All required dependencies are ready."); err != nil {
			return exitFailure
		}
	}
	return exitSuccess
}
