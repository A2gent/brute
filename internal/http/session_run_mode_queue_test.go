package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestSerialQueueGrantsEscalatedWriteAccess(t *testing.T) {
	var granted atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
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
	sess, err := s.sessionManager.CreateQueued("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata["provider"] = "openai"
	sess.Metadata["model"] = "test-model"
	sess.Metadata[sessionQueueModeMetadataKey] = "serial"
	sess.Metadata[sessionQueueAutoStartKey] = true
	sess.Metadata[session.RunModeReadOnlyKey] = true
	sess.Metadata[session.RunModeWriteRequestedKey] = true
	sess.AddUserMessage("Continue after requesting writes")
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
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
	if !s.runSerialQueuedSession(context.Background(), sess.ID) {
		t.Fatal("queue did not advance")
	}
	fresh, err = s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.IsAutoReadOnly() || fresh.WriteRequested() || !granted.Load() || fresh.Status != session.StatusCompleted {
		t.Fatalf("write grant failed: status=%s metadata=%v granted=%v", fresh.Status, fresh.Metadata, granted.Load())
	}
}
