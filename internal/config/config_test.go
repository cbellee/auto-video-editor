package config

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsZero(t *testing.T) {
	t.Setenv(dirEnv, t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	if cfg.LastModel != "" {
		t.Errorf("expected zero config, got %+v", cfg)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	if err := Save(Config{LastModel: "qwen2-vl-7b"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LastModel != "qwen2-vl-7b" {
		t.Errorf("LastModel = %q, want qwen2-vl-7b", cfg.LastModel)
	}
}

func TestDirHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != dir {
		t.Errorf("Dir = %q, want %q", got, dir)
	}
}

func TestSaveCreatesNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "ave")
	t.Setenv(dirEnv, dir)
	if err := Save(Config{LastModel: "m"}); err != nil {
		t.Fatalf("Save into nested dir: %v", err)
	}
	cfg, err := Load()
	if err != nil || cfg.LastModel != "m" {
		t.Fatalf("round trip through nested dir failed: %+v err=%v", cfg, err)
	}
}
