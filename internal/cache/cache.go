// Package cache persists reusable analysis work for ave in the macOS user
// cache location so a long editing run can resume cheaply after cancellation
// without redoing valid media probes, Technical Quality scoring, transcripts,
// or Music Cue detection. Only deterministic, input-keyed analysis is cached;
// creative choices never are, so a cache hit can never leak one run's intent,
// theme, guidance, music, or duration into a later run.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirEnv overrides the cache directory, primarily for tests. disableEnv turns
// the cache off entirely so a run can be forced to recompute every result.
const (
	dirEnv     = "AVE_CACHE_DIR"
	disableEnv = "AVE_NO_CACHE"
	appDir     = "ave"
)

// Enabled reports whether caching is active. A run disables the cache by
// setting AVE_NO_CACHE to a non-empty value, forcing every analysis to recompute.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(disableEnv)) == ""
}

// Dir returns the directory where ave stores cached analysis, honoring the
// AVE_CACHE_DIR override before falling back to the OS user cache location.
func Dir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(dirEnv)); override != "" {
		return override, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	return filepath.Join(base, appDir), nil
}

// Key derives a stable cache key for a category of analysis from a source
// fingerprint and every setting that can change the result. The settings are
// sorted so key construction is order-independent, and the whole tuple is
// hashed so an unrelated setting change can never collide with a prior result.
func Key(category, fingerprint string, settings map[string]string) string {
	pairs := make([]string, 0, len(settings))
	for name, value := range settings {
		pairs = append(pairs, name+"="+value)
	}
	sort.Strings(pairs)
	payload := strings.Join(append([]string{category, fingerprint}, pairs...), "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return category + "-" + hex.EncodeToString(sum[:])
}

// Load reads a cached value of type T for key, reporting whether a valid entry
// was found. A disabled cache, a missing entry, or an unreadable/corrupt entry
// all report a miss without error so analysis simply recomputes; a corrupt
// entry is removed so it is rewritten cleanly.
func Load[T any](key string) (T, bool) {
	var value T
	if !Enabled() {
		return value, false
	}
	dir, err := Dir()
	if err != nil {
		return value, false
	}
	path := filepath.Join(dir, key+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return value, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		_ = os.Remove(path)
		var zero T
		return zero, false
	}
	return value, true
}

// Store writes value under key so a later run can reuse it. The write is
// atomic (temp file then rename) so a cancellation mid-write never leaves a
// half-written entry a later run would read. A disabled cache is a no-op. A
// failure to persist is returned but is never fatal to a run.
func Store[T any](key string, value T) error {
	if !Enabled() {
		return nil
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".ave-cache-*")
	if err != nil {
		return fmt.Errorf("create cache entry: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write cache entry: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("flush cache entry: %w", err)
	}
	final := filepath.Join(dir, key+".json")
	if err := os.Rename(tempPath, final); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("commit cache entry: %w", err)
	}
	return nil
}
