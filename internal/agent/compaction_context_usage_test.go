package agent

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestCompactionContextUsageMetadata(t *testing.T) {
	metadata := compactionContextUsageMetadata(80, 20, 100)
	if got := metadata[messageMetadataContextBeforePercent]; got != float64(80) {
		t.Fatalf("before percent = %v, want 80", got)
	}
	if got := metadata[messageMetadataContextAfterPercent]; got != float64(20) {
		t.Fatalf("after percent = %v, want 20", got)
	}

	if got := compactionContextUsageMetadata(80, 20, 0); got != nil {
		t.Fatalf("metadata without a context window = %v, want nil", got)
	}
}

func TestEstimateCompactedContextTokens(t *testing.T) {
	messages := []session.Message{{Role: "assistant", Content: strings.Repeat("x", 400)}}
	if got := estimateCompactedContextTokens(messages, strings.Repeat("s", 400)); got != 200 {
		t.Fatalf("estimated tokens = %d, want 200", got)
	}
}
