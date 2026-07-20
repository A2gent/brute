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
