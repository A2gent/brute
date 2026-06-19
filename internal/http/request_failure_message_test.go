package http

import (
	"errors"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestAddRequestFailedAssistantMessageAppendsForNewFailure(t *testing.T) {
	sess := session.New("test-agent")
	addRequestFailedAssistantMessage(sess, errors.New("provider unavailable"))

	if len(sess.Messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(sess.Messages))
	}
	if got, want := sess.Messages[0].Content, "Request failed: provider unavailable"; got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
}

func TestAddRequestFailedAssistantMessageSkipsDuplicateAgentFailure(t *testing.T) {
	sess := session.New("test-agent")
	const message = "Agent stopped after reaching maximum step limit (50) before producing a final answer."
	sess.AddAssistantMessage(message, nil)

	addRequestFailedAssistantMessage(sess, errors.New(message))

	if len(sess.Messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(sess.Messages))
	}
	if got := sess.Messages[0].Content; got != message {
		t.Fatalf("message content = %q, want %q", got, message)
	}
}

func TestAddRequestFailedAssistantMessageSkipsDuplicateRequestFailure(t *testing.T) {
	sess := session.New("test-agent")
	const message = "Request failed: provider unavailable"
	sess.AddAssistantMessage(message, nil)

	addRequestFailedAssistantMessage(sess, errors.New("provider unavailable"))

	if len(sess.Messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(sess.Messages))
	}
}
