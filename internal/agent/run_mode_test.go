package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestAutoReadOnlyToolDefinitions(t *testing.T) {
	ag := newSelectionAgent(t, nil)
	for _, name := range []string{"git_commit", "git_push", "bitbucket_create_pull_request", "telegram_send_message"} {
		ag.toolManager.Register(namedTool{name})
	}
	sess := session.New("test")
	sess.Metadata[session.RunModeReadOnlyKey] = true
	// Group expansion, including unknown MCP tools, must not broaden write permissions.
	sess.Metadata[metadataToolGroups] = tools.ToolGroupNames()
	names := toolNames(ag, sess)
	for _, name := range []string{"read", "grep", "parallel", "pipeline", requestWriteToolName} {
		if !names[name] {
			t.Errorf("read-only tool missing: %s", name)
		}
	}
	for _, name := range []string{"edit", "write", "replace_lines", "insert_lines", "bash", "code_execution", "git_commit", "git_push", "telegram_send_message", "bitbucket_create_pull_request", "custom_mcp_tool", tools.EnableToolsToolName} {
		if names[name] {
			t.Errorf("unsafe tool exposed: %s", name)
		}
	}
	delete(sess.Metadata, session.RunModeReadOnlyKey)
	if names := toolNames(ag, sess); names[requestWriteToolName] || !names["write"] || !names["bash"] {
		t.Fatalf("manual tools changed: %v", names)
	}
}

func TestReadOnlyExecutionGuardBlocksHiddenAndNestedWrites(t *testing.T) {
	manager := tools.NewManager(t.TempDir())
	manager.Register(requestWriteTool{})
	var requested atomic.Bool
	ctx := readOnlyExecutionContext(context.Background(), &requested)
	for _, call := range []struct{ name, input string }{
		{"write", `{"path":"unsafe","content":"oops"}`},
		{"functions.write", `{"path":"unsafe","content":"oops"}`},
		{"parallel", `{"steps":[{"tool":"functions.write","args":{"path":"unsafe","content":"oops"}}]}`},
		{"pipeline", `{"steps":[{"tool":"write","args":{"path":"unsafe","content":"oops"}}]}`},
	} {
		result, err := manager.Execute(ctx, call.name, json.RawMessage(call.input))
		if err == nil && result != nil && result.Success && !strings.Contains(result.Output, "request_write") {
			t.Fatalf("unsafe execution accepted: %s %+v", call.name, result)
		}
	}
	if _, err := manager.Execute(ctx, requestWriteToolName, json.RawMessage(`{}`)); err != nil || !requested.Load() {
		t.Fatalf("write request failed: %v", err)
	}
}

func TestAutoReadOnlyRequestsSerialEscalation(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	projectID := "project-one"
	sess.ProjectID = &projectID
	sess.Metadata[session.RunModeReadOnlyKey] = true
	sess.Metadata[session.RunModeDecisionKey] = session.RunModeDecision{Label: "read_only", Confidence: .99, Source: "jev"}
	sess.AddUserMessage("Analyze this")
	mock := &mockStreamingLLM{response: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "escalate", Name: requestWriteToolName, Input: `{}`}}}}
	ag := New(Config{MaxSteps: 3}, mock, tools.NewManager(t.TempDir()), sm)
	if _, _, err := ag.Run(context.Background(), sess, "Analyze this"); err != nil {
		t.Fatal(err)
	}
	fresh, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != session.StatusQueued || !fresh.WriteRequested() || !fresh.IsAutoReadOnly() || fresh.Metadata["queue_mode"] != "serial" || fresh.Metadata["queue_auto_start"] != true || *fresh.ProjectID != projectID {
		t.Fatalf("escalation not queued safely: %+v", fresh)
	}
	for _, message := range fresh.Messages {
		if message.Metadata["run_mode_notice"] == true {
			t.Fatal("queue escalation should not add a synthetic assistant notice")
		}
	}
	request := ag.buildRequest(context.Background(), fresh)
	if last := request.Messages[len(request.Messages)-1]; last.Role != "tool" || len(last.ToolResults) != 1 {
		t.Fatalf("UI notice leaked into model transcript: %+v", last)
	}
}

func TestAutoReadOnlyNativeProvidersQueue(t *testing.T) {
	for _, provider := range []string{"anthropic", "anthropic:custom", "kimi_cli", "cursor", "fallback_chain"} {
		t.Run(provider, func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("test")
			if err != nil {
				t.Fatal(err)
			}
			sess.Metadata[session.RunModeReadOnlyKey] = true
			sess.AddUserMessage("Analyze")
			ag := New(Config{Provider: provider}, nil, tools.NewManager(t.TempDir()), sm)
			if _, _, err := ag.Run(context.Background(), sess, "Analyze"); err != nil {
				t.Fatal(err)
			}
			if sess.Status != session.StatusQueued || !sess.WriteRequested() {
				t.Fatal("unguarded native provider was not queued")
			}
		})
	}
}

func TestAutoReadOnlyQueuesBeforeNativeTransitionCompaction(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata[session.RunModeReadOnlyKey] = true
	sess.AddUserMessage("Analyze")
	transition, err := session.NewProviderTransition("native-transition", session.ProviderEndpoint{Provider: "openai", Model: "source"}, session.ProviderEndpoint{Provider: "anthropic", Model: "native"}, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	session.PutProviderTransition(sess, transition)
	// A nil resolver/client ensures no transition compaction or native request is invoked.
	ag := New(Config{Provider: "openai"}, nil, tools.NewManager(t.TempDir()), sm)
	if _, _, err := ag.Run(context.Background(), sess, "Analyze"); err != nil {
		t.Fatal(err)
	}
	if sess.Status != session.StatusQueued || !sess.WriteRequested() {
		t.Fatal("native transition was not queued")
	}
}

// Inject from inside request_write so the live DB changes after the loop saves
// its tool-call message but before it saves the escalation.
type noteDuringWriteRequestTool struct {
	requestWriteTool
	manager *session.Manager
	status  session.Status
}

func (t noteDuringWriteRequestTool) Execute(ctx context.Context, input json.RawMessage) (*tools.Result, error) {
	fresh, err := t.manager.Get(ctx.Value("session_id").(string))
	if err != nil {
		return nil, err
	}
	fresh.AddUserMessageWithImagesAndMetadata("Keep the public API unchanged", nil, map[string]interface{}{"injected_during_run": true})
	fresh.TaskProgress = "- [x] Read the API"
	fresh.SetStatus(t.status)
	if err := t.manager.Save(fresh); err != nil {
		return nil, err
	}
	return t.requestWriteTool.Execute(ctx, input)
}

func TestAutoReadOnlyEscalationPreservesLiveState(t *testing.T) {
	for _, status := range []session.Status{session.StatusRunning, session.StatusInputRequired, session.StatusWaitingExternal} {
		t.Run(string(status), func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("test")
			if err != nil {
				t.Fatal(err)
			}
			sess.Metadata[session.RunModeReadOnlyKey] = true
			sess.AddUserMessage("Analyze this")
			mock := &MockLLM{Response: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "escalate", Name: requestWriteToolName, Input: `{}`}}}}
			manager := tools.NewManager(t.TempDir())
			ag := New(Config{MaxSteps: 3}, mock, manager, sm)
			manager.Register(noteDuringWriteRequestTool{manager: sm, status: status})
			if _, _, err := ag.Run(context.Background(), sess, "Analyze this"); err != nil {
				t.Fatal(err)
			}
			fresh, err := sm.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := status
			if status == session.StatusRunning {
				wantStatus = session.StatusQueued
			}
			if fresh.Status != wantStatus || fresh.TaskProgress != "- [x] Read the API" {
				t.Fatalf("live state overwritten: status=%s progress=%q", fresh.Status, fresh.TaskProgress)
			}
			for _, msg := range fresh.Messages {
				if msg.Role == "user" && msg.Content == "Keep the public API unchanged" && msg.Metadata["injected_during_run"] == true {
					return
				}
			}
			t.Fatal("user note injected during request_write was lost")
		})
	}
}
