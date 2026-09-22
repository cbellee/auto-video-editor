package edit

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cbellee/auto-video-editor/internal/config"
	"github.com/cbellee/auto-video-editor/internal/lmstudio"
)

// modelLister is the subset of the LM Studio client used to discover models.
type modelLister interface {
	ListVisionModels(ctx context.Context) ([]string, error)
}

// resolveModel decides which vision model ranks the footage. An explicit
// --model flag always wins. Otherwise the remembered last-successful model is
// used. With neither, an interactive session prompts from discovered models,
// while a non-interactive run fails rather than guessing, keeping scripted runs
// deterministic.
func resolveModel(ctx context.Context, lister modelLister, options Options) (string, error) {
	if strings.TrimSpace(options.Model) != "" {
		return strings.TrimSpace(options.Model), nil
	}
	if remembered := rememberedModel(); remembered != "" {
		return remembered, nil
	}
	if !options.Interactive || options.Prompt == nil || options.Stdin == nil {
		return "", fmt.Errorf("no vision model selected; pass --model <name> " +
			"(no remembered model and this run is not interactive)")
	}
	return promptForModel(ctx, lister, options)
}

// promptForModel lists discoverable vision models and asks the user to choose.
func promptForModel(ctx context.Context, lister modelLister, options Options) (string, error) {
	models, err := lister.ListVisionModels(ctx)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", fmt.Errorf("no compatible vision models are loaded in LM Studio")
	}
	_, _ = fmt.Fprintln(options.Prompt, "Available vision models:")
	for index, name := range models {
		_, _ = fmt.Fprintf(options.Prompt, "  %d) %s\n", index+1, name)
	}
	reader := bufio.NewReader(options.Stdin)
	for {
		_, _ = fmt.Fprintf(options.Prompt, "Choose a model [1-%d]: ", len(models))
		line, readErr := reader.ReadString('\n')
		choice, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && choice >= 1 && choice <= len(models) {
			return models[choice-1], nil
		}
		if readErr != nil {
			return "", fmt.Errorf("no model selected")
		}
	}
}

// rememberedModel returns the last-successful vision model, or empty if none is
// stored or the store cannot be read.
func rememberedModel() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return cfg.LastModel
}

// rememberModel persists model as the last-successful choice, ignoring storage
// errors so a successful edit is never failed by a config write problem.
func rememberModel(model string) {
	_ = config.Save(config.Config{LastModel: model})
}

// scoreFrom projects a ranked candidate's model assessment into the plan's
// provenance shape.
func scoreFrom(candidate rankedCandidate) SegmentScore {
	return SegmentScore{
		VisualInterest: candidate.score.VisualInterest,
		Energy:         candidate.score.Energy,
		Usefulness:     candidate.score.Usefulness,
		Redundancy:     candidate.score.Redundancy,
		Subjects:       candidate.score.Subjects,
		Actions:        candidate.score.Actions,
		Base:           candidate.base,
	}
}

var _ modelLister = (*lmstudio.Client)(nil)
