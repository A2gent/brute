package http

import (
	"context"
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

func TestReloadReadinessReflectsRunsAndQueueWorkers(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	server := NewServer(&config.Config{}, nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	read := func() reloadReadinessResponse {
		rec := httptest.NewRecorder()
		server.handleReloadReadiness(rec, httptest.NewRequest(http.MethodGet, "/system/reload-readiness", nil))
		var resp reloadReadinessResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	if got := read(); !got.Idle {
		t.Fatalf("fresh server should be idle: %+v", got)
	}

	_, release, err := server.sessionManager.AcquireRun(context.Background(), "s1")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := read(); got.Idle || got.ActiveRuns != 1 {
		t.Fatalf("active run should block reload: %+v", got)
	}
	release()

	// A live queue worker means queued sessions are about to start.
	server.serialQueueMu.Lock()
	server.serialQueueWorkers = map[string]struct{}{serialQueueGlobalScope: {}}
	server.serialQueueMu.Unlock()
	if got := read(); got.Idle || got.ActiveQueueWorkers != 1 {
		t.Fatalf("queue worker should block reload: %+v", got)
	}

	server.clearSerialSessionQueueWorker(serialQueueGlobalScope)
	if got := read(); !got.Idle {
		t.Fatalf("server should be idle again: %+v", got)
	}
}
