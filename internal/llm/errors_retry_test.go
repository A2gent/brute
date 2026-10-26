package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestNonRetryablePreservesErrorWithoutBlockingFallback(t *testing.T) {
	if NonRetryable(nil) != nil {
		t.Fatal("nil error must remain nil")
	}
	original := errors.New("invalid request")
	marked := NonRetryable(original)
	wrapped := fmt.Errorf("proxy: %w", marked)
	if marked.Error() != original.Error() || !errors.Is(wrapped, original) || !IsNonRetryable(wrapped) {
		t.Fatalf("error identity or retry marker lost: %v", wrapped)
	}
	if IsUnsafeForRetry(wrapped) || IsNonRetryable(original) {
		t.Fatal("retry and execution-safety markers must remain distinct")
	}
}
