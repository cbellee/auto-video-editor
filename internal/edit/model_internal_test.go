package edit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbellee/auto-video-editor/internal/config"
)

type fakeLister struct {
	models []string
	err    error
}

func (f fakeLister) ListVisionModels(_ context.Context) ([]string, error) {
	return f.models, f.err
}

func TestResolveModelFlagWins(t *testing.T) {
	t.Setenv("AVE_CONFIG_DIR", t.TempDir())
	model, err := resolveModel(context.Background(), fakeLister{}, Options{Model: "flag-model"})
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if model != "flag-model" {
		t.Errorf("model = %q, want flag-model", model)
	}
}

func TestResolveModelUsesRemembered(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AVE_CONFIG_DIR", dir)
	if err := config.Save(config.Config{LastModel: "remembered-model"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	model, err := resolveModel(context.Background(), fakeLister{}, Options{})
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if model != "remembered-model" {
		t.Errorf("model = %q, want remembered-model", model)
	}
}

func TestResolveModelNonInteractiveRequiresFlag(t *testing.T) {
	t.Setenv("AVE_CONFIG_DIR", t.TempDir())
	_, err := resolveModel(context.Background(), fakeLister{models: []string{"m"}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("expected a --model requirement error, got %v", err)
	}
}

func TestResolveModelInteractiveSelects(t *testing.T) {
	t.Setenv("AVE_CONFIG_DIR", t.TempDir())
	options := Options{
		Interactive: true,
		Stdin:       strings.NewReader("2\n"),
		Prompt:      &strings.Builder{},
	}
	model, err := resolveModel(context.Background(),
		fakeLister{models: []string{"first", "second"}}, options)
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if model != "second" {
		t.Errorf("model = %q, want second", model)
	}
}

func TestResolveModelInteractiveNoModelsFails(t *testing.T) {
	t.Setenv("AVE_CONFIG_DIR", t.TempDir())
	options := Options{
		Interactive: true,
		Stdin:       strings.NewReader("1\n"),
		Prompt:      &strings.Builder{},
	}
	_, err := resolveModel(context.Background(), fakeLister{models: nil}, options)
	if err == nil || !strings.Contains(err.Error(), "no compatible vision models") {
		t.Fatalf("expected a no-models error, got %v", err)
	}
}

func TestRememberModelRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AVE_CONFIG_DIR", dir)
	rememberModel("kept-model")
	if got := rememberedModel(); got != "kept-model" {
		t.Errorf("rememberedModel = %q, want kept-model", got)
	}
	if _, err := filepath.Abs(dir); err != nil {
		t.Fatal(err)
	}
}
