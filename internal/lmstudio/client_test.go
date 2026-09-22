package lmstudio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
