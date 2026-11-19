package agent

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestMergeSessionLiveStatePendingQuestionFreshWins(t *testing.T) {
	sess := &session.Session{Metadata: map[string]interface{}{"pending_question": "old"}}

	mergeSessionLiveState(sess, &session.Session{
		Status:   session.StatusInputRequired,
		Metadata: map[string]interface{}{"pending_question": "new"},
	})
	if got := sess.Metadata["pending_question"]; got != "new" {
		t.Fatalf("pending_question = %v, want new", got)
	}

	sess.Metadata["pending_question"] = "old"
	mergeSessionLiveState(sess, &session.Session{Metadata: map[string]interface{}{}})
	if _, ok := sess.Metadata["pending_question"]; ok {
		t.Fatalf("stale pending_question kept: %v", sess.Metadata)
	}
}
