package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// MockLLM is a mock implementation of llm.Client
type MockLLM struct {
	CapturedRequest  *llm.ChatRequest
	CapturedRequests []*llm.ChatRequest
	Response         *llm.ChatResponse
	Responses        []*llm.ChatResponse
	Err              error
}

func (m *MockLLM) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	m.CapturedRequest = request
	m.CapturedRequests = append(m.CapturedRequests, request)
	if m.Err != nil {
		return nil, m.Err
	}
	if len(m.Responses) > 0 {
		response := m.Responses[0]
		m.Responses = m.Responses[1:]
		return response, nil
	}
	return m.Response, nil
}

func TestDefaultSystemPromptIncludesSessionWidgetConventions(t *testing.T) {
	prompts := map[string]string{
		"default system prompt":        DefaultSystemPrompt(),
		"built-in tools guidance only": DefaultBuiltInToolsGuidance(),
	}

	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			for _, expected := range []string{
				"Output/widget conventions",
				"plain text paths with optional line ranges",
				"suggest_git_commit exactly once",
				"a2gent-git-commit block",
				"a2gent-map JSON block",
				"terminal fallback is the address list",
			} {
				if !strings.Contains(prompt, expected) {
					t.Fatalf("expected prompt to mention %q, got:\n%s", expected, prompt)
				}
			}
		})
	}
}

func TestMaybeCompactContext(t *testing.T) {
	os.Unsetenv("AAGENT_CONTEXT_COMPACTION_TRIGGER_PERCENT")
	os.Unsetenv("AAGENT_CONTEXT_COMPACTION_PROMPT")

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

func TestLoopRetriesEmptyFinalResponseWithoutPromotingToolOutput(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("review the HTML")
	sess.AddAssistantMessage("", []session.ToolCall{
		{ID: "tc-read", Name: "read", Input: []byte(`{"path":"page.html"}`)},
	})
	sess.AddToolResult([]session.ToolResult{
		{ToolCallID: "tc-read", Name: "read", Content: "<div>raw html from tool</div>"},
	})

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{Content: "", Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 1}},
			{Content: "Reviewed the page and found layout issues.", Usage: llm.TokenUsage{InputTokens: 120, OutputTokens: 8}},
		},
	}
	ag := New(Config{MaxSteps: 5, ContextWindow: 1000}, mockLLM, tools.NewManager(t.TempDir()), sm)

	content, _, err := ag.RunWithEvents(context.Background(), sess, "review the HTML", nil)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if content != "Reviewed the page and found layout issues." {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(mockLLM.CapturedRequests) != 2 {
		t.Fatalf("expected retry request, got %d requests", len(mockLLM.CapturedRequests))
	}
	second := mockLLM.CapturedRequests[1]
	if len(second.Messages) == 0 {
		t.Fatal("expected messages in retry request")
	}
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "previous model response was empty") {
		t.Fatalf("expected transient retry prompt as last message, got role=%q content=%q", last.Role, last.Content)
	}
	if sess.Status != session.StatusCompleted {
		t.Fatalf("expected completed session, got %s", sess.Status)
	}
	lastMsg := sess.Messages[len(sess.Messages)-1]
	if lastMsg.Role != "assistant" || strings.Contains(lastMsg.Content, "raw html from tool") {
		t.Fatalf("expected final assistant content without raw tool output, got role=%q content=%q", lastMsg.Role, lastMsg.Content)
	}
	if got := metadataFloat(sess.Metadata, metadataContextWindow); got != 1000 {
		t.Fatalf("expected context window metadata, got %v", got)
	}
}

func TestLoopStoresLLMTimingMetadataOnAssistantMessage(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("hello")

	mockLLM := &MockLLM{
		Response: &llm.ChatResponse{Content: "hi there"},
	}
	ag := New(Config{Provider: "test-provider", Model: "test-model", MaxSteps: 3}, mockLLM, tools.NewManager(t.TempDir()), sm)

	_, _, err = ag.RunWithEvents(context.Background(), sess, "hello", nil)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	lastMsg := sess.Messages[len(sess.Messages)-1]
	if lastMsg.Role != "assistant" {
		t.Fatalf("expected assistant message, got %s", lastMsg.Role)
	}
	if lastMsg.Metadata == nil {
		t.Fatal("expected assistant message metadata")
	}
	if _, ok := lastMsg.Metadata["llm_duration_ms"].(int64); !ok {
		t.Fatalf("expected int64 llm_duration_ms metadata, got %#v", lastMsg.Metadata["llm_duration_ms"])
	}
	if got := lastMsg.Metadata["llm_provider"]; got != "test-provider" {
		t.Fatalf("expected llm_provider metadata, got %#v", got)
	}
	if got := lastMsg.Metadata["llm_model"]; got != "test-model" {
		t.Fatalf("expected llm_model metadata, got %#v", got)
	}
	if got := lastMsg.Metadata["llm_started_at"]; got == "" {
		t.Fatalf("expected llm_started_at metadata, got %#v", got)
	}
	if got := lastMsg.Metadata["llm_completed_at"]; got == "" {
		t.Fatalf("expected llm_completed_at metadata, got %#v", got)
	}
}

func TestLoopFailsAfterRepeatedEmptyFinalResponses(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("review the HTML")
	sess.AddAssistantMessage("", []session.ToolCall{
		{ID: "tc-read", Name: "read", Input: []byte(`{"path":"page.html"}`)},
	})
	sess.AddToolResult([]session.ToolResult{
		{ToolCallID: "tc-read", Name: "read", Content: "<div>raw html from tool</div>"},
	})

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{Content: "", Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 1}},
			{Content: "", Usage: llm.TokenUsage{InputTokens: 120, OutputTokens: 1}},
		},
	}
	ag := New(Config{MaxSteps: 5}, mockLLM, tools.NewManager(t.TempDir()), sm)

	_, _, err = ag.RunWithEvents(context.Background(), sess, "review the HTML", nil)
	if err == nil {
		t.Fatal("expected empty final response error")
	}
	if sess.Status != session.StatusFailed {
		t.Fatalf("expected failed session, got %s", sess.Status)
	}
	lastMsg := sess.Messages[len(sess.Messages)-1]
	if lastMsg.Role != "assistant" || !strings.Contains(lastMsg.Content, "empty final response") {
		t.Fatalf("expected explicit failure assistant message, got role=%q content=%q", lastMsg.Role, lastMsg.Content)
	}
	if strings.Contains(lastMsg.Content, "raw html from tool") {
		t.Fatalf("failure message should not include raw tool output: %q", lastMsg.Content)
	}
}

type loopNoopTool struct{}

func (loopNoopTool) Name() string { return "noop" }

func (loopNoopTool) Description() string { return "No-op test tool" }

func (loopNoopTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (loopNoopTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true, Output: "ok"}, nil
}

type loopProgressTool struct{}

func (loopProgressTool) Name() string { return "progress_test" }

func (loopProgressTool) Description() string { return "Progress test tool" }

func (loopProgressTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (loopProgressTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	tools.ReportProgress(ctx, tools.ProgressEvent{
		ToolName: "progress_test",
		Status:   "child_session_created",
		Content:  "child session ready",
		Metadata: map[string]interface{}{
			"child_session_id": "child-123",
		},
	})
	return &tools.Result{Success: true, Output: "ok"}, nil
}

func TestLoopEmitsToolProgressEvents(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("run progress tool")

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{ID: "call-progress", Name: "progress_test", Input: `{}`}}},
			{Content: "done"},
		},
	}
	manager := tools.NewManager(t.TempDir())
	manager.Register(loopProgressTool{})
	ag := New(Config{MaxSteps: 3}, mockLLM, manager, sm)

	var events []Event
	content, _, err := ag.RunWithEvents(context.Background(), sess, "run progress tool", func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("RunWithEvents returned error: %v", err)
	}
	if content != "done" {
		t.Fatalf("content = %q, want done", content)
	}

	for _, event := range events {
		if event.Type != EventToolProgress || event.ToolProgress == nil {
			continue
		}
		if event.ToolProgress.ToolCallID != "call-progress" {
			t.Fatalf("progress tool_call_id = %q, want call-progress", event.ToolProgress.ToolCallID)
		}
		if event.ToolProgress.Status != "child_session_created" {
			t.Fatalf("progress status = %q, want child_session_created", event.ToolProgress.Status)
		}
		if got := event.ToolProgress.Metadata["child_session_id"]; got != "child-123" {
			t.Fatalf("progress child_session_id = %#v, want child-123", got)
		}
		return
	}
	t.Fatalf("expected %s event, got %#v", EventToolProgress, events)
}

func TestRecordPendingToolProgressMetadata(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddAssistantMessage("", []session.ToolCall{
		{
			ID:    "call-parallel",
			Name:  "parallel",
			Input: json.RawMessage(`{"steps":[{"tool":"delegate_to_agent","args":{"agent_id":"reviewer","task":"review"}}]}`),
		},
	})

	ok := recordPendingToolProgress(sess, tools.ProgressEvent{
		ToolCallID: "call-parallel",
		ToolName:   "delegate_to_agent",
		Status:     "child_session_created",
		Content:    "Docker sub-agent session created. Waiting for output stream.",
		Metadata: map[string]interface{}{
			"child_session_id": "child-reviewer",
			"agent_name":       "reviewer",
			"parallel_step":    1,
			"parallel_tool":    "delegate_to_agent",
		},
	})
	if !ok {
		t.Fatal("expected pending progress to be recorded")
	}

	raw := sess.Messages[len(sess.Messages)-1].Metadata[messageMetadataPendingToolResults]
	results, ok := raw.([]session.ToolResult)
	if !ok || len(results) != 1 {
		t.Fatalf("pending results = %#v, want one session.ToolResult", raw)
	}
	result := results[0]
	if result.ToolCallID != "call-parallel" || result.Name != "delegate_to_agent" {
		t.Fatalf("unexpected pending result identity: %#v", result)
	}
	if result.Metadata["child_session_id"] != "child-reviewer" {
		t.Fatalf("child session metadata not preserved: %#v", result.Metadata)
	}
	progressMap, ok := result.Metadata[toolMetadataParallelStepProgress].(map[string]interface{})
	if !ok {
		t.Fatalf("parallel step progress missing: %#v", result.Metadata)
	}
	step, ok := progressMap["1"].(map[string]interface{})
	if !ok {
		t.Fatalf("step 1 progress missing: %#v", progressMap)
	}
	stepMetadata, ok := step["metadata"].(map[string]interface{})
	if !ok || stepMetadata["child_session_id"] != "child-reviewer" {
		t.Fatalf("step child session metadata not preserved: %#v", step)
	}

	if !clearPendingToolProgressMetadata(sess) {
		t.Fatal("expected pending progress metadata to be cleared")
	}
	if _, exists := sess.Messages[len(sess.Messages)-1].Metadata[messageMetadataPendingToolResults]; exists {
		t.Fatalf("pending metadata still present: %#v", sess.Messages[len(sess.Messages)-1].Metadata)
	}
}

func TestLoopFailsWhenMaxStepsReachedWithoutFinalAssistantContent(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("keep calling a tool")

	mockLLM := &MockLLM{
		Response: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "call-noop", Name: "noop", Input: `{}`}}},
	}
	manager := tools.NewManager(t.TempDir())
	manager.Register(loopNoopTool{})
	ag := New(Config{MaxSteps: 2}, mockLLM, manager, sm)

	content, _, err := ag.RunWithEvents(context.Background(), sess, "keep calling a tool", nil)
	if err == nil {
		t.Fatalf("expected max steps error, got content=%q", content)
	}
	if !strings.Contains(err.Error(), "maximum step limit") {
		t.Fatalf("expected max steps error, got %v", err)
	}
	if sess.Status != session.StatusFailed {
		t.Fatalf("expected failed session, got %s", sess.Status)
	}
	lastMsg := sess.Messages[len(sess.Messages)-1]
	if lastMsg.Role != "assistant" || !strings.Contains(lastMsg.Content, "maximum step limit") {
		t.Fatalf("expected explicit max-step failure assistant message, got role=%q content=%q", lastMsg.Role, lastMsg.Content)
	}
}

func TestLoopFinalizesWhenMaxStepsReached(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("keep calling a tool")

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{ID: "call-noop-1", Name: "noop", Input: `{}`}}},
			{ToolCalls: []llm.ToolCall{{ID: "call-noop-2", Name: "noop", Input: `{}`}}},
			{Content: "Final summary", Usage: llm.TokenUsage{InputTokens: 120, OutputTokens: 12}},
		},
	}
	manager := tools.NewManager(t.TempDir())
	manager.Register(loopNoopTool{})
	ag := New(Config{MaxSteps: 2}, mockLLM, manager, sm)

	content, _, err := ag.RunWithEvents(context.Background(), sess, "keep calling a tool", nil)
	if err != nil {
		t.Fatalf("expected max-step finalization to succeed, got %v", err)
	}
	if content != "Final summary" {
		t.Fatalf("content = %q, want Final summary", content)
	}
	if sess.Status != session.StatusCompleted {
		t.Fatalf("expected completed session, got %s", sess.Status)
	}
	if len(mockLLM.CapturedRequests) != 3 {
		t.Fatalf("captured request count = %d, want 3", len(mockLLM.CapturedRequests))
	}
	finalRequest := mockLLM.CapturedRequests[2]
	if len(finalRequest.Tools) != 0 {
		t.Fatalf("finalization request exposed %d tools, want none", len(finalRequest.Tools))
	}
	if len(finalRequest.Messages) == 0 || !strings.Contains(finalRequest.Messages[len(finalRequest.Messages)-1].Content, "Produce the final response now without calling tools") {
		t.Fatalf("finalization prompt missing from final request: %#v", finalRequest.Messages)
	}
	lastMsg := sess.Messages[len(sess.Messages)-1]
	if lastMsg.Role != "assistant" || lastMsg.Content != "Final summary" {
		t.Fatalf("expected final assistant summary, got role=%q content=%q", lastMsg.Role, lastMsg.Content)
	}
	if got := lastMsg.Metadata["max_steps_exceeded"]; got != true {
		t.Fatalf("expected max-step metadata on final summary, got %#v", lastMsg.Metadata)
	}
	if got := lastMsg.Metadata["finalized_after_step_limit"]; got != true {
		t.Fatalf("expected finalization metadata on final summary, got %#v", lastMsg.Metadata)
	}
}
