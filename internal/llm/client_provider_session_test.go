package llm_test

import (
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestChatRequestProviderSessionCursorIndependentFromPreviousResponseID(t *testing.T) {
	req := &llm.ChatRequest{
		ProviderSessionCursor: "provider-session-abc",
		PreviousResponseID:    "resp_xyz",
	}
	if req.ProviderSessionCursor != "provider-session-abc" {
		t.Fatalf("ProviderSessionCursor = %q, want provider-session-abc", req.ProviderSessionCursor)
	}
	if req.PreviousResponseID != "resp_xyz" {
		t.Fatalf("PreviousResponseID = %q, want resp_xyz", req.PreviousResponseID)
	}
	if req.ProviderSessionCursor == req.PreviousResponseID {
		t.Fatal("ProviderSessionCursor and PreviousResponseID must be independent fields")
	}
}

func TestChatResponseProviderSessionCursorIndependentFromResponseID(t *testing.T) {
	resp := &llm.ChatResponse{
		ProviderSessionCursor: "provider-session-abc",
		ResponseID:            "resp_xyz",
	}
	if resp.ProviderSessionCursor != "provider-session-abc" {
		t.Fatalf("ProviderSessionCursor = %q, want provider-session-abc", resp.ProviderSessionCursor)
	}
	if resp.ResponseID != "resp_xyz" {
		t.Fatalf("ResponseID = %q, want resp_xyz", resp.ResponseID)
	}
	if resp.ProviderSessionCursor == resp.ResponseID {
		t.Fatal("ProviderSessionCursor and ResponseID must be independent fields")
	}
}
