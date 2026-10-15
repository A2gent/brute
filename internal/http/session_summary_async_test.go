package http

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const summaryPromptMarker = "SUMMARY_PROMPT_MARKER"

func TestSessionSummaryOnlyLatestPassMayWrite(t *testing.T) {
	s := &Server{}
	older, _ := s.beginSessionSummary("sess")
	newer, _ := s.beginSessionSummary("sess")
	sess := &session.Session{ID: "sess", Summary: "Current summary"}
	s.preserveSessionSummary(sess, older, "Stale eager summary")
	if sess.Summary != "Current summary" {
		t.Fatal("stale eager summary must not be persisted or mutate the session")
	}
	if s.finishSessionSummary("sess", older, "stale summary", false) {
		t.Fatal("stale summary pass must not be allowed to write")
	}
	if s.finishSessionSummary("sess", older, "", true) || s.sessionSummarySkipCount != 0 {
		t.Fatal("stale skip must not count as a saved LLM call")
	}
	if !s.finishSessionSummary("sess", newer, "", false) {
		t.Fatal("latest summary pass must be allowed to write")
	}
	next, _ := s.beginSessionSummary("sess")
	if s.finishSessionSummary("sess", older, "", false) || !s.finishSessionSummary("sess", next, "", false) {
		t.Fatal("released token must not match a newer pass")
	}
}

// TestHandleChatStreamEmitsDoneBeforeSummaryCompletes guards against the summary
// LLM call delaying the final `done` event and serial queue advancement.
func TestHandleChatStreamEmitsDoneBeforeSummaryCompletes(t *testing.T) {
	releaseSummary := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSummary) }) }
	defer release()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		content := "Hello"
		if strings.Contains(string(body), summaryPromptMarker) {
			// Block the summary call until the test has observed `done`.
			select {
			case <-releaseSummary:
			case <-r.Context().Done():
				return
			}
			content = "Generated async summary"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]interface{}{
			"type": "response.completed",
			"response": map[string]interface{}{
				"id": "resp_summary", "status": "completed",
				"output": []map[string]interface{}{{"type": "message", "content": []map[string]string{{"type": "output_text", "text": content}}}},
				"usage":  map[string]int{"input_tokens": 3, "output_tokens": 1},
			},
		})
		_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveSettings(map[string]string{
		sessionSummaryPromptTemplateSettingKey: summaryPromptMarker + " {{latest_assistant_message}}",
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: provider.URL + "/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	defer server.sessionSummaryWG.Wait()
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	api := httptest.NewServer(server.router)
	defer api.Close()

	req, err := http.NewRequest(http.MethodPost, api.URL+"/sessions/"+sess.ID+"/chat/stream", bytes.NewBufferString(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	doneSeen := make(chan ChatStreamEvent, 1)
	go func() {
		resp, err := api.Client().Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			var event ChatStreamEvent
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "done" {
				doneSeen <- event
				return
			}
		}
	}()

	select {
	case event := <-doneSeen:
		if event.Status != string(session.StatusCompleted) {
			t.Fatalf("done status = %q, want completed", event.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("done event was not emitted while summary generation was blocked")
	}

	release()

	deadline := time.Now().Add(5 * time.Second)
	for {
		fresh, err := sessionManager.GetSummary(sess.ID)
		if err == nil && fresh.Summary == "Generated async summary" {
			if fresh.Status != session.StatusCompleted {
				t.Fatalf("status after summary = %q, want completed", fresh.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			got := ""
			if fresh != nil {
				got = fresh.Summary
			}
			t.Fatalf("summary was not persisted asynchronously, got %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
