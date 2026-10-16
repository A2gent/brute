package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestSessionCatalogToolNotificationDeliveryAndReplay(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: "project-1", Name: "Demo", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	projectID := "project-1"
	sess.ProjectID = &projectID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	hub := server.catalogHub()
	hub.mu.Lock()
	cursor := hub.cursorLocked()
	hub.mu.Unlock()
	sub := hub.subscribe(projectID, cursor, true)
	defer hub.unsubscribe(sub)
	readCatalogEvent(t, sub.events) // catalog_ready

	payload := map[string]interface{}{
		"title": "Progress", "message": "Background work is still running", "level": "success",
		"image_path": "/tmp/image.png", "image_url": "https://example.com/image.png",
		"audio_clip_id": "clip-1", "auto_play_audio": false,
		"unexpected": "private tool data",
	}
	message := &MessageResponse{Timestamp: now, ToolResults: []ToolResultResponse{
		{ToolCallID: "ordinary", Content: "large transcript content"},
		{ToolCallID: "failed", IsError: true, Metadata: map[string]interface{}{"webapp_notification": payload}},
		{ToolCallID: "notify-1", Content: strings.Repeat("large tool output", 10000), Metadata: map[string]interface{}{
			"webapp_notification": payload, "other_metadata": "not for catalog",
		}},
	}}
	server.publishSessionEvent(sess.ID, ChatStreamEvent{Type: "tool_completed", Message: message})
	got := readCatalogEvent(t, sub.events)
	if got.Type != "webapp_notification" || got.SessionID != sess.ID || got.ProjectID != projectID || got.Session != nil {
		t.Fatalf("unexpected notification event: %+v", got)
	}
	notification := got.Notification
	if notification == nil || notification.ID != sess.ID+":notify-1" || notification.CreatedAt != now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("unstable notification identity: %+v", notification)
	}
	if notification.ImagePath != "/tmp/image.png" || notification.AudioClipID != "clip-1" || notification.AutoPlayAudio == nil || *notification.AutoPlayAudio {
		t.Fatalf("missing media payload: %+v", notification)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 || strings.Contains(string(encoded), "private tool data") || strings.Contains(string(encoded), "large tool output") {
		t.Fatalf("catalog event leaked transcript or unrelated metadata: %s", encoded)
	}
	// Mutating the source after publication must not change replayed payloads.
	payload["message"] = "mutated"
	replay := hub.subscribe(projectID, cursor, true)
	defer hub.unsubscribe(replay)
	replayed := readCatalogEvent(t, replay.events)
	if replayed.ID != got.ID || replayed.Notification.ID != notification.ID || replayed.Notification.Message != notification.Message {
		t.Fatalf("replay differs from live delivery: %+v", replayed)
	}
	if ready := readCatalogEvent(t, replay.events); ready.Type != "catalog_ready" {
		t.Fatalf("unexpected event after notification: %+v", ready)
	}

	otherProject := hub.subscribe("different-project", cursor, true)
	defer hub.unsubscribe(otherProject)
	if event := readCatalogEvent(t, otherProject.events); event.Type != "catalog_ready" {
		t.Fatalf("notification leaked to another project: %+v", event)
	}
	// Re-publication with a changed message timestamp retains notification identity.
	message.Timestamp = now.Add(time.Second)
	server.publishSessionEvent(sess.ID, ChatStreamEvent{Type: "tool_completed", Message: message})
	if duplicate := readCatalogEvent(t, sub.events); duplicate.Notification.ID != notification.ID || duplicate.ID == got.ID {
		t.Fatalf("notification identity must be separate from stream cursor: %+v", duplicate)
	}
}

func TestSessionCatalogIgnoresNonNotificationToolResults(t *testing.T) {
	for _, kind := range []string{"nested", "failed", "empty", "runtime", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			server, _ := newUnifiedAgentsTestServer(t)
			sess, err := server.sessionManager.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			result := ToolResultResponse{ToolCallID: "notify-1", Metadata: map[string]interface{}{
				"webapp_notification": map[string]interface{}{"message": "Progress"},
			}}
			event := ChatStreamEvent{Type: "tool_completed", Message: &MessageResponse{ToolResults: []ToolResultResponse{result}}}
			switch kind {
			case "nested":
				parent, err := server.sessionManager.Create("build")
				if err != nil {
					t.Fatal(err)
				}
				sess.ParentID = &parent.ID
				if err := server.sessionManager.Save(sess); err != nil {
					t.Fatal(err)
				}
			case "failed":
				event.Message.ToolResults[0].IsError = true
			case "empty":
				result.Metadata["webapp_notification"] = map[string]interface{}{"message": "  "}
			case "runtime":
				event.Message = nil
			case "ordinary":
				result.Metadata = nil
				event.Message.ToolResults[0] = result
			}
			hub := server.catalogHub()
			hub.mu.Lock()
			before := hub.seq
			hub.mu.Unlock()
			server.publishSessionEvent(sess.ID, event)
			hub.mu.Lock()
			defer hub.mu.Unlock()
			if hub.seq != before {
				t.Fatalf("unexpected catalog event for %s", kind)
			}
		})
	}
}
