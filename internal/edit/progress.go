package edit

import (
	"fmt"
	"io"
)

// progressReporter surfaces a run's progress at a verbosity the user chose.
// Stage lines mark the major phases of an edit and appear in normal and verbose
// modes; Detail lines carry subprocess-level specifics and appear only in
// verbose mode. Quiet mode shows neither, leaving only errors and the final
// result. The three modes report the same phases so output stays consistent
// across a full edit regardless of verbosity.
type progressReporter struct {
	writer  io.Writer
	verbose bool
	quiet   bool
}

// newProgressReporter builds a reporter writing to w. A nil writer discards all
// progress, which keeps non-interactive callers and tests simple.
func newProgressReporter(w io.Writer, verbose, quiet bool) progressReporter {
	if w == nil {
		w = io.Discard
	}
	return progressReporter{writer: w, verbose: verbose, quiet: quiet}
}

// Stage reports a major phase of the edit. It is suppressed in quiet mode.
func (r progressReporter) Stage(format string, args ...any) {
	if r.quiet {
		return
	}
	_, _ = fmt.Fprintf(r.writer, "• "+format+"\n", args...)
}

// Detailf reports subprocess-level detail shown only in verbose mode.
func (r progressReporter) Detailf(format string, args ...any) {
	if !r.verbose {
		return
	}
	_, _ = fmt.Fprintf(r.writer, "  - "+format+"\n", args...)
}
