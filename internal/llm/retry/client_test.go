package retry

import (
	"context"
	"errors"
	"testing"
)

func TestIsRetryableError_ContextCanceledWithActiveContext(t *testing.T) {
	ctx := context.Background()
	err := errors.New("request failed: Post \"https://example.com\": context canceled")

	if !IsRetryableError(ctx, err) {
		t.Fatalf("expected provider-side context canceled error to be retryable")
	}
}

func TestIsRetryableError_ContextCanceledWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := errors.New("request failed: Post \"https://example.com\": context canceled")

	if IsRetryableError(ctx, err) {
		t.Fatalf("expected canceled context to be non-retryable")
	}
}

func TestIsRetryableError_HTTP2StreamInternalError(t *testing.T) {
	ctx := context.Background()
	err := errors.New("failed to read Codex response: stream error: stream ID 21; INTERNAL_ERROR; received from peer")

	if !IsRetryableError(ctx, err) {
		t.Fatalf("expected provider HTTP/2 stream internal errors to be retryable")
	}
}
