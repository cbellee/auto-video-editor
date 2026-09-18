// Command ave provides local automatic video editing workflows.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/cbellee/auto-video-editor/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, isTerminal(os.Stdin)))
}

// isTerminal reports whether the file is an interactive terminal, so ave only
// prompts to resolve orientation ties when a user can answer.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
