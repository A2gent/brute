package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestSerialQueueGrantsEscalatedWriteAccess(t *testing.T) {
	var granted atomic.Bool
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"escalating\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"escalate\",\"name\":\"request_write\",\"arguments\":\"{}\"}]}}\n\ndata: [DONE]\n\n")
			return
		}
		if len(request.Input) == 0 || request.Input[len(request.Input)-1].Role != "user" {
			t.Errorf("first request after write grant must end with a user turn: %+v", request.Input)
		} else if last := request.Input[len(request.Input)-1]; len(last.Content) != 1 || last.Content[0].Text != "Write access granted. Continue the task." {
			t.Errorf("missing transient write-grant prompt: %+v", last)
		}
		for _, item := range request.Input {
			for _, content := range item.Content {
				if strings.Contains(content.Text, "The session is now in the project serial queue") {
					t.Error("UI escalation notice leaked into provider request")
				}
			}
		}
		for _, tool := range request.Tools {
			if tool.Name == "write" {
				granted.Store(true)
			}
			if tool.Name == "request_write" {
				t.Error("escalation tool still exposed after write grant")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"done\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	s, _, _ := runModeTestServer(t, 500, `{}`)
	s.config.ActiveProvider = "openai"
	s.config.LLMRetries = 1
	s.config.Providers["openai"] = config.Provider{APIKey: "test-key", BaseURL: provider.URL + "/v1", Model: "test-model"}
	s.toolManager = tools.NewManager(s.config.WorkDir)
	sess, err := s.sessionManager.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata["provider"] = "openai"
	sess.Metadata["model"] = "test-model"
	sess.Metadata[session.RunModeReadOnlyKey] = true
	sess.AddUserMessage("Analyze this and update it if needed")
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	// Build the actual escalation transcript through the agent without adding
	// a user message between the tool result and the serial queue resume.
	if _, err := s.runSessionWithoutStreaming(context.Background(), sess, "Analyze this and update it if needed"); err != nil {
		t.Fatal(err)
	}
	if sess.Status != session.StatusQueued || !sess.WriteRequested() || len(sess.Messages) != 3 || len(sess.Messages[1].ToolCalls) != 1 || sess.Messages[1].ToolCalls[0].Name != "request_write" || len(sess.Messages[2].ToolResults) != 1 {
		t.Fatalf("invalid escalation fixture: %+v", sess)
	}
	// An existing run lease must not lift the guard or start another run.
	_, release, err := s.sessionManager.AcquireRun(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.runSerialQueuedSession(context.Background(), sess.ID)
	fresh, _ := s.sessionManager.Get(sess.ID)
	if !fresh.IsAutoReadOnly() || !fresh.WriteRequested() || granted.Load() {
		t.Fatal("write grant bypassed the existing lease")
	}
	release()
	if cont, _ := s.runSerialQueuedSession(context.Background(), sess.ID); !cont {
		t.Fatal("queue did not advance")
	}
	fresh, err = s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.IsAutoReadOnly() || fresh.WriteRequested() || !granted.Load() || fresh.Status != session.StatusCompleted {
		t.Fatalf("write grant failed: status=%s metadata=%v granted=%v", fresh.Status, fresh.Metadata, granted.Load())
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want escalation then resume", requests.Load())
	}
	for _, msg := range fresh.Messages {
		if msg.Role == "user" && msg.Content == "Write access granted. Continue the task." {
			t.Fatal("transient grant prompt was persisted in the transcript")
		}
	}
}
