package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestNormalizeBaseURLStripsSystemOneSuffix(t *testing.T) {
	t.Parallel()

	if got := NormalizeBaseURL(" https://api.typesafe.ai/v1/systemone/ "); got != "https://api.typesafe.ai/v1" {
		t.Fatalf("NormalizeBaseURL = %q", got)
	}
	if got := NormalizeBaseURL(""); got != defaultBaseURL {
		t.Fatalf("empty URL should default, got %q", got)
	}
}

func TestChatClassifiesAutomaticRouterPayloadAsChoice(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Fatalf("path = %s, want /v1/systemone", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {
				"route": {"type": "choice", "choice": "2", "confidence": 0.91}
			},
			"usage": {"input_tokens": 40, "output_tokens": 8}
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", "jev-latest", server.URL+"/v1")
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		SystemPrompt: "You are a strict model router.",
		Messages: []llm.Message{{
			Role: "user",
			Content: `Rules: [{"index":1,"match":"documentation","target":"google/docs"},{"index":2,"match":"coding","target":"cursor/composer"}]

User prompt: Add a failing test and implement the fix.`,
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(resp.Content, `"index":2`) {
		t.Fatalf("content = %s, want index 2", resp.Content)
	}
	if !strings.Contains(resp.Content, "jev confidence=0.91") {
		t.Fatalf("content = %s, want confidence reason", resp.Content)
	}
	if captured["state"] != "Add a failing test and implement the fix." {
		t.Fatalf("state = %#v", captured["state"])
	}
	questions, _ := captured["questions"].(map[string]any)
	route, _ := questions["route"].(map[string]any)
	if route["type"] != "choice" {
		t.Fatalf("question type = %#v", route["type"])
	}
	criteria, _ := route["criteria"].(map[string]any)
	if _, ok := criteria["2"]; !ok {
		t.Fatalf("criteria missing rule 2: %#v", criteria)
	}
}

func TestChatConnectivityCheckUsesNoul(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"type":"noul"`) {
			t.Fatalf("expected noul question, got %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {"ok": {"type": "noul", "noul": 0.97}},
			"usage": {"input_tokens": 12, "output_tokens": 4}
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", "", server.URL+"/v1")
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(resp.Content, "connected") {
		t.Fatalf("content = %s", resp.Content)
	}
}

func TestChatRejectsToolCalling(t *testing.T) {
	t.Parallel()

	client := NewClient("test-key", "jev-latest", "https://api.typesafe.ai/v1")
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "edit files"}},
		Tools:    []llm.ToolDefinition{{Name: "bash"}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not support tool-calling") {
		t.Fatalf("error = %v", err)
	}
}

func TestListModelsIncludesDefaultAlias(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest"},{"name":"jev-preview"}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", "", server.URL+"/v1")
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	joined := strings.Join(models, ",")
	if !strings.Contains(joined, "jev-latest") || !strings.Contains(joined, "jev-preview") {
		t.Fatalf("models = %v", models)
	}
}
