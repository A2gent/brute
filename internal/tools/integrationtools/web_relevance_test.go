package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm/jev"
)

func webFakeJev(t *testing.T, scores []float64, confidence float64) (*jev.Client, *int) {
	t.Helper()
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var req jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		answers := map[string]any{}
		for key, q := range req.Questions {
			if q.Type != "score" {
				t.Errorf("question type %s", q.Type)
			}
			var index int
			_, _ = fmt.Sscanf(key, "item_%d", &index)
			score := scores[index%len(scores)]
			answers[key] = map[string]any{"score": score, "confidence": confidence}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return jev.NewClient("test-key", "", srv.URL), calls
}

func TestWebRelevanceSelection(t *testing.T) {
	for _, name := range []string{"fetch_url", "tavily_search", "exa_search", "brave_search_query"} {
		t.Run(name, func(t *testing.T) {
			client, calls := webFakeJev(t, []float64{4, 0, 3}, .95)
			output := "## Relevant\nkeep body\n\n## Noise\ndrop body\n\n## Useful\nalso keep\n"
			if name != "fetch_url" {
				output = "Search results\n\n1. Relevant\nURL: https://example.com/a\nSnippet: keep body\n\n2. Noise\nURL: https://example.com/b\nSnippet: drop body\n\n3. Useful\nURL: https://example.com/c\nSnippet: also keep\n"
			}
			tool := NewWebRelevanceTool(&webBenchSource{name, output}, client, WebRelevanceOptions{Enabled: true})
			result, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/page","task_context":"find useful material"}`))
			if err != nil || !result.Success {
				t.Fatalf("%+v %v", result, err)
			}
			if *calls != 1 || strings.Contains(result.Output, "drop body") || !strings.Contains(result.Output, "keep body") || !strings.Contains(result.Output, "Noise") || !strings.Contains(result.Output, "Dropped") {
				t.Fatalf("calls=%d output=%s", *calls, result.Output)
			}
			tool = NewWebRelevanceTool(&webBenchSource{name, output}, client, WebRelevanceOptions{Enabled: true, TopN: 1})
			result, _ = tool.Execute(context.Background(), json.RawMessage(`{"task_context":"find useful material"}`))
			if strings.Contains(result.Output, "also keep") {
				t.Fatal("top-N did not drop lower score")
			}
		})
	}
}

func TestWebRelevanceFallbackAndBypass(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		enabled      bool
		confidence   float64
		wantCalls    int
	}{
		{"disabled", `{"task_context":"x"}`, false, .95, 0},
		{"explicit bypass", `{"task_context":"x","filter_results":false}`, true, .95, 0},
		{"no context", `{"url":"https://example.com"}`, true, .95, 0},
		{"low confidence", `{"task_context":"x"}`, true, .2, 1},
		{"query context", `{"query":"x"}`, true, .95, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, calls := webFakeJev(t, []float64{0}, tc.confidence)
			name := "fetch_url"
			if tc.name == "query context" {
				name = "exa_search"
			}
			output := "## Title\nfull body"
			if name != "fetch_url" {
				output = "Search\n\n1. Title\nURL: https://example.com\nContent: full body\n"
			}
			tool := NewWebRelevanceTool(&webBenchSource{name, output}, client, WebRelevanceOptions{Enabled: tc.enabled})
			result, err := tool.Execute(context.Background(), json.RawMessage(tc.params))
			if err != nil || *calls != tc.wantCalls {
				t.Fatalf("calls %d err %v", *calls, err)
			}
			if tc.name != "query context" && result.Output != output {
				t.Fatalf("fallback changed output: %s", result.Output)
			}
		})
	}
}

func TestWebRelevanceNeverSendsSecrets(t *testing.T) {
	for _, text := range []string{"api_key=very-secret-value", `{"password":"private-value"}`, "-----BEGIN PRIVATE KEY-----", "Bearer privatevalue", "https://example.com?token=secret", "known-private-credential", "https://user:password@example.com", "http://127.0.0.1/admin", "https://example.com/.env", "https://example.com?signature=private"} {
		t.Run(text, func(t *testing.T) {
			client, calls := webFakeJev(t, []float64{0}, .95)
			output := "## Title\n" + text
			tool := NewWebRelevanceTool(&webBenchSource{"fetch_url", output}, client, WebRelevanceOptions{Enabled: true, Secrets: []string{"known-private-credential"}})
			result, err := tool.Execute(context.Background(), json.RawMessage(`{"task_context":"x"}`))
			if err != nil || *calls != 0 || result.Output != output {
				t.Fatalf("secret sent/changed: calls=%d result=%+v err=%v", *calls, result, err)
			}
		})
	}
}
