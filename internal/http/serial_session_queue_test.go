package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestSerialQueuedProjectSessionsRunOneAtATime(t *testing.T) {
	var mu sync.Mutex
	activeRequests := 0
	maxActiveRequests := 0
	totalRequests := 0

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/chat/completions"; got != want {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}

		mu.Lock()
		activeRequests++
		totalRequests++
		callNumber := totalRequests
		if activeRequests > maxActiveRequests {
			maxActiveRequests = activeRequests
		}
		mu.Unlock()

		defer func() {
			mu.Lock()
			activeRequests--
			mu.Unlock()
		}()

		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done %d\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n", callNumber)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-serial-queue",
		Name:      "Serial Queue",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
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
	api := httptest.NewServer(server.router)
	defer api.Close()

	createQueued := func(task string) string {
		t.Helper()
		payload := map[string]interface{}{
			"agent_id":   "build",
			"task":       task,
			"project_id": project.ID,
			"queued":     true,
			"queue_mode": "serial",
		}
		body, _ := json.Marshal(payload)
		resp, err := api.Client().Post(api.URL+"/sessions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create queued session status = %d, want 201", resp.StatusCode)
		}
		var created CreateSessionResponse
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return created.ID
	}

	firstID := createQueued("first queued task")
	secondID := createQueued("second queued task")

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		first, firstErr := sessionManager.Get(firstID)
		second, secondErr := sessionManager.Get(secondID)
		if firstErr == nil && secondErr == nil && first.Status == session.StatusCompleted && second.Status == session.StatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	first, err := sessionManager.Get(firstID)
	if err != nil {
		t.Fatalf("load first session: %v", err)
	}
	second, err := sessionManager.Get(secondID)
	if err != nil {
		t.Fatalf("load second session: %v", err)
	}
	if first.Status != session.StatusCompleted || second.Status != session.StatusCompleted {
		t.Fatalf("queued sessions did not complete: first=%s second=%s", first.Status, second.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if totalRequests != 2 {
		t.Fatalf("provider requests = %d, want 2", totalRequests)
	}
	if maxActiveRequests != 1 {
		t.Fatalf("max concurrent provider requests = %d, want 1", maxActiveRequests)
	}
}

func TestStartSessionManuallyOverridesSerialQueueAutoStart(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-manual-start",
		Name:      "Manual Start",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	projectID := project.ID
	sess.ProjectID = &projectID
	sess.Metadata = map[string]interface{}{
		sessionQueueModeMetadataKey: sessionQueueModeSerial,
		sessionQueueAutoStartKey:    true,
	}
	sess.AddUserMessage("Start me manually")
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/start", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if fresh.Status != session.StatusQueued {
		t.Fatalf("session status = %s, want %s", fresh.Status, session.StatusQueued)
	}
	if sessionIsSerialQueuedAutoRun(fresh) {
		t.Fatalf("manual start should clear serial queue auto-start metadata")
	}
}

func TestSerialQueueCanAdvanceAfterInactiveStatuses(t *testing.T) {
	advanceStatuses := []session.Status{
		session.StatusCompleted,
		session.StatusFailed,
		session.StatusPaused,
		session.StatusInputRequired,
		session.StatusWaitingExternal,
	}
	for _, status := range advanceStatuses {
		t.Run(string(status), func(t *testing.T) {
			if !serialQueueCanAdvanceAfterStatus(status) {
				t.Fatalf("serial queue should advance after %s", status)
			}
		})
	}

	blockingStatuses := []session.Status{
		session.StatusQueued,
		session.StatusRunning,
	}
	for _, status := range blockingStatuses {
		t.Run(string(status), func(t *testing.T) {
			if serialQueueCanAdvanceAfterStatus(status) {
				t.Fatalf("serial queue should not advance after %s", status)
			}
		})
	}
}
