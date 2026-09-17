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

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
