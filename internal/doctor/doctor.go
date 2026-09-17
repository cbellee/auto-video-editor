// Package doctor inspects the local media and AI toolchain required by ave.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultLMStudioURL = "http://127.0.0.1:1234"

// Result describes one dependency check.
type Result struct {
	Name    string
	Ready   bool
	Summary string
	Detail  string
	Remedy  string
}

// Report contains dependency results and ave's platform directories.
type Report struct {
	ConfigDir string
	CacheDir  string
	Results   []Result
}

// Ready reports whether every required dependency passed its check.
func (report Report) Ready() bool {
	for _, result := range report.Results {
		if !result.Ready {
			return false
		}
	}
	return true
}

// Check inspects all required local dependencies.
func Check(ctx context.Context) Report {
	return Report{
		ConfigDir: platformDir("AVE_CONFIG_DIR", os.UserConfigDir),
		CacheDir:  platformDir("AVE_CACHE_DIR", os.UserCacheDir),
		Results: []Result{
			checkFFmpeg(ctx),
			checkCommand(ctx, "ffprobe", []string{"ffprobe"}, []string{"-version"}, "brew install ffmpeg"),
			checkLMStudio(ctx),
			checkCommand(
				ctx,
				"whisper.cpp",
				[]string{"whisper-cli", "whisper-cpp"},
				[]string{"--help"},
				"brew install whisper-cpp and download a local Whisper model",
			),
			checkCommand(ctx, "aubio", []string{"aubio", "aubiotrack"}, []string{"--version"}, "brew install aubio"),
		},
	}
}

func platformDir(envName string, lookup func() (string, error)) string {
	if configured := os.Getenv(envName); configured != "" {
		return configured
	}
	base, err := lookup()
	if err != nil {
		return filepath.Join(".", ".ave")
	}
	return filepath.Join(base, "ave")
}

func checkFFmpeg(ctx context.Context) Result {
	const remedy = "brew install ffmpeg"

	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return missingResult("ffmpeg", "not found in PATH", remedy)
	}

	version, err := commandOutput(ctx, path, "-version")
	if err != nil {
		return failedCommandResult("ffmpeg", path, err, remedy)
	}
	filters, err := commandOutput(ctx, path, "-hide_banner", "-filters")
	if err != nil {
		return failedCommandResult("ffmpeg", path, err, remedy)
	}
	encoders, err := commandOutput(ctx, path, "-hide_banner", "-encoders")
	if err != nil {
		return failedCommandResult("ffmpeg", path, err, remedy)
	}

	requiredFilters := []string{
		"blurdetect",
		"scdet",
		"vidstabdetect",
		"vidstabtransform",
		"silencedetect",
		"ebur128",
		"xfade",
		"acrossfade",
	}
	availableFilters := capabilityNames(filters)
	availableEncoders := capabilityNames(encoders)
	var missing []string
	for _, filter := range requiredFilters {
		if !availableFilters[filter] {
			missing = append(missing, "filter "+filter)
		}
	}
	if !availableEncoders["h264_videotoolbox"] && !availableEncoders["libx264"] {
		missing = append(missing, "H.264 encoder")
	}
	if !availableEncoders["aac"] {
		missing = append(missing, "AAC encoder")
	}
	if len(missing) > 0 {
		return Result{
			Name:    "ffmpeg",
			Summary: "missing required capabilities: " + strings.Join(missing, ", "),
			Detail:  path,
			Remedy:  remedy + " and ensure the required filters and encoders are enabled",
		}
	}

	return Result{
		Name:    "ffmpeg",
		Ready:   true,
		Summary: firstLine(version),
		Detail:  path,
	}
}

func capabilityNames(output string) map[string]bool {
	names := make(map[string]bool)
	for line := range strings.Lines(output) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] == "=" {
			continue
		}
		names[fields[1]] = true
	}
	return names
}

func checkCommand(
	ctx context.Context,
	name string,
	candidates []string,
	args []string,
	remedy string,
) Result {
	var path string
	for _, candidate := range candidates {
		resolved, err := exec.LookPath(candidate)
		if err == nil {
			path = resolved
			break
		}
	}
	if path == "" {
		return missingResult(name, "not found in PATH", remedy)
	}

	output, err := commandOutput(ctx, path, args...)
	if err != nil {
		return failedCommandResult(name, path, err, remedy)
	}
	summary := firstLine(output)
	if summary == "" {
		summary = "available"
	}
	return Result{Name: name, Ready: true, Summary: summary, Detail: path}
}

func checkLMStudio(ctx context.Context) Result {
	const name = "LM Studio"
	baseURL := os.Getenv("AVE_LM_STUDIO_URL")
	if baseURL == "" {
		baseURL = defaultLMStudioURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return missingResult(name, "invalid server URL", "set AVE_LM_STUDIO_URL to a loopback HTTP URL")
	}
	if !isLoopback(parsed.Hostname()) {
		return missingResult(name, "server URL is not loopback", "use LM Studio on localhost to keep media local")
	}

	requestURL := strings.TrimRight(baseURL, "/") + "/api/v1/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return missingResult(name, "cannot create model request", "set AVE_LM_STUDIO_URL to a valid loopback URL")
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if !isLoopback(request.URL.Hostname()) {
				return fmt.Errorf("refusing redirect to non-loopback host %q", request.URL.Hostname())
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return missingResult(name, "server unavailable", "start the LM Studio local server and retry")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		return missingResult(name, "cannot read model API response", "update or restart LM Studio")
	}
	if closeErr != nil {
		return missingResult(name, "cannot close model API response", "update or restart LM Studio")
	}

	if response.StatusCode != http.StatusOK {
		return missingResult(
			name,
			fmt.Sprintf("model API returned HTTP %d", response.StatusCode),
			"update or restart LM Studio and enable its local server",
		)
	}

	var payload struct {
		Models []struct {
			Type         string `json:"type"`
			Key          string `json:"key"`
			Capabilities struct {
				Vision bool `json:"vision"`
			} `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return missingResult(name, "model API returned invalid JSON", "update or restart LM Studio")
	}

	var models []string
	for _, model := range payload.Models {
		if model.Type == "llm" && model.Capabilities.Vision {
			models = append(models, model.Key)
		}
	}
	if len(models) == 0 {
		return missingResult(name, "no compatible vision model installed", "download a vision-capable model in LM Studio")
	}

	return Result{
		Name:    name,
		Ready:   true,
		Summary: fmt.Sprintf("%d compatible vision model(s)", len(models)),
		Detail:  "vision model: " + strings.Join(models, ", "),
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func commandOutput(ctx context.Context, path string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, path, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", fmt.Errorf("cancelled: %w", ctx.Err())
		}
		return "", fmt.Errorf("%s: %w", firstLine(string(output)), err)
	}
	return string(output), nil
}

func missingResult(name, summary, remedy string) Result {
	return Result{Name: name, Summary: summary, Remedy: remedy}
}

func failedCommandResult(name, path string, err error, remedy string) Result {
	return Result{
		Name:    name,
		Summary: "check failed",
		Detail:  fmt.Sprintf("%s: %v", path, err),
		Remedy:  remedy,
	}
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return line
}
