package claudecli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestClientChatStreamPreservesNativeAssistantTextBeforeToolUse(t *testing.T) {
	tmp := t.TempDir()
	argsFile := tmp + "/args.txt"
	t.Setenv("ARGS_FILE", argsFile)
	fullReport := "Полный отчёт Claude до native tool call.\n\nПроверил исходные данные и нашёл важные детали."
	assistantEnvelope := mustMarshalAssistantEnvelope(t, cliStreamMessage{
		Role:       "assistant",
		StopReason: "tool_use",
		Content: []cliStreamContent{
			{Type: "text", Text: fullReport},
			{Type: "tool_use", ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"report.md"}`)},
		},
	})
	fakeClaude := writeFakeClaudeStreamLines(t, tmp, argsFile,
		assistantEnvelope,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"report contents"}]}}`,
		`{"type":"result","subtype":"success","result":"Сейчас проверю файл.","usage":{"input_tokens":10,"output_tokens":5}}`,
	)

	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
	})
	var deltas []string
	resp, err := client.ChatStream(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "Проверь отчёт"}},
	}, func(event llm.StreamEvent) error {
		if event.Type == llm.StreamEventContentDelta {
			deltas = append(deltas, event.ContentDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	want := fullReport + "\n\nСейчас проверю файл."
	if resp.Content != want {
		t.Fatalf("content = %q, want complete chronological assistant text", resp.Content)
	}
	if got := strings.Join(deltas, ""); got != fullReport {
		t.Fatalf("content deltas = %q, want canonical assistant envelope %q", got, fullReport)
	}
}

func TestClientChatStreamDoesNotDuplicateAssistantEnvelopeAfterLegacyTextDeltas(t *testing.T) {
	tmp := t.TempDir()
	argsFile := tmp + "/args.txt"
	t.Setenv("ARGS_FILE", argsFile)
	assistantEnvelope := mustMarshalAssistantEnvelope(t, cliStreamMessage{
		Role:       "assistant",
		StopReason: "tool_use",
		Content: []cliStreamContent{
			{Type: "text", Text: "Полный отчёт."},
			{Type: "tool_use", ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"report.md"}`)},
		},
	})
	fakeClaude := writeFakeClaudeStreamLines(t, tmp, argsFile,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Полный "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"отчёт."}}}`,
		assistantEnvelope,
		`{"type":"result","subtype":"success","result":"Готово."}`,
	)

	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
	})
	var deltas []string
	resp, err := client.ChatStream(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "Проверь отчёт"}},
	}, func(event llm.StreamEvent) error {
		if event.Type == llm.StreamEventContentDelta {
			deltas = append(deltas, event.ContentDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	want := "Полный отчёт.\n\nГотово."
	if resp.Content != want {
		t.Fatalf("content = %q, want deduplicated chronological text", resp.Content)
	}
	if got := strings.Join(deltas, ""); got != "Полный отчёт." {
		t.Fatalf("content deltas = %q, want no duplicated envelope text", got)
	}
}

func mustMarshalAssistantEnvelope(t *testing.T, message cliStreamMessage) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Type    string           `json:"type"`
		Message cliStreamMessage `json:"message"`
	}{Type: "assistant", Message: message})
	if err != nil {
		t.Fatalf("marshal assistant envelope: %v", err)
	}
	return string(raw)
}
