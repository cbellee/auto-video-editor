package ave_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLICommandContracts(t *testing.T) {
	binary := buildCLI(t)

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput []string
	}{
		{
			name:       "root help lists commands",
			args:       []string{"--help"},
			wantStatus: 0,
			wantOutput: []string{"Usage: ave", "edit", "render", "doctor"},
		},
		{
			name:       "no arguments shows root help",
			wantStatus: 0,
			wantOutput: []string{"Usage: ave", "doctor"},
		},
		{
			name:       "edit help shows source argument",
			args:       []string{"edit", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave edit <folder>"},
		},
		{
			name:       "render help shows plan argument",
			args:       []string{"render", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave render <plan.json>"},
		},
		{
			name:       "doctor help shows output modes",
			args:       []string{"doctor", "--help"},
			wantStatus: 0,
			wantOutput: []string{"usage: ave doctor", "--verbose", "--quiet"},
		},
		{
			name:       "edit requires source folder",
			args:       []string{"edit"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave edit <folder>"},
		},
		{
			name:       "render requires edit plan",
			args:       []string{"render"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave render <plan.json>"},
		},
		{
			name:       "doctor rejects positional arguments",
			args:       []string{"doctor", "unexpected"},
			wantStatus: 2,
			wantOutput: []string{"usage: ave doctor"},
		},
		{
			name:       "unknown command is rejected",
			args:       []string{"unknown"},
			wantStatus: 2,
			wantOutput: []string{"unknown command", "Usage: ave"},
		},
		{
			name:       "verbose and quiet conflict",
			args:       []string{"doctor", "--verbose", "--quiet"},
			wantStatus: 2,
			wantOutput: []string{"--verbose and --quiet cannot be used together"},
		},
		{
			name:       "quiet mode still reports command errors",
			args:       []string{"edit", "clips", "--quiet"},
			wantStatus: 1,
			wantOutput: []string{"edit is not implemented yet"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLI(t, binary, test.args...)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d\noutput:\n%s", status, test.wantStatus, output)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("output does not contain %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestDoctorReportsReadyDependencies(t *testing.T) {
	binary := buildCLI(t)
	toolDir := createFakeTools(t, true)
	server := newFakeLMStudio(t, true)

	baseEnv := append(
		os.Environ(),
		"PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AVE_LM_STUDIO_URL="+server.URL,
		"AVE_CONFIG_DIR="+filepath.Join(t.TempDir(), "config"),
		"AVE_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)

	tests := []struct {
		name       string
		args       []string
		wantOutput []string
		wantEmpty  bool
	}{
		{
			name: "normal output",
			args: []string{"doctor"},
			wantOutput: []string{
				"[ok] ffmpeg",
				"[ok] ffprobe",
				"[ok] LM Studio",
				"[ok] whisper.cpp",
				"[ok] aubio",
				"All required dependencies are ready.",
			},
		},
		{
			name: "verbose output",
			args: []string{"doctor", "--verbose"},
			wantOutput: []string{
				"vision model: local/vision-model",
				"config directory:",
				"cache directory:",
				toolDir,
			},
		},
		{
			name:      "quiet success",
			args:      []string{"doctor", "--quiet"},
			wantEmpty: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, output := runCLIWithEnv(t, binary, baseEnv, test.args...)
			if status != 0 {
				t.Fatalf("status = %d, want 0\noutput:\n%s", status, output)
			}
			if test.wantEmpty && output != "" {
				t.Fatalf("output = %q, want empty", output)
			}
			for _, want := range test.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("output does not contain %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestDoctorReportsActionableFailures(t *testing.T) {
	binary := buildCLI(t)

	t.Run("missing helper tools", func(t *testing.T) {
		toolDir := createFakeTools(t, false)
		server := newFakeLMStudio(t, true)
		env := doctorEnv(t, toolDir, server.URL)

		status, output := runCLIWithEnv(t, binary, env, "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		for _, want := range []string{
			"[missing] whisper.cpp: not found in PATH",
			"brew install whisper-cpp",
			"[missing] aubio: not found in PATH",
			"brew install aubio",
		} {
			if !strings.Contains(output, want) {
				t.Errorf("output does not contain %q:\n%s", want, output)
			}
		}
	})

	t.Run("quiet output contains failures only", func(t *testing.T) {
		toolDir := createFakeTools(t, false)
		server := newFakeLMStudio(t, true)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor", "--quiet")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if strings.Contains(output, "[ok]") || strings.Contains(output, "All required") {
			t.Errorf("quiet output includes successful checks:\n%s", output)
		}
		if !strings.Contains(output, "[missing] whisper.cpp") {
			t.Errorf("quiet output omits failure:\n%s", output)
		}
	})

	t.Run("ffmpeg lacks required capability", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		writeExecutable(t, toolDir, "ffmpeg", `#!/bin/sh
	case "$*" in
	  *"-filters"*)
	    echo "... scdet V->V"
	    echo "... blurdetect_opencl V->V"
	    echo "... xfade_opencl VV->V"
	    ;;
	  *"-encoders"*)
	    echo "V....D h264_videotoolbox_extra"
	    echo "A....D aac_at"
	    ;;
	  *) echo "ffmpeg version 7.1.1" ;;
	esac
	`)
		server := newFakeLMStudio(t, true)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] ffmpeg: missing required capabilities") {
			t.Errorf("output does not identify incompatible ffmpeg:\n%s", output)
		}
		if !strings.Contains(output, "filter blurdetect") {
			t.Errorf("output does not identify missing filter:\n%s", output)
		}
	})

	t.Run("LM Studio has no vision model", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := newFakeLMStudio(t, false)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: no compatible vision model installed") {
			t.Errorf("output does not identify missing vision model:\n%s", output)
		}
	})

	t.Run("remote model endpoint is rejected", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		env := doctorEnv(t, toolDir, "https://models.example.com")

		status, output := runCLIWithEnv(t, binary, env, "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: server URL is not loopback") {
			t.Errorf("output does not reject remote endpoint:\n%s", output)
		}
	})

	t.Run("LM Studio returns invalid JSON", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			if _, err := writer.Write([]byte("not-json")); err != nil {
				t.Errorf("write invalid response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: model API returned invalid JSON") {
			t.Errorf("output does not identify invalid model response:\n%s", output)
		}
	})

	t.Run("LM Studio returns HTTP error", func(t *testing.T) {
		toolDir := createFakeTools(t, true)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)

		status, output := runCLIWithEnv(t, binary, doctorEnv(t, toolDir, server.URL), "doctor")
		if status != 1 {
			t.Fatalf("status = %d, want 1\noutput:\n%s", status, output)
		}
		if !strings.Contains(output, "[missing] LM Studio: model API returned HTTP 503") {
			t.Errorf("output does not identify model server error:\n%s", output)
		}
	})
}

func buildCLI(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "ave")
	args := []string{"build"}
	if os.Getenv("AVE_COVER_DIR") != "" {
		args = append(args, "-race", "-cover")
	}
	args = append(args, "-o", binary, "./cmd/ave")
	command := exec.Command("go", args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output.String())
	}
	return binary
}

func runCLI(t *testing.T, binary string, args ...string) (int, string) {
	t.Helper()
	return runCLIWithEnv(t, binary, os.Environ(), args...)
}

func runCLIWithEnv(t *testing.T, binary string, env []string, args ...string) (int, string) {
	t.Helper()

	command := exec.Command(binary, args...)
	if coverageDir := os.Getenv("AVE_COVER_DIR"); coverageDir != "" {
		env = append(env, "GOCOVERDIR="+coverageDir)
	}
	command.Env = env
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err == nil {
		return 0, output.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run CLI: %v", err)
	}
	return exitErr.ExitCode(), output.String()
}

func createFakeTools(t *testing.T, includeOptional bool) string {
	t.Helper()

	dir := t.TempDir()
	writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$*" in
  *"-filters"*)
    echo "... blurdetect V->V"
    echo "... scdet V->V"
    echo "... vidstabdetect V->V"
    echo "... vidstabtransform V->V"
    echo "... silencedetect A->A"
    echo "... ebur128 A->N"
    echo "... xfade VV->V"
    echo "... acrossfade AA->A"
    ;;
  *"-encoders"*)
    echo "V....D h264_videotoolbox"
    echo "V....D libx264"
    echo "A....D aac"
    ;;
  *)
    echo "ffmpeg version 7.1.1"
    ;;
esac
`)
	writeExecutable(t, dir, "ffprobe", "#!/bin/sh\necho 'ffprobe version 7.1.1'\n")
	if includeOptional {
		writeExecutable(t, dir, "whisper-cli", "#!/bin/sh\necho 'whisper.cpp 1.7.0'\n")
		writeExecutable(t, dir, "aubio", "#!/bin/sh\necho 'aubio 0.4.9'\n")
	}
	return dir
}

func doctorEnv(t *testing.T, toolDir, serverURL string) []string {
	t.Helper()

	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") ||
			strings.HasPrefix(entry, "AVE_LM_STUDIO_URL=") ||
			strings.HasPrefix(entry, "AVE_CONFIG_DIR=") ||
			strings.HasPrefix(entry, "AVE_CACHE_DIR=") {
			continue
		}
		env = append(env, entry)
	}
	return append(
		env,
		"PATH="+toolDir,
		"AVE_LM_STUDIO_URL="+serverURL,
		"AVE_CONFIG_DIR="+filepath.Join(t.TempDir(), "config"),
		"AVE_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func newFakeLMStudio(t *testing.T, withVisionModel bool) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/models" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		models := `[]`
		if withVisionModel {
			models = `[{"type":"llm","key":"local/vision-model","capabilities":{"vision":true}}]`
		}
		if _, err := fmt.Fprintf(writer, `{"models":%s}`, models); err != nil {
			t.Errorf("write model response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
