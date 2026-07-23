package agent

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

func TestRecordLLMRequestMetadataTracksSuccessfulCacheActivity(t *testing.T) {
	sess := session.New("build")
	completedAt := time.Date(2026, time.July, 23, 12, 34, 56, 0, time.UTC)

	recordLLMRequestMetadata(sess, completedAt, "openai_codex", "gpt-5.5", llm.TokenUsage{
		InputTokens:       4096,
		CachedInputTokens: 2048,
	})

	if got := sess.Metadata[metadataLastLLMRequestAt]; got != "2026-07-23T12:34:56Z" {
		t.Fatalf("last request at = %#v", got)
	}
	if got := sess.Metadata[metadataLastLLMProvider]; got != "openai_codex" {
		t.Fatalf("last provider = %#v", got)
	}
	if got := sess.Metadata[metadataLastLLMModel]; got != "gpt-5.5" {
		t.Fatalf("last model = %#v", got)
	}
	if got := metadataFloat(sess.Metadata, metadataLastLLMInputTokens); got != 4096 {
		t.Fatalf("last input tokens = %v", got)
	}
	if got := metadataFloat(sess.Metadata, metadataLastLLMCachedInputTokens); got != 2048 {
		t.Fatalf("last cached input tokens = %v", got)
	}
}

func TestRecordLLMRequestMetadataUsesFallbackActiveNode(t *testing.T) {
	sess := session.New("build")
	sess.Metadata["fallback_active_provider"] = "anthropic:work"
	sess.Metadata["fallback_active_model"] = "claude-sonnet-4-5"

	recordLLMRequestMetadata(sess, time.Now(), "fallback_chain:primary", "", llm.TokenUsage{})

	if got := sess.Metadata[metadataLastLLMProvider]; got != "anthropic:work" {
		t.Fatalf("last provider = %#v", got)
	}
	if got := sess.Metadata[metadataLastLLMModel]; got != "claude-sonnet-4-5" {
		t.Fatalf("last model = %#v", got)
	}
}
