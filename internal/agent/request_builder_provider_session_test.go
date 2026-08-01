package agent

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestBuildRequestSetsProviderSessionCursorWhenEnabled(t *testing.T) {
	sess := session.New("test-agent")
	sess.Metadata = map[string]interface{}{
		metadataProviderSessionCursor: "cursor-1",
	}
	sess.AddUserMessage("hello")
	sess.AddAssistantMessageWithMetadata("hi", nil, map[string]interface{}{
		messageMetadataProviderSessionCursor: "cursor-1",
	})
	sess.AddUserMessage("follow up")

	ag := New(Config{UseProviderSession: true}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)

	if request.ProviderSessionCursor != "cursor-1" {
		t.Fatalf("ProviderSessionCursor = %q, want cursor-1", request.ProviderSessionCursor)
	}
	if request.PreviousResponseID != "" {
		t.Fatalf("PreviousResponseID = %q, want empty when using provider session", request.PreviousResponseID)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("expected 1 trimmed message, got %d", len(request.Messages))
	}
	if request.Messages[0].Role != "user" || request.Messages[0].Content != "follow up" {
		t.Fatalf("unexpected trimmed message: %+v", request.Messages[0])
	}
}

func TestBuildRequestOmitsProviderSessionCursorWhenIdentityMismatches(t *testing.T) {
	sess := session.New("test-agent")
	sess.Metadata = map[string]interface{}{
		metadataProviderSessionCursor:   "cursor-1",
		metadataProviderSessionIdentity: "identity-a",
	}
	sess.AddUserMessage("hello")
	sess.AddAssistantMessageWithMetadata("hi", nil, map[string]interface{}{
		messageMetadataProviderSessionCursor:   "cursor-1",
		messageMetadataProviderSessionIdentity: "identity-a",
	})
	sess.AddUserMessage("follow up")

	ag := New(Config{UseProviderSession: true, ProviderSessionIdentity: "identity-b"}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)

	if request.ProviderSessionCursor != "" {
		t.Fatalf("ProviderSessionCursor = %q, want empty on identity mismatch", request.ProviderSessionCursor)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("expected full history on identity mismatch, got %d messages", len(request.Messages))
	}
}

func TestBuildRequestUsesProviderSessionCursorWhenIdentityMatches(t *testing.T) {
	sess := session.New("test-agent")
	sess.Metadata = map[string]interface{}{
		metadataProviderSessionCursor:   "cursor-1",
		metadataProviderSessionIdentity: "identity-a",
	}
	sess.AddUserMessage("hello")
	sess.AddAssistantMessageWithMetadata("hi", nil, map[string]interface{}{
		messageMetadataProviderSessionCursor:   "cursor-1",
		messageMetadataProviderSessionIdentity: "identity-a",
	})
	sess.AddUserMessage("follow up")

	ag := New(Config{UseProviderSession: true, ProviderSessionIdentity: "identity-a"}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)

	if request.ProviderSessionCursor != "identity-a|cursor-1" {
		t.Fatalf("ProviderSessionCursor = %q, want identity-bound cursor", request.ProviderSessionCursor)
	}
	if len(request.Messages) != 1 || request.Messages[0].Content != "follow up" {
		t.Fatalf("unexpected trimmed message set: %+v", request.Messages)
	}
}

func TestBuildRequestOmitsProviderSessionCursorWhenDisabled(t *testing.T) {
	sess := session.New("test-agent")
	sess.Metadata = map[string]interface{}{
		metadataProviderSessionCursor: "cursor-1",
	}
	sess.AddUserMessage("hello")

	ag := New(Config{UseProviderSession: false}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)

	if request.ProviderSessionCursor != "" {
		t.Fatalf("ProviderSessionCursor = %q, want empty when disabled", request.ProviderSessionCursor)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("expected full history, got %d messages", len(request.Messages))
	}
}

func TestBuildRequestSetsReasoningEffort(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddUserMessage("hello")

	ag := New(Config{ReasoningEffort: " high "}, nil, tools.NewManager(t.TempDir()), nil)
	request := ag.buildRequest(sess)

	if request.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", request.ReasoningEffort)
	}
}
