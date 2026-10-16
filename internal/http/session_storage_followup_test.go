package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestUpdateMessageMetadataKeyPublishesCatalogEvent(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddAssistantMessage("in progress", nil)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents("")
	defer unsubscribe()
	updatedAt := time.Now().Add(time.Second)
	if err := server.sessionManager.SaveMessageMetadataKey(sess.ID, sess.Messages[0].ID, "pending_tool_results", "progress", updatedAt); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != "session_updated" || event.SessionID != sess.ID || event.Session == nil {
			t.Fatalf("missing catalog row after column-only commit: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("UpdateMessageMetadataKey did not produce a catalog event")
	}
	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil || !fresh.UpdatedAt.Equal(updatedAt) || fresh.Messages[0].Metadata["pending_tool_results"] != "progress" {
		t.Fatalf("event published before commit: %v %v", fresh, err)
	}
	if err := server.sessionManager.SaveMessageMetadataKey(sess.ID, "missing", "key", "value", time.Now()); err == nil {
		t.Fatal("missing message should fail")
	}
	select {
	case event := <-events:
		t.Fatalf("failed write produced event: %+v", event)
	default:
	}
}

func TestArchiveAnalyzedPublishesCatalogEvent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey, &now)
	sess.AddUserMessage("keep transcript")
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents("")
	defer unsubscribe()
	params, _ := json.Marshal(map[string]interface{}{"action": "mark_analyzed", "session_ids": []string{sess.ID}})
	result, err := tools.NewArchivedSessionsTool(store, server.sessionManager).Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("mark analyzed: %v %v", result, err)
	}
	select {
	case event := <-events:
		if event.Type != "session_updated" || event.SessionID != sess.ID {
			t.Fatalf("wrong event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("archive tool bypassed manager notifications")
	}
	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil || len(fresh.Messages) != 1 || storage.SessionMetadataTime(fresh.Metadata, storage.SessionArchiveAnalyzedAtKey) == nil {
		t.Fatalf("archive update lost: %v %v", fresh, err)
	}
}

func TestAppendProviderFailuresRetainsBoundedHistoryWithinRun(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		appendSessionProviderFailure(sess, &agent.ProviderTraceEvent{Attempt: i, Reason: strings.Repeat("upstream body", 1000)})
	}
	failures := sessionProviderFailures(sess.Metadata)
	if len(failures) != storage.MaxProviderFailures || failures[0].Attempt != 5 || failures[len(failures)-1].Attempt != 24 {
		t.Fatalf("lost recent failure history: %+v", failures)
	}
	for _, failure := range failures {
		if len(failure.Reason) > storage.MaxProviderFailureTextBytes {
			t.Fatal("error body not capped at append")
		}
	}
}
