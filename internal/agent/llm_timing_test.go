package agent

import (
	"context"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type timingStreamLLM struct {
	MockLLM
	first llm.StreamEvent
}

func (m *timingStreamLLM) ChatStream(ctx context.Context, req *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	for _, ev := range []llm.StreamEvent{
		{Type: llm.StreamEventProviderTrace, Phase: "attempt_started"},
		{Type: llm.StreamEventUsage},
		{Type: llm.StreamEventContentDelta},
		{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "thinking"},
	} {
		if err := onEvent(ev); err != nil {
			return nil, err
		}
	}
	time.Sleep(30 * time.Millisecond)
	if m.first.Type != "" {
		if err := onEvent(m.first); err != nil {
			return nil, err
		}
	}
	time.Sleep(30 * time.Millisecond)
	return &llm.ChatResponse{Content: "done"}, nil
}

func TestLoopRecordsStreamingTTFT(t *testing.T) {
	for _, first := range []llm.StreamEvent{
		{Type: llm.StreamEventContentDelta, ContentDelta: "hello"},
		{Type: llm.StreamEventToolCallDelta, ToolCallID: "call", ToolInputDelta: "{"},
		{Type: llm.StreamEventToolStarted, ToolCallID: "call", ToolCallName: "read"},
		{},
	} {
		t.Run(string(first.Type), func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("ttft")
			if err != nil {
				t.Fatal(err)
			}
			client := &timingStreamLLM{first: first}
			ag := New(Config{MaxSteps: 1}, client, tools.NewManager(t.TempDir()), sm)
			if _, _, err := ag.RunWithEvents(context.Background(), sess, "hello", nil); err != nil {
				t.Fatal(err)
			}
			metadata := sess.Messages[len(sess.Messages)-1].Metadata
			value, present := metadata["llm_ttft_ms"]
			if first.Type == "" {
				if present {
					t.Fatalf("no content/tool delta, but TTFT recorded: %v", value)
				}
				return
			}
			ttft, ok := value.(int64)
			if !ok || ttft < 25 || ttft > metadata["llm_duration_ms"].(int64)-25 {
				t.Fatalf("TTFT must measure first delta, not trace/reasoning/completion: %v (metadata %v)", value, metadata)
			}
		})
	}
}
