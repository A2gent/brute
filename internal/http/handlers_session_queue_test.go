package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestPauseAndResumeQueuedSessions(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	api := httptest.NewServer(server.router)
	defer api.Close()

	sess, err := sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	sess.AddUserMessage("wait in queue")
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}

	pauseBody, _ := json.Marshal(QueueSessionsRequest{SessionIDs: []string{sess.ID}})
	pauseResp, err := api.Client().Post(api.URL+"/sessions/queue/pause", "application/json", bytes.NewReader(pauseBody))
	if err != nil {
		t.Fatalf("pause queued session: %v", err)
	}
	defer pauseResp.Body.Close()
	if pauseResp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d, want %d", pauseResp.StatusCode, http.StatusOK)
	}
	var pauseResult QueueSessionsMutationResponse
	if err := json.NewDecoder(pauseResp.Body).Decode(&pauseResult); err != nil {
		t.Fatalf("decode pause response: %v", err)
	}
	if len(pauseResult.Updated) != 1 || pauseResult.Updated[0] != sess.ID {
		t.Fatalf("pause updated = %#v, want [%s]", pauseResult.Updated, sess.ID)
	}

	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if !sessionIsQueuePaused(fresh) {
		t.Fatalf("expected session to be queue-paused")
	}

	resumeBody, _ := json.Marshal(QueueSessionsRequest{SessionIDs: []string{sess.ID, "missing"}})
	resumeResp, err := api.Client().Post(api.URL+"/sessions/queue/resume", "application/json", bytes.NewReader(resumeBody))
	if err != nil {
		t.Fatalf("resume queued session: %v", err)
	}
	defer resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want %d", resumeResp.StatusCode, http.StatusOK)
	}
	var resumeResult QueueSessionsMutationResponse
	if err := json.NewDecoder(resumeResp.Body).Decode(&resumeResult); err != nil {
		t.Fatalf("decode resume response: %v", err)
	}
	if len(resumeResult.Updated) != 1 || resumeResult.Updated[0] != sess.ID {
		t.Fatalf("resume updated = %#v, want [%s]", resumeResult.Updated, sess.ID)
	}
	if len(resumeResult.Skipped) != 1 || resumeResult.Skipped[0] != "missing" {
		t.Fatalf("resume skipped = %#v, want [missing]", resumeResult.Skipped)
	}

	fresh, err = sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session after resume: %v", err)
	}
	if sessionIsQueuePaused(fresh) {
		t.Fatalf("expected session to be unpaused")
	}
}

func TestPauseQueuedSessionsRejectsEmptyPayload(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	api := httptest.NewServer(server.router)
	defer api.Close()

	body, _ := json.Marshal(QueueSessionsRequest{SessionIDs: []string{}})
	resp, err := api.Client().Post(api.URL+"/sessions/queue/pause", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("pause request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestStartQueuedSessionClearsQueuePause(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	sess.Metadata = map[string]interface{}{
		sessionQueuePausedKey: true,
	}
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
	if sessionIsQueuePaused(fresh) {
		t.Fatalf("manual start should clear queue pause metadata")
	}
}
