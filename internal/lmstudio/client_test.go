package lmstudio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsNonLoopback(t *testing.T) {
	if _, err := New("http://example.com:1234"); err == nil {
		t.Fatal("expected non-loopback host to be rejected")
	}
	if _, err := New("http://127.0.0.1:1234"); err != nil {
		t.Fatalf("loopback host should be accepted: %v", err)
	}
}

func TestListVisionModelsFiltersToVisionLLMs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[
			{"type":"llm","key":"vision-a","capabilities":{"vision":true}},
			{"type":"llm","key":"text-b","capabilities":{"vision":false}},
			{"type":"embeddings","key":"emb-c","capabilities":{"vision":true}}
		]}`)
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := client.ListVisionModels(context.Background())
	if err != nil {
		t.Fatalf("ListVisionModels: %v", err)
	}
	if len(models) != 1 || models[0] != "vision-a" {
		t.Fatalf("models = %v, want [vision-a]", models)
	}
}

func TestScoreParsesValidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatEnvelope(`{"visual_interest":0.8,"subjects":["dog"],"actions":["running"],"energy":0.7,"usefulness":0.9,"redundancy":0.1}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	result, err := client.Score(context.Background(), ScoreRequest{Model: "m", ImagePNG: []byte("png")})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.Repaired {
		t.Error("valid response should not be marked repaired")
	}
	if result.Score.VisualInterest != 0.8 || result.Score.Subjects[0] != "dog" {
		t.Fatalf("unexpected score %+v", result.Score)
	}
}

func TestScoreRepairsOnceThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, chatEnvelope("not json at all"))
			return
		}
		_, _ = io.WriteString(w, chatEnvelope(`{"visual_interest":0.5,"subjects":[],"actions":[],"energy":0.5,"usefulness":0.5,"redundancy":0.5}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	result, err := client.Score(context.Background(), ScoreRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Score with one repair: %v", err)
	}
	if !result.Repaired {
		t.Error("expected result to be marked repaired")
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
}

func TestScoreFailsAfterRepairAttempt(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, chatEnvelope("still not json"))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	_, err := client.Score(context.Background(), ScoreRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected failure after unsuccessful repair")
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
	if !strings.Contains(err.Error(), "repair") {
		t.Errorf("error should mention the repair attempt: %v", err)
	}
	if !errors.Is(err, ErrUnparseableScore) {
		t.Errorf("error should wrap ErrUnparseableScore so callers can skip the candidate: %v", err)
	}
}

func TestExtractJSONStripsReasoningBlocks(t *testing.T) {
	object := `{"visual_interest":0.4}`
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain object", raw: object, want: object},
		{name: "fenced object", raw: "```json\n" + object + "\n```", want: object},
		{
			name: "reasoning before object",
			raw:  "<think>The scene looks calm {maybe 0.3}</think>\n" + object,
			want: object,
		},
		{
			name: "reasoning with braces does not corrupt object",
			raw:  "<think>weigh {a} vs {b}</think>" + object,
			want: object,
		},
		{
			name: "unterminated reasoning yields no object",
			raw:  "<think>I am still thinking and never produced JSON",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSON(tc.raw); got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestScoreParsesResponseWithReasoningBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, chatEnvelope("<think>subjects are dogs {running}</think>\n"+
			`{"visual_interest":0.8,"subjects":["dog"],"actions":["running"],"energy":0.7,"usefulness":0.9,"redundancy":0.1}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	result, err := client.Score(context.Background(), ScoreRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Score with reasoning block: %v", err)
	}
	if result.Repaired {
		t.Error("a response whose JSON follows a reasoning block should parse without repair")
	}
	if result.Score.VisualInterest != 0.8 || result.Score.Subjects[0] != "dog" {
		t.Fatalf("unexpected score %+v", result.Score)
	}
}

func TestScoreRejectsOutOfRangeValues(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, chatEnvelope(`{"visual_interest":9,"subjects":[],"actions":[],"energy":0.5,"usefulness":0.5,"redundancy":0.5}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	if _, err := client.Score(context.Background(), ScoreRequest{Model: "m"}); err == nil {
		t.Fatal("expected out-of-range score to be rejected")
	}
	if calls != 2 {
		t.Errorf("out-of-range value should trigger one repair: got %d calls", calls)
	}
}

func TestScoreSendsImageAsDataURL(t *testing.T) {
	var sawImage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for _, m := range payload.Messages {
			if strings.Contains(string(m.Content), "data:image/png;base64,") {
				sawImage = true
			}
		}
		_, _ = io.WriteString(w, chatEnvelope(`{"visual_interest":0.5,"subjects":[],"actions":[],"energy":0.5,"usefulness":0.5,"redundancy":0.5}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	if _, err := client.Score(context.Background(), ScoreRequest{Model: "m", ImagePNG: []byte("realbytes")}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !sawImage {
		t.Error("expected the contact sheet to be sent as a base64 data URL")
	}
}

// chatEnvelope wraps assistant content in an OpenAI-compatible chat response.
func chatEnvelope(content string) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	})
	return string(body)
}

func TestDiscoverThemeParsesValidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatEnvelope(`{"theme":"a day at the beach"}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	result, err := client.DiscoverTheme(context.Background(), ThemeRequest{Model: "m", UserPrompt: "subjects: beach"})
	if err != nil {
		t.Fatalf("DiscoverTheme: %v", err)
	}
	if result.Repaired {
		t.Error("valid response should not be marked repaired")
	}
	if result.Theme != "a day at the beach" {
		t.Fatalf("unexpected theme %q", result.Theme)
	}
}

func TestDiscoverThemeRepairsOnceThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, chatEnvelope("prose without json"))
			return
		}
		_, _ = io.WriteString(w, chatEnvelope(`{"theme":"surfing"}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	result, err := client.DiscoverTheme(context.Background(), ThemeRequest{Model: "m"})
	if err != nil {
		t.Fatalf("DiscoverTheme with one repair: %v", err)
	}
	if !result.Repaired || result.Theme != "surfing" {
		t.Fatalf("unexpected result %+v", result)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
}

func TestDiscoverThemeFailsAfterRepairAttempt(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, chatEnvelope(`{"theme":""}`))
	}))
	defer server.Close()

	client, _ := New(server.URL)
	_, err := client.DiscoverTheme(context.Background(), ThemeRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected failure when theme never validates")
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
	if !strings.Contains(err.Error(), "repair") {
		t.Errorf("error should mention the repair attempt: %v", err)
	}
}

func TestResolveTimeoutHonorsEnv(t *testing.T) {
	if got := resolveTimeout(); got != defaultTimeout {
		t.Errorf("default timeout = %v, want %v", got, defaultTimeout)
	}

	t.Setenv(timeoutEnv, "250ms")
	if got := resolveTimeout(); got != 250*time.Millisecond {
		t.Errorf("env timeout = %v, want 250ms", got)
	}

	t.Setenv(timeoutEnv, "not-a-duration")
	if got := resolveTimeout(); got != defaultTimeout {
		t.Errorf("invalid env timeout = %v, want fallback %v", got, defaultTimeout)
	}

	t.Setenv(timeoutEnv, "-5s")
	if got := resolveTimeout(); got != defaultTimeout {
		t.Errorf("non-positive env timeout = %v, want fallback %v", got, defaultTimeout)
	}
}
