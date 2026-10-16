package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestSessionPromptEdit(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	manager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), manager, store, speechcache.New(0), 0)
	sess, err := manager.CreateQueued("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("original")
	setSessionQueuePaused(sess, true)
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	request := func(action, token, content string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"action": action, "token": token, "content": content})
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/prompt-edit", bytes.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("%s status=%d body=%s", action, rec.Code, rec.Body.String())
		}
	}
	request("begin", "owner", "", 200)
	fresh, _ := manager.Get(sess.ID)
	if fresh.Metadata[promptEditTokenKey] == "owner" {
		t.Fatal("raw lease token persisted")
	}
	server.pendingDrainMu.Lock()
	if server.startPendingRunLocked(sess.ID, &session.PendingMessage{Content: "steer"}) {
		t.Fatal("steer started during edit")
	}
	if !sessionPromptEditing(fresh) {
		t.Fatal("missing edit lease")
	}
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/start", nil))
	if rec.Code != 409 {
		t.Fatalf("start while editing: %d", rec.Code)
	}
	request("begin", "other", "", 409)
	request("renew", "owner", "", 200)
	request("save", "owner", " ", 400)
	request("cancel", "owner", "", 200)
	fresh, _ = manager.Get(sess.ID)
	if fresh.Messages[0].Content != "original" || !sessionIsQueuePaused(fresh) || sessionPromptEditing(fresh) {
		t.Fatal("cancel changed original prompt/pause")
	}
	request("begin", "owner", "", 200)
	request("save", "owner", "updated", 200)
	fresh, _ = manager.Get(sess.ID)
	if fresh.Messages[0].Content != "updated" || !sessionIsQueuePaused(fresh) || sessionPromptEditing(fresh) {
		t.Fatal("save did not preserve original pause")
	}
	request("begin", "owner", "", 200)
	fresh, _ = manager.Get(sess.ID)
	fresh.Metadata[promptEditExpiryKey] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	manager.Save(fresh)
	request("save", "owner", "stale", 409)
	request("begin", "new-owner", "", 200)
	request("cancel", "new-owner", "", 200)
	_, release, err := manager.AcquireRun(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	request("begin", "owner", "", 409)
	release()
	fresh, _ = manager.Get(sess.ID)
	fresh.SetStatus(session.StatusRunning)
	manager.Save(fresh)
	request("begin", "owner", "", 409)
	fresh.SetStatus(session.StatusQueued)
	setSessionQueuePaused(fresh, false)
	fresh.Metadata[sessionQueueModeMetadataKey] = sessionQueueModeSerial
	fresh.Metadata[sessionQueueAutoStartKey] = true
	manager.Save(fresh)
	request("begin", "last-owner", "", 200)
	if next, err := server.nextSerialQueuedSession(serialQueueGlobalScope); err != nil || next != nil {
		t.Fatal("editing session remained runnable")
	}
	fresh, _ = manager.Get(sess.ID)
	fresh.Metadata[promptEditExpiryKey] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	manager.Save(fresh)
	if next, err := server.nextSerialQueuedSession(serialQueueGlobalScope); err != nil || next == nil || next.ID != sess.ID {
		t.Fatal("expired editor did not restore queue eligibility")
	}
}
