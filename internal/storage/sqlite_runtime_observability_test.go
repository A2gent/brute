package storage

import (
	"testing"
	"time"
)

func TestRuntimeObservabilityMessageMetadataRoundtrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	sess := &Session{
		ID:      "runtime-session",
		AgentID: "test-agent",
		Title:   "Runtime metadata",
		Status:  "completed",
		Messages: []Message{
			{
				ID:      "msg-1",
				Role:    "assistant",
				Content: "done",
				Metadata: map[string]interface{}{
					"runtime_turn_id": "turn-abc",
					"runtime_native":  true,
					"runtime_cost": map[string]interface{}{
						"total_cost_usd": 0.01,
						"duration_ms":    int64(100),
					},
					"llm_duration_ms": int64(120),
				},
				Timestamp: now,
			},
		},
		Metadata:  map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveSession(sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	loaded, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(loaded.Messages))
	}
	msg := loaded.Messages[0]
	if msg.Metadata["runtime_turn_id"] != "turn-abc" {
		t.Fatalf("runtime_turn_id = %#v", msg.Metadata["runtime_turn_id"])
	}
	if msg.Metadata["runtime_native"] != true {
		t.Fatalf("runtime_native = %#v", msg.Metadata["runtime_native"])
	}
	cost, ok := msg.Metadata["runtime_cost"].(map[string]interface{})
	if !ok || cost["total_cost_usd"] != 0.01 {
		t.Fatalf("runtime_cost = %#v", msg.Metadata["runtime_cost"])
	}
	if msg.Metadata["llm_duration_ms"] == nil {
		t.Fatalf("timing metadata lost: %#v", msg.Metadata)
	}
}
