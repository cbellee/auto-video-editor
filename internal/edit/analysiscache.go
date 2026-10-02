package edit

import (
	"context"

	"github.com/cbellee/auto-video-editor/internal/cache"
)

// cachedCandidate is the serializable projection of an analyzedCandidate written
// to the analysis cache. analyzedCandidate itself carries unexported fields, so
// the cache stores this exported mirror and reconstructs the internal form on a
// hit.
type cachedCandidate struct {
	Start              float64        `json:"start"`
	End                float64        `json:"end"`
	Metrics            SegmentMetrics `json:"metrics"`
	Eligible           bool           `json:"eligible"`
	Shaky              bool           `json:"shaky"`
	NeedsStabilization bool           `json:"needs_stabilization"`
	HardFailure        bool           `json:"hard_failure"`
	Reasons            []string       `json:"reasons"`
}

// analysisCacheKey derives the cache key for one Source Clip's Technical Quality
// analysis. It folds in the source fingerprint and every setting that changes
// the result — the quality profile (which sets every gate threshold) and the
// shake treatment (which decides whether shaky footage is rejected or retained)
// — so a cache hit can never reuse analysis computed under different settings.
func analysisCacheKey(fingerprint string, cfg analysisConfig) string {
	return cache.Key("analysis", fingerprint, map[string]string{
		"profile": string(cfg.profile),
		"shake":   string(cfg.shake),
	})
}

// analyzeClipCached returns the Technical Quality analysis for one Source Clip,
// reusing a valid cached result when the clip content and analysis settings are
// unchanged and otherwise computing it and caching the outcome. Caching this
// stage is what lets a cancelled run resume without re-probing, re-sampling, and
// re-scoring footage it already analyzed.
func analyzeClipCached(
	ctx context.Context,
	path string,
	clip sourceClip,
	cfg analysisConfig,
	fingerprint string,
	report progressReporter,
) ([]analyzedCandidate, error) {
	key := analysisCacheKey(fingerprint, cfg)
	if stored, ok := cache.Load[[]cachedCandidate](key); ok {
		report.Detailf("reusing cached analysis for %s", path)
		return fromCachedCandidates(stored), nil
	}
	candidates, err := analyzeClip(ctx, path, clip, cfg)
	if err != nil {
		return nil, err
	}
	if err := cache.Store(key, toCachedCandidates(candidates)); err != nil {
		// A cache write failure must never fail the run; the analysis already
		// succeeded and the result is returned regardless.
		report.Detailf("could not cache analysis for %s: %v", path, err)
	}
	return candidates, nil
}

func toCachedCandidates(candidates []analyzedCandidate) []cachedCandidate {
	stored := make([]cachedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		stored = append(stored, cachedCandidate{
			Start:              candidate.rng.start,
			End:                candidate.rng.end,
			Metrics:            candidate.metrics,
			Eligible:           candidate.gate.eligible,
			Shaky:              candidate.gate.shaky,
			NeedsStabilization: candidate.gate.needsStabilization,
			HardFailure:        candidate.gate.hardFailure,
			Reasons:            candidate.gate.reasons,
		})
	}
	return stored
}

func fromCachedCandidates(stored []cachedCandidate) []analyzedCandidate {
	candidates := make([]analyzedCandidate, 0, len(stored))
	for _, entry := range stored {
		candidates = append(candidates, analyzedCandidate{
			rng:     candidateRange{start: entry.Start, end: entry.End},
			metrics: entry.Metrics,
			gate: gateResult{
				eligible:           entry.Eligible,
				shaky:              entry.Shaky,
				needsStabilization: entry.NeedsStabilization,
				hardFailure:        entry.HardFailure,
				reasons:            entry.Reasons,
			},
		})
	}
	return candidates
}
