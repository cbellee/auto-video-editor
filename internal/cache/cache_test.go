package cache

import (
	"os"
	"path/filepath"
	"testing"
)

type sample struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

func TestStoreAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	t.Setenv(disableEnv, "")

	key := Key("quality", "v1:10:abc", map[string]string{"profile": "balanced", "shake": "reject"})
	want := sample{Name: "clip", Score: 0.75}
	if err := Store(key, want); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, ok := Load[sample](key)
	if !ok {
		t.Fatal("expected a cache hit after storing")
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestLoadMissReturnsFalse(t *testing.T) {
	t.Setenv(dirEnv, t.TempDir())
	t.Setenv(disableEnv, "")
	if _, ok := Load[sample]("quality-missing"); ok {
		t.Error("expected a miss for an unknown key")
	}
}

func TestKeyDependsOnFingerprintAndSettings(t *testing.T) {
	base := Key("quality", "v1:10:abc", map[string]string{"profile": "balanced"})
	otherFingerprint := Key("quality", "v1:10:xyz", map[string]string{"profile": "balanced"})
	otherSetting := Key("quality", "v1:10:abc", map[string]string{"profile": "strict"})
	otherCategory := Key("transcript", "v1:10:abc", map[string]string{"profile": "balanced"})

	if base == otherFingerprint {
		t.Error("key must change when the source fingerprint changes")
	}
	if base == otherSetting {
		t.Error("key must change when an analysis setting changes")
	}
	if base == otherCategory {
		t.Error("key must change when the analysis category changes")
	}
}

func TestKeyIsOrderIndependent(t *testing.T) {
	a := Key("quality", "fp", map[string]string{"profile": "balanced", "shake": "reject"})
	b := Key("quality", "fp", map[string]string{"shake": "reject", "profile": "balanced"})
	if a != b {
		t.Errorf("key must not depend on settings map order: %q vs %q", a, b)
	}
}

func TestDisabledCacheNeverStoresOrHits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	t.Setenv(disableEnv, "1")

	key := Key("quality", "fp", nil)
	if err := Store(key, sample{Name: "x"}); err != nil {
		t.Fatalf("store on disabled cache should be a no-op, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("disabled cache wrote %d entries, want 0", len(entries))
	}
	if _, ok := Load[sample](key); ok {
		t.Error("disabled cache must always miss")
	}
}

func TestCorruptEntryIsTreatedAsMissAndRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	t.Setenv(disableEnv, "")

	key := Key("quality", "fp", nil)
	path := filepath.Join(dir, key+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt entry: %v", err)
	}
	if _, ok := Load[sample](key); ok {
		t.Error("corrupt entry must be treated as a miss")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("corrupt entry should have been removed, stat err = %v", err)
	}
}

func TestStoreIsAtomic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	t.Setenv(disableEnv, "")

	key := Key("quality", "fp", nil)
	if err := Store(key, sample{Name: "clip", Score: 1}); err != nil {
		t.Fatalf("store: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			t.Errorf("store left a non-committed artifact behind: %s", entry.Name())
		}
	}
}
