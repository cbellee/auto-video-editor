package edit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubLookPath replaces the package lookPath for a test, reporting every binary
// in present as found and anything else as missing. It restores the original on
// cleanup.
func stubLookPath(t *testing.T, present ...string) {
	t.Helper()
	found := make(map[string]bool, len(present))
	for _, name := range present {
		found[name] = true
	}
	prev := lookPath
	lookPath = func(name string) (string, error) {
		if found[name] {
			return "/fake/bin/" + name, nil
		}
		return "", errors.New("not found: " + name)
	}
	t.Cleanup(func() { lookPath = prev })
}

// seedWhisperModelEnv points AVE_WHISPER_MODEL at a real file so whisper model
// resolution succeeds inside preflight.
func seedWhisperModelEnv(t *testing.T) {
	t.Helper()
	model := filepath.Join(t.TempDir(), "ggml-base.en.bin")
	if err := os.WriteFile(model, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	t.Setenv(whisperModelEnv, model)
}

func TestPreflightPassesWhenAllDependenciesPresent(t *testing.T) {
	stubLookPath(t, "ffmpeg", "ffprobe", "whisper-cli", "aubio")
	seedWhisperModelEnv(t)

	err := preflight(context.Background(), fakeLister{models: []string{"vision"}}, Options{Music: "/tmp/song.mp3"})
	if err != nil {
		t.Fatalf("preflight: unexpected error: %v", err)
	}
}

func TestPreflightReportsEachMissingDependency(t *testing.T) {
	tests := []struct {
		name       string
		binaries   []string
		models     []string
		listErr    error
		seedModel  bool
		music      string
		wantSubstr string
	}{
		{
			name:       "missing ffmpeg",
			binaries:   []string{"ffprobe", "whisper-cli", "aubio"},
			models:     []string{"vision"},
			seedModel:  true,
			wantSubstr: "ffmpeg not found",
		},
		{
			name:       "missing ffprobe",
			binaries:   []string{"ffmpeg", "whisper-cli", "aubio"},
			models:     []string{"vision"},
			seedModel:  true,
			wantSubstr: "ffprobe not found",
		},
		{
			name:       "missing whisper binary",
			binaries:   []string{"ffmpeg", "ffprobe", "aubio"},
			models:     []string{"vision"},
			seedModel:  true,
			wantSubstr: "whisper.cpp not found",
		},
		{
			name:       "missing whisper model",
			binaries:   []string{"ffmpeg", "ffprobe", "whisper-cli", "aubio"},
			models:     []string{"vision"},
			seedModel:  false,
			wantSubstr: whisperModelEnv,
		},
		{
			name:       "missing aubio only when music supplied",
			binaries:   []string{"ffmpeg", "ffprobe", "whisper-cli"},
			models:     []string{"vision"},
			seedModel:  true,
			music:      "/tmp/song.mp3",
			wantSubstr: "aubio not found",
		},
		{
			name:       "lm studio unreachable",
			binaries:   []string{"ffmpeg", "ffprobe", "whisper-cli", "aubio"},
			listErr:    errors.New("connection refused"),
			seedModel:  true,
			wantSubstr: "LM Studio unavailable",
		},
		{
			name:       "no vision model loaded",
			binaries:   []string{"ffmpeg", "ffprobe", "whisper-cli", "aubio"},
			models:     nil,
			seedModel:  true,
			wantSubstr: "no compatible vision model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubLookPath(t, tc.binaries...)
			if tc.seedModel {
				seedWhisperModelEnv(t)
			} else {
				// Force whisper model resolution to fail deterministically by
				// pointing the env override at a non-existent file.
				t.Setenv(whisperModelEnv, filepath.Join(t.TempDir(), "missing.bin"))
			}

			err := preflight(context.Background(),
				fakeLister{models: tc.models, err: tc.listErr},
				Options{Music: tc.music})
			if err == nil {
				t.Fatalf("preflight: expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("preflight error = %q, want substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestPreflightSkipsAubioWithoutMusic(t *testing.T) {
	// aubio is intentionally absent; preflight must still pass when no Music
	// Track is supplied.
	stubLookPath(t, "ffmpeg", "ffprobe", "whisper-cli")
	seedWhisperModelEnv(t)

	err := preflight(context.Background(), fakeLister{models: []string{"vision"}}, Options{})
	if err != nil {
		t.Fatalf("preflight: unexpected error without music: %v", err)
	}
}
