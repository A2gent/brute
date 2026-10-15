package openaicodex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestPublicAPIRequestServiceTierFlex(t *testing.T) {
	var body map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	client := NewClientWithOptions("test", "gpt-5.4-mini", upstream.URL, Options{PublicAPI: true, ServiceTier: "priority", ReasoningEffort: "high"})
	if _, err := client.Chat(t.Context(), &llm.ChatRequest{ServiceTier: "flex", ReasoningEffort: "none"}); err != nil {
		t.Fatal(err)
	}
	if body["service_tier"] != "flex" {
		t.Errorf("service_tier = %v, want flex", body["service_tier"])
	}
	if body["reasoning"].(map[string]interface{})["effort"] != "none" {
		t.Errorf("reasoning = %v, want none", body["reasoning"])
	}
}

func TestEmptyIncompleteResponseReturnsError(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, reason := range []string{"max_output_tokens", "content_filter", ""} {
			t.Run(strings.Join([]string{map[bool]string{false: "json", true: "stream"}[streaming], reason}, "/"), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					response := map[string]interface{}{
						"status": "incomplete", "incomplete_details": map[string]string{"reason": reason},
						"output": []interface{}{map[string]string{"type": "reasoning", "id": "rs_capped"}},
						"usage":  map[string]interface{}{"output_tokens": 80, "output_tokens_details": map[string]int{"reasoning_tokens": 80}},
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: "))
						_ = json.NewEncoder(w).Encode(map[string]interface{}{"type": "response.incomplete", "response": response})
						_, _ = w.Write([]byte("\n"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(response)
					}
				}))
				defer upstream.Close()
				client := NewClientWithOptions("test", "gpt-5-mini", upstream.URL, Options{PublicAPI: true})
				_, err := client.Chat(t.Context(), &llm.ChatRequest{MaxTokens: 80})
				if err == nil || !strings.Contains(err.Error(), "incomplete") || (reason != "" && !strings.Contains(err.Error(), reason)) {
					t.Fatalf("empty incomplete response error = %v, want incomplete with reason %q", err, reason)
				}
			})
		}
	}
}

func TestIncompleteStreamKeepsUsefulOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prefix  string
		output  string
		content string
		tools   int
	}{
		{"delta fallback", `{"type":"response.output_text.delta","delta":"partial"}`, `[]`, "partial", 0},
		{"tool call", "", `[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]`, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if tc.prefix != "" {
				body = "data: " + tc.prefix + "\n\n"
			}
			body += `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":` + tc.output + "}}\n\n"
			response, err := parseStreamResponse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if response.Content != tc.content || len(response.ToolCalls) != tc.tools {
				t.Fatalf("useful incomplete output lost: %+v", response)
			}
		})
	}
}
