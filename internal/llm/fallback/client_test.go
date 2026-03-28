package fallback

import (
	"context"
	"errors"
	"testing"
)

func TestIsRetryableError_ContextCanceledWithActiveContext(t *testing.T) {
	ctx := context.Background()
	err := errors.New("request failed: Post \"https://example.com\": context canceled")

	if !isRetryableError(ctx, err) {
		t.Fatalf("expected provider-side context canceled error to be retryable")
	}
}

func TestIsRetryableError_ContextCanceledWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := errors.New("request failed: Post \"https://example.com\": context canceled")

	if isRetryableError(ctx, err) {
		t.Fatalf("expected canceled context to be non-retryable")
	}
}

func TestIsFallbackableError_ContextCanceledWithActiveContext(t *testing.T) {
	ctx := context.Background()
	err := errors.New("request failed: Post \"https://example.com\": context canceled")

	if !isFallbackableError(ctx, err) {
		t.Fatalf("expected provider-side context canceled error to be fallbackable")
	}
}
