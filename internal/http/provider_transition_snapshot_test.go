package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/session"
	"testing"
)

func TestSnapshotRestoresDurableProviderTimeline(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("test")
	sess.AddUserMessage("task")
	tr, _ := session.NewProviderTransition("ab", session.ProviderEndpoint{Provider: "a"}, session.ProviderEndpoint{Provider: "b"}, "manual", "confirm")
	session.PutProviderTransition(sess, tr)
	snapshot := server.sessionSnapshotStreamEvent(sess)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored ChatStreamEvent
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.ProviderTransitions) != 1 || restored.ProviderTransitions[0].Timeline.AfterMessageID != sess.Messages[0].ID {
		t.Fatalf("snapshot lost timeline: %s", raw)
	}
	if len(restored.Messages) != 1 || restored.Messages[0].Content != "task" {
		t.Fatal("notice was added to conversational messages")
	}
}
