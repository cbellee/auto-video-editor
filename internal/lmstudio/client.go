// Package lmstudio is a minimal client for a locally hosted LM Studio server.
// All requests are restricted to loopback hosts so that frames and analysis
// never leave the machine.
package lmstudio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the loopback LM Studio server used when none is configured.
const DefaultBaseURL = "http://127.0.0.1:1234"

// baseURLEnv overrides the LM Studio server location.
const baseURLEnv = "AVE_LM_STUDIO_URL"

// Client talks to a loopback LM Studio server.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for baseURL, rejecting any non-loopback host so that
// media cannot be sent off the machine.
func New(baseURL string) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid LM Studio URL %q", baseURL)
	}
	if !IsLoopback(parsed.Hostname()) {
		return nil, fmt.Errorf("LM Studio host %q is not loopback; refusing to send media off the machine", parsed.Hostname())
	}
	client := &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if !IsLoopback(request.URL.Hostname()) {
				return fmt.Errorf("refusing redirect to non-loopback host %q", request.URL.Hostname())
			}
			return nil
		},
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: client}, nil
}

// FromEnv returns a Client using AVE_LM_STUDIO_URL or the default loopback URL.
func FromEnv() (*Client, error) {
	base := os.Getenv(baseURLEnv)
	if base == "" {
		base = DefaultBaseURL
	}
	return New(base)
}

// BaseURL reports the configured server location.
func (c *Client) BaseURL() string { return c.baseURL }

// IsLoopback reports whether host is a loopback name or address.
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// ListVisionModels returns the keys of installed vision-capable LLMs.
func (c *Client) ListVisionModels(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build model request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("contact LM Studio: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LM Studio model API returned status %d", response.StatusCode)
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
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	var models []string
	for _, model := range payload.Models {
		if model.Type == "llm" && model.Capabilities.Vision {
			models = append(models, model.Key)
		}
	}
	return models, nil
}

// Score is the structured visual assessment the model returns for one
// Candidate Segment.
type Score struct {
	VisualInterest float64  `json:"visual_interest"`
	Subjects       []string `json:"subjects"`
	Actions        []string `json:"actions"`
	Energy         float64  `json:"energy"`
	Usefulness     float64  `json:"usefulness"`
	Redundancy     float64  `json:"redundancy"`
}

// valid reports whether every numeric score is within the unit interval.
func (s Score) valid() error {
	for name, value := range map[string]float64{
		"visual_interest": s.VisualInterest,
		"energy":          s.Energy,
		"usefulness":      s.Usefulness,
		"redundancy":      s.Redundancy,
	} {
		if value < 0 || value > 1 {
			return fmt.Errorf("%s %.3f is outside the 0..1 range", name, value)
		}
	}
	return nil
}

// ScoreRequest carries the inputs for scoring one Candidate Segment.
type ScoreRequest struct {
	Model        string
	SystemPrompt string
	UserPrompt   string
	ImagePNG     []byte
}

// ScoreResult pairs the parsed Score with the raw response retained as
// provenance.
type ScoreResult struct {
	Score       Score
	RawResponse string
	Repaired    bool
}

// chatMessage is one OpenAI-compatible chat message with optional multimodal
// content parts.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// Score requests a visual assessment, making exactly one schema-guided repair
// attempt if the first response is not valid JSON matching the Score schema.
func (c *Client) Score(ctx context.Context, req ScoreRequest) (ScoreResult, error) {
	messages := []chatMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: userContent(req.UserPrompt, req.ImagePNG)},
	}

	raw, err := c.chat(ctx, req.Model, messages)
	if err != nil {
		return ScoreResult{}, err
	}
	score, parseErr := parseScore(raw)
	if parseErr == nil {
		return ScoreResult{Score: score, RawResponse: raw}, nil
	}

	// One schema-guided repair attempt; never guess a fallback on failure.
	messages = append(messages,
		chatMessage{Role: "assistant", Content: raw},
		chatMessage{Role: "user", Content: repairInstruction(parseErr)},
	)
	repaired, err := c.chat(ctx, req.Model, messages)
	if err != nil {
		return ScoreResult{}, err
	}
	score, parseErr = parseScore(repaired)
	if parseErr != nil {
		return ScoreResult{}, fmt.Errorf("model returned an invalid score after one repair attempt: %w", parseErr)
	}
	return ScoreResult{Score: score, RawResponse: repaired, Repaired: true}, nil
}

// userContent assembles the multimodal user turn: the prompt text followed by
// the contact sheet as an inline data URL.
func userContent(prompt string, image []byte) []contentPart {
	parts := []contentPart{{Type: "text", Text: prompt}}
	if len(image) > 0 {
		encoded := base64.StdEncoding.EncodeToString(image)
		parts = append(parts, contentPart{
			Type:     "image_url",
			ImageURL: &imageURL{URL: "data:image/png;base64," + encoded},
		})
	}
	return parts
}

func repairInstruction(cause error) string {
	return fmt.Sprintf(
		"That response was not valid: %s. Reply with only a JSON object matching the schema "+
			"{\"visual_interest\":number,\"subjects\":[string],\"actions\":[string],"+
			"\"energy\":number,\"usefulness\":number,\"redundancy\":number} "+
			"where every number is between 0 and 1. Do not include any other text.",
		cause,
	)
}

// chat posts a chat completion and returns the assistant message content.
func (c *Client) chat(ctx context.Context, model string, messages []chatMessage) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":           model,
		"messages":        messages,
		"temperature":     0,
		"response_format": scoreResponseFormat(),
	})
	if err != nil {
		return "", fmt.Errorf("encode chat request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("contact LM Studio: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LM Studio chat API returned status %d", response.StatusCode)
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return "", fmt.Errorf("LM Studio chat API returned no choices")
	}
	return payload.Choices[0].Message.Content, nil
}

// parseScore extracts a Score from a possibly fenced JSON string and validates
// its numeric ranges.
func parseScore(raw string) (Score, error) {
	trimmed := extractJSON(raw)
	if trimmed == "" {
		return Score{}, fmt.Errorf("response contained no JSON object")
	}
	var score Score
	if err := json.Unmarshal([]byte(trimmed), &score); err != nil {
		return Score{}, fmt.Errorf("parse score JSON: %w", err)
	}
	if err := score.valid(); err != nil {
		return Score{}, err
	}
	return score, nil
}

// extractJSON returns the substring from the first '{' to the last '}',
// tolerating code fences or stray prose around the object.
func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return ""
	}
	return raw[start : end+1]
}

// scoreResponseFormat asks LM Studio for structured JSON output matching the
// Score schema.
func scoreResponseFormat() map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "candidate_score",
			"strict": true,
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"visual_interest": map[string]any{"type": "number"},
					"subjects":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"actions":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"energy":          map[string]any{"type": "number"},
					"usefulness":      map[string]any{"type": "number"},
					"redundancy":      map[string]any{"type": "number"},
				},
				"required": []string{"visual_interest", "subjects", "actions", "energy", "usefulness", "redundancy"},
			},
		},
	}
}
