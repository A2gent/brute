package retry

import (
	"context"
	"errors"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

type countingLLM struct {
	calls int
	err   error
}

func (c *countingLLM) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	return nil, c.err
}

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

func TestIsRetryableError_UnsafeForRetryMarker(t *testing.T) {
	ctx := context.Background()
	transient := errors.New("request failed: connection reset by peer")
	if !IsRetryableError(ctx, transient) {
		t.Fatalf("expected otherwise transient error to be retryable")
	}

	unsafeErr := llm.UnsafeForRetry(transient)
	if IsRetryableError(ctx, unsafeErr) {
		t.Fatalf("expected explicitly unsafe error to be non-retryable")
	}
}

func TestClientChat_DoesNotRetryUnsafeError(t *testing.T) {
	inner := &countingLLM{err: llm.UnsafeForRetry(errors.New("provider failed after process start"))}
	wrapped := Wrap(inner, WithMaxRetries(3))

	_, err := wrapped.Chat(context.Background(), &llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Fatalf("expected exactly 1 attempt for unsafe error, got %d", inner.calls)
	}
}
