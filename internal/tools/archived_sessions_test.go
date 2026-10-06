package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestArchivedSessionsToolListGetAndMarkAnalyzed(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	archivedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	save := func(id string, metadata map[string]interface{}) {
		t.Helper()
		now := time.Now()
		if err := store.SaveSession(&storage.Session{
			ID: id, AgentID: "build", Title: "Title " + id, Status: "completed", Metadata: metadata,
			CreatedAt: now, UpdatedAt: now,
			Messages: []storage.Message{{ID: id + "-m1", Role: "user", Content: "hello from " + id, Timestamp: now}},
		}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	save("pending", storage.SetSessionMetadataTime(nil, storage.SessionArchivedAtKey, &archivedAt))
	analyzed := storage.SetSessionMetadataTime(nil, storage.SessionArchivedAtKey, &archivedAt)
	save("analyzed", storage.SetSessionMetadataTime(analyzed, storage.SessionArchiveAnalyzedAtKey, &archivedAt))
	save("active", nil)

	tool := NewArchivedSessionsTool(store)
	run := func(params map[string]any) *Result {
		t.Helper()
		result, err := tool.Execute(context.Background(), mustJSON(t, params))
		if err != nil {
			t.Fatalf("execute %v: %v", params, err)
		}
		return result
	}

	list := run(map[string]any{"action": "list"})
	if !list.Success || !strings.Contains(list.Output, "pending") {
		t.Fatalf("expected pending session in list: %+v", list)
	}
	if strings.Contains(list.Output, "analyzed |") || strings.Contains(list.Output, "active") {
		t.Fatalf("list must skip analyzed and non-archived sessions: %s", list.Output)
	}

	if get := run(map[string]any{"action": "get", "session_id": "pending"}); !get.Success || !strings.Contains(get.Output, "hello from pending") {
		t.Fatalf("expected transcript: %+v", get)
	}
	if get := run(map[string]any{"action": "get", "session_id": "active"}); get.Success {
		t.Fatalf("get must reject non-archived sessions")
	}

	if mark := run(map[string]any{"action": "mark_analyzed", "session_ids": []string{"pending"}}); !mark.Success {
		t.Fatalf("mark failed: %+v", mark)
	}
	if list := run(map[string]any{"action": "list"}); !strings.Contains(list.Output, "No archived sessions pending") {
		t.Fatalf("expected empty pending list after marking: %s", list.Output)
	}
	reloaded, err := store.GetSession("pending")
	if err != nil || len(reloaded.Messages) != 1 {
		t.Fatalf("marking must keep transcript intact: %v %+v", err, reloaded)
	}
}
