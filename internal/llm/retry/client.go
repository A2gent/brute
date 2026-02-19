package retry

import (
	"context"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	DefaultMaxRetries   = 3
	DefaultRetryBackoff = 1 * time.Second
)

// Client wraps an LLM client with retry logic for transient errors.
type Client struct {
	inner      llm.Client
	maxRetries int
}

// ClientOption configures the retry client.
type ClientOption func(*Client)

// WithMaxRetries sets the maximum number of retries.
func WithMaxRetries(n int) ClientOption {
	return func(c *Client) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

// Wrap wraps an LLM client with retry logic.
func Wrap(inner llm.Client, opts ...ClientOption) llm.Client {
	if inner == nil {
		return nil
	}
	c := &Client{
		inner:      inner,
		maxRetries: DefaultMaxRetries,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.maxRetries == 0 {
		return inner
	}
	return c
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			logging.Info("Retrying LLM request (attempt %d/%d)", attempt+1, c.maxRetries+1)
			if err := sleepWithContext(ctx, retryBackoff(attempt)); err != nil {
				return nil, lastErr
			}
		}

		resp, err := c.inner.Chat(ctx, request)
		if err == nil {
			if attempt > 0 {
				logging.Info("LLM request succeeded after %d retries", attempt)
			}
			return resp, nil
		}

		lastErr = err
		if !IsRetryableError(ctx, err) {
			return nil, err
		}
		logging.Warn("LLM request failed (attempt %d/%d): %v", attempt+1, c.maxRetries+1, err)
	}
	return nil, lastErr
}

// ChatStream implements streaming with retry logic.
func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	streamClient, ok := c.inner.(llm.StreamingClient)
	if !ok {
		return c.Chat(ctx, request)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			logging.Info("Retrying LLM stream request (attempt %d/%d)", attempt+1, c.maxRetries+1)
			if err := sleepWithContext(ctx, retryBackoff(attempt)); err != nil {
				return nil, lastErr
			}
		}

		emitted := false
		wrappedOnEvent := onEvent
		if onEvent != nil {
			wrappedOnEvent = func(ev llm.StreamEvent) error {
				if ev.Type == llm.StreamEventContentDelta || ev.Type == llm.StreamEventToolCallDelta {
					emitted = true
				}
				return onEvent(ev)
			}
		}

		resp, err := streamClient.ChatStream(ctx, request, wrappedOnEvent)
		if err == nil {
			if attempt > 0 {
				logging.Info("LLM stream request succeeded after %d retries", attempt)
			}
			return resp, nil
		}

		lastErr = err
		if emitted && !IsRetryableError(ctx, err) {
			return nil, err
		}
		if !IsRetryableError(ctx, err) {
			return nil, err
		}
		logging.Warn("LLM stream request failed (attempt %d/%d): %v", attempt+1, c.maxRetries+1, err)
	}
	return nil, lastErr
}

func retryBackoff(attempt int) time.Duration {
	base := DefaultRetryBackoff
	for i := 0; i < attempt; i++ {
		base *= 2
	}
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// IsRetryableError determines if an error is worth retrying.
func IsRetryableError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}

	if strings.Contains(msg, "context canceled") {
		return false
	}
	if strings.Contains(msg, "context deadline exceeded") {
		return true
	}
	if strings.Contains(msg, "failed to connect") || strings.Contains(msg, "request failed") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "no such host") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporarily unavailable") || strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "ratelimit") || strings.Contains(msg, "429") {
		return true
	}
	if strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") || strings.Contains(msg, "overloaded") {
		return true
	}
	return false
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
