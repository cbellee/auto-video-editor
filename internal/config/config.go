// Package config persists routine, non-creative operational preferences for
// ave in the macOS user configuration location. Only settings that should
// survive between runs live here; per-run creative inputs never do.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config holds operational defaults remembered between runs.
type Config struct {
	// LastModel is the vision model key that most recently produced a
	// successful ranking, remembered to reduce interactive prompting.
	LastModel string `json:"last_model,omitempty"`
}

// dirEnv overrides the configuration directory, primarily for tests.
const dirEnv = "AVE_CONFIG_DIR"

const (
	appDir   = "ave"
	fileName = "config.json"
)

// Dir returns the directory where ave stores its configuration, honoring the
// AVE_CONFIG_DIR override before falling back to the OS user config location.
func Dir() (string, error) {
	if override := os.Getenv(dirEnv); override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, appDir), nil
}

// Load reads the persisted configuration, returning a zero Config when none has
// been written yet.
func Load() (Config, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes the configuration, creating the configuration directory when
// necessary. A failure to persist is returned but is never fatal to a run.
func Save(cfg Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
