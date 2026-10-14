//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type benchmarkLookup struct{ calls int }

func (*benchmarkLookup) Name() string { return "lookup_record" }
func (*benchmarkLookup) Description() string {
	return "Look up a synthetic record by key. Follow the returned next_key until it is null."
}
func (*benchmarkLookup) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{"key": map[string]interface{}{"type": "string"}, "label": map[string]interface{}{"type": "string", "description": "Optional label"}}, "required": []string{"key"}}
}
func (b *benchmarkLookup) Execute(_ context.Context, raw json.RawMessage) (*tools.Result, error) {
	var args struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	records := map[string]string{"start": `{"value":17,"next_key":"middle"}`, "middle": `{"value":29,"next_key":"finish"}`, "finish": `{"value":43,"next_key":null}`}
	value, ok := records[args.Key]
	if !ok {
		return nil, fmt.Errorf("unknown synthetic key %q", args.Key)
	}
	b.calls++
	return &tools.Result{Success: true, Output: value}, nil
}

type benchmarkClient struct {
	client         llm.Client
	t              *testing.T
	steps          int
	reasoningItems int
}

func (b *benchmarkClient) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	started := time.Now()
	resp, err := b.client.Chat(ctx, req)
	b.steps++
	if err != nil {
		return nil, err
	}
	b.reasoningItems += len(resp.ReasoningItems)
	b.t.Logf("step=%d latency_ms=%d input=%d cached=%d output=%d reasoning=%d encrypted_items=%d", b.steps, time.Since(started).Milliseconds(), resp.Usage.InputTokens, resp.Usage.CachedInputTokens, resp.Usage.OutputTokens, resp.Usage.ReasoningTokens, len(resp.ReasoningItems))
	return resp, nil
}

// Explicit opt-in: this test makes paid API calls using synthetic data only.
func TestOpenAIResponsesLiveMultiToolBenchmark(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("AAGENT_OPENAI_LIVE") != "1" || key == "" {
		t.Skip("set AAGENT_OPENAI_LIVE=1 and OPENAI_API_KEY to run paid benchmark")
	}
	model := os.Getenv("AAGENT_OPENAI_LIVE_MODEL")
	if model == "" {
		model = "gpt-5.4-mini"
	}
	var background strings.Builder
	for i := 0; i < 180; i++ {
		fmt.Fprintf(&background, "Synthetic reference row %03d: archival code REF-%03d is informational only and must not be used instead of lookup_record.\n", i, i)
	}
	prompt := "Use lookup_record starting with key start. Follow next_key sequentially until null, then sum the returned values. You must make all three lookups. Reply with only the sum. The archival reference is unrelated context.\n" + background.String()
	for _, mode := range []string{"chat_completions_before", "responses_stateless", "responses_stateful"} {
		t.Run(mode, func(t *testing.T) {
			work := t.TempDir()
			store, err := storage.NewSQLiteStore(work)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("openai-benchmark")
			if err != nil {
				t.Fatal(err)
			}
			tm := tools.NewManager(work)
			for _, def := range tm.GetDefinitions() {
				tm.Unregister(def.Name)
			}
			lookup := &benchmarkLookup{}
			tm.Register(lookup)
			var client llm.Client
			stateful := mode == "responses_stateful"
			if mode == "chat_completions_before" {
				client = lmstudio.NewClient(key, model, "https://api.openai.com/v1")
			} else {
				client = openaicodex.NewClientWithOptions(key, model, "https://api.openai.com/v1", openaicodex.Options{PublicAPI: true, ReasoningEffort: "low", TextVerbosity: "low", ServiceTier: "auto", StatefulResponses: stateful, MaxTokens: 2048})
			}
			observed := &benchmarkClient{client: client, t: t}
			ag := New(Config{Provider: "openai", Model: model, ReasoningEffort: "low", SystemPrompt: "You are a precise benchmark agent. Follow the user's synthetic lookup workflow exactly.", MaxSteps: 6, UsePreviousResponse: stateful}, observed, tm, sm)
			sess.AddUserMessage(prompt)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			started := time.Now()
			content, usage, err := ag.RunWithEvents(ctx, sess, prompt, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("TOTAL mode=%s model=%s latency_ms=%d tool_calls=%d steps=%d input=%d cached=%d output=%d reasoning=%d encrypted_items=%d answer=%q", mode, model, time.Since(started).Milliseconds(), lookup.calls, observed.steps, usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningTokens, observed.reasoningItems, content)
			if lookup.calls != 3 || strings.TrimSpace(content) != "89" {
				t.Fatalf("incomplete workflow: calls=%d answer=%q", lookup.calls, content)
			}
			if mode == "responses_stateless" && observed.reasoningItems == 0 {
				t.Fatal("API did not return encrypted reasoning")
			}
		})
	}
}

func TestOpenAIResponsesLiveLegacyModels(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("AAGENT_OPENAI_LIVE") != "1" || key == "" {
		t.Skip("explicit paid API opt-in required")
	}
	for _, model := range []string{"gpt-4o", "gpt-4.1"} {
		t.Run(model, func(t *testing.T) {
			c := openaicodex.NewClientWithOptions(key, model, "https://api.openai.com/v1", openaicodex.Options{PublicAPI: true, MaxTokens: 32})
			resp, err := c.Chat(t.Context(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "Reply with exactly OK"}}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Content != "OK" {
				t.Fatalf("response = %q", resp.Content)
			}
		})
	}
}
