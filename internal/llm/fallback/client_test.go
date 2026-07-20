package fallback

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

func TestIsRetryableError_UnsafeForRetryMarker(t *testing.T) {
	ctx := context.Background()
	unsafeErr := llm.UnsafeForRetry(errors.New("Claude CLI failed after process start"))
	if isRetryableError(ctx, unsafeErr) {
		t.Fatalf("expected explicitly unsafe error to be non-retryable")
	}
	if isFallbackableError(ctx, unsafeErr) {
		t.Fatalf("expected explicitly unsafe error to be non-fallbackable")
	}
}

func TestClientChat_DoesNotRetryOrFallbackOnUnsafeError(t *testing.T) {
	primary := &countingLLM{err: llm.UnsafeForRetry(errors.New("native tool execution may have started"))}
	secondary := &countingLLM{err: errors.New("secondary should not run")}
	client := NewClient([]Node{
		{Name: "primary", Client: primary},
		{Name: "secondary", Client: secondary},
	}, WithMaxRetries(3))

	_, err := client.Chat(context.Background(), &llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if primary.calls != 1 {
		t.Fatalf("expected primary provider to be called once, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected secondary provider to be skipped for unsafe error, got %d calls", secondary.calls)
	}
}
