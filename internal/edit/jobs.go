package edit

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"
)

// clipAnalysis pairs a Source Clip with its content fingerprint and the
// Technical Quality analysis of its Candidate Segments.
type clipAnalysis struct {
	clip        sourceClip
	fingerprint string
	candidates  []analyzedCandidate
}

// maxAutomaticJobs caps automatic parallelism so an edit stays responsive on the
// user's Mac instead of saturating every core with ffmpeg workers.
const maxAutomaticJobs = 4

// resolveJobs turns the requested --jobs value into a bounded worker count. A
// non-positive request selects an automatic bound from the CPU count, capped so
// background analysis never starves the interactive machine. An explicit
// request is honored but still floored at one and capped at the CPU count so a
// user cannot oversubscribe the machine.
func resolveJobs(requested int) int {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 1
	}
	if requested <= 0 {
		automatic := cpus
		if automatic > maxAutomaticJobs {
			automatic = maxAutomaticJobs
		}
		return automatic
	}
	if requested > cpus {
		return cpus
	}
	return requested
}

// analyzeClipsParallel fingerprints and analyzes every Source Clip with bounded
// parallelism, reusing cached analysis where valid. Results are returned in the
// original clip order so the Edit Plan stays deterministic regardless of how the
// workers interleave. The first failure cancels the remaining work and is
// returned; a cancellation (Ctrl-C) propagates through the shared context so the
// child ffmpeg/ffprobe processes are terminated.
func analyzeClipsParallel(
	ctx context.Context,
	clips []sourceClip,
	cfg analysisConfig,
	jobs int,
	report progressReporter,
) ([]clipAnalysis, error) {
	results := make([]clipAnalysis, len(clips))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(jobs)

	for index, clip := range clips {
		index, clip := index, clip
		group.Go(func() error {
			fingerprint, err := sourceFingerprint(clip.path)
			if err != nil {
				return err
			}
			candidates, err := analyzeClipCached(groupCtx, clip.path, clip, cfg, fingerprint, report)
			if err != nil {
				return err
			}
			results[index] = clipAnalysis{clip: clip, fingerprint: fingerprint, candidates: candidates}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}
