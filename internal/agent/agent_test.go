package agent

import (
	"context"
	"os"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

// MockLLM is a mock implementation of llm.Client
type MockLLM struct {
	CapturedRequest *llm.ChatRequest
	Response        *llm.ChatResponse
	Err             error
}

func (m *MockLLM) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	m.CapturedRequest = request
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Response, nil
}

func TestMaybeCompactContext(t *testing.T) {
	// Setup temporary session storage
	tmpDir, err := os.MkdirTemp("", "session_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup dependencies
	store, err := storage.NewSQLiteStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	mockLLM := &MockLLM{
		Response: &llm.ChatResponse{
			Content: "Summarized content",
			Usage: llm.TokenUsage{
				InputTokens:  50,
				OutputTokens: 20,
			},
		},
	}

	cfg := Config{
		ContextWindow:            1000,
		CompactionTriggerPercent: 50.0, // Trigger at 500 tokens
		CompactionPrompt:         "Compact this",
	}

	a := New(cfg, mockLLM, nil, sm)

	// Create a session
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add some messages
	sess.AddUserMessage("Hello")
	sess.AddAssistantMessage("Hi there", nil)
	sess.AddUserMessage("How are you?")
	sess.AddAssistantMessage("I'm good", nil)

	// Manually set metadata to simulate high token usage
	// 600 tokens > 50% of 1000
	metadataSetFloat(sess, metadataCurrentContextTokens, 600)
	
	// Debug: print the values being checked
	if testing.Verbose() {
		cfg := a.resolveCompactionConfig()
		currentTokens := metadataFloat(sess.Metadata, metadataCurrentContextTokens)
		usagePercent := (currentTokens / float64(cfg.ContextWindow)) * 100.0
		t.Logf("Debug: cfg.Enabled=%v, cfg.ContextWindow=%d, cfg.TriggerPercent=%f", cfg.Enabled, cfg.ContextWindow, cfg.TriggerPercent)
		t.Logf("Debug: currentTokens=%f, usagePercent=%f, should trigger=%v", currentTokens, usagePercent, usagePercent >= cfg.TriggerPercent)
		t.Logf("Debug: sess.Messages count=%d", len(sess.Messages))
	}

	// Run compaction
	_, compacted, err := a.maybeCompactContext(context.Background(), sess, 1)

	if err != nil {
		t.Fatalf("Compaction failed: %v", err)
	}
	if !compacted {
		t.Fatal("Expected compaction to happen")
	}

	// Verify request to LLM
	if mockLLM.CapturedRequest == nil {
		t.Fatal("Expected LLM request")
	}

	// Check that we sent a single aggregated message for compaction
	// (conversation history is flattened into one user message for the summarizer)
	if len(mockLLM.CapturedRequest.Messages) != 1 {
		t.Errorf("Expected 1 aggregated message to be sent for compaction, got %d", len(mockLLM.CapturedRequest.Messages))
	}
	if mockLLM.CapturedRequest.Messages[0].Role != "user" {
		t.Errorf("Expected aggregated message to have role 'user', got %s", mockLLM.CapturedRequest.Messages[0].Role)
	}

	// Check final session state
	// We expect 6 messages: [User, Assistant, Summary, User, Assistant, Synthetic Continuation]
	// The first 2 are summarized but kept in history.
	// The Summary is inserted at index 2.
	// The last 2 are kept raw.
	// A synthetic continuation message is added at the end.

	if len(sess.Messages) != 6 {
		t.Errorf("Expected 6 messages after compaction, got %d", len(sess.Messages))
	}

	if len(sess.Messages) == 6 {
		if sess.Messages[2].Role != "assistant" {
			t.Errorf("Expected message at index 2 to be summary (assistant), got %s", sess.Messages[2].Role)
		}

		isCompaction := false
		if sess.Messages[2].Metadata != nil {
			if v, ok := sess.Messages[2].Metadata["context_compaction"]; ok {
				if b, ok := v.(bool); ok && b {
					isCompaction = true
				}
			}
		}

		if !isCompaction {
			t.Errorf("Expected message at index 2 to be compaction summary")
		}

		if sess.Messages[3].Content != "How are you?" {
			t.Errorf("Expected message at index 3 to be 'How are you?', got '%s'", sess.Messages[3].Content)
		}

		// Check that the last message is the synthetic continuation
		lastMsg := sess.Messages[5]
		if lastMsg.Role != "user" {
			t.Errorf("Expected last message to be user (synthetic continuation), got %s", lastMsg.Role)
		}
		if lastMsg.Metadata == nil || lastMsg.Metadata["synthetic_continuation"] != true {
			t.Errorf("Expected last message to have synthetic_continuation metadata")
		}
	}
}
