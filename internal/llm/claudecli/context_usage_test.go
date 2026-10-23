package claudecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestUsageFromRawIncludesCacheInInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want llm.TokenUsage
	}{
		{"uncached", `{"input_tokens":20,"output_tokens":3}`, llm.TokenUsage{InputTokens: 20, OutputTokens: 3}},
		{"cache hit", `{"input_tokens":20,"cache_read_input_tokens":900,"cache_creation_input_tokens":100,"output_tokens":3}`, llm.TokenUsage{InputTokens: 1020, OutputTokens: 3, CachedInputTokens: 900}},
		{"cache write", `{"input_tokens":20,"cache_creation_input_tokens":100}`, llm.TokenUsage{InputTokens: 120}},
		{"normalized cache alias", `{"input_tokens":1000,"cached_input_tokens":900}`, llm.TokenUsage{InputTokens: 1000, CachedInputTokens: 900}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usageFromRaw(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("usage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStreamUsageKeepsLatestContextSeparateFromBilledTotals(t *testing.T) {
	for _, source := range []string{"assistant", "stream_event"} {
		t.Run(source, func(t *testing.T) {
			var emitted llm.TokenUsage
			p := newStreamProcessor(func(ev llm.StreamEvent) error {
				if ev.Type == llm.StreamEventUsage {
					emitted = ev.Usage
				}
				return nil
			})
			messages := []string{
				`{"input_tokens":20,"cache_creation_input_tokens":1000,"output_tokens":30}`,
				`{"input_tokens":10,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":40}`,
			}
			for _, usage := range messages {
				line := `{"type":"assistant","message":{"role":"assistant","usage":` + usage + `}}`
				if source == "stream_event" {
					line = `{"type":"stream_event","event":{"type":"message_start","message":{"usage":` + usage + `}}}`
				}
				var event cliStreamEnvelope
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if err := p.handleEnvelope(event); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.handleEnvelope(cliStreamEnvelope{Type: "result", NumTurns: 2, Usage: json.RawMessage(`{"input_tokens":30,"cache_read_input_tokens":1000,"cache_creation_input_tokens":1200,"output_tokens":70}`)}); err != nil {
				t.Fatal(err)
			}
			if err := p.finalize(p.onEvent); err != nil {
				t.Fatal(err)
			}
			want := llm.TokenUsage{InputTokens: 2230, CachedInputTokens: 1000, OutputTokens: 70, ContextTokens: 1210}
			if p.usage != want || emitted != want {
				t.Fatalf("usage = %+v, emitted = %+v, want %+v", p.usage, emitted, want)
			}
		})
	}
}

func TestMultiTurnResultWithoutPerTurnUsageHasUnknownContext(t *testing.T) {
	result := `{"type":"result","subtype":"success","result":"done","num_turns":3,"usage":{"input_tokens":20,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":70}}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			var response *llm.ChatResponse
			if stream {
				response, _ = runFakeClaude(t, result)
			} else {
				dir := t.TempDir()
				binary := filepath.Join(dir, "claude")
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+result+"'\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				client := NewClientWithOptions("sonnet", Options{Executable: binary, WorkDir: dir, NoSessionPersistence: true})
				var err error
				response, err = client.Chat(t.Context(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if response.Usage.ContextTokens != -1 || response.Usage.InputTokens != 1220 {
				t.Fatalf("usage = %+v, want unknown context with billed input 1220", response.Usage)
			}
		})
	}
}
