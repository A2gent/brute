package fallback

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	DefaultMaxRetries   = 3
	DefaultRetryBackoff = 1 * time.Second
)

// Node represents one provider node in fallback chain order.
type Node struct {
	Name   string
	Model  string
	Client llm.Client
}

// Client attempts providers in order and falls back on transient/provider-side failures.
// Each provider is retried up to MaxRetries times before moving to the next.
type Client struct {
	nodes      []Node
	maxRetries int
}

// ClientOption configures the fallback client.
type ClientOption func(*Client)

// WithMaxRetries sets the maximum number of retries per provider.
func WithMaxRetries(n int) ClientOption {
	return func(c *Client) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

func NewClient(nodes []Node, opts ...ClientOption) *Client {
	copied := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Client == nil {
			continue
		}
		copied = append(copied, node)
	}
	c := &Client{
		nodes:      copied,
		maxRetries: DefaultMaxRetries,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	if len(c.nodes) == 0 {
		return nil, fmt.Errorf("fallback chain has no providers")
	}

	var failures []string
	for i, node := range c.nodes {
		nodeReq := cloneRequestWithModel(request, node.Model)

		var lastErr error
		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if attempt > 0 {
				logging.Info("Retrying provider %s (attempt %d/%d)", node.Name, attempt+1, c.maxRetries+1)
				if err := sleepWithContext(ctx, retryBackoff(attempt)); err != nil {
					return nil, fmt.Errorf("retry interrupted: %w", err)
				}
			}

			resp, err := node.Client.Chat(ctx, nodeReq)
			if err == nil {
				if i > 0 || attempt > 0 {
					logging.Warn("Fallback chain recovered on provider %s (position %d, attempt %d)", node.Name, i+1, attempt+1)
				}
				return resp, nil
			}

			lastErr = err
			if !isRetryableError(ctx, err) {
				break
			}
			logging.Warn("Provider %s failed (attempt %d/%d): %v", node.Name, attempt+1, c.maxRetries+1, err)
		}

		if !isFallbackableError(ctx, lastErr) {
			return nil, fmt.Errorf("%s failed: %w", node.Name, lastErr)
		}
		failures = append(failures, fmt.Sprintf("%s: %v", node.Name, lastErr))
		logging.Warn("Fallback chain provider %s exhausted retries, trying next provider: %v", node.Name, lastErr)
	}

	return nil, fmt.Errorf("all fallback providers failed: %s", strings.Join(failures, " | "))
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	if len(c.nodes) == 0 {
		return nil, fmt.Errorf("fallback chain has no providers")
	}

	var failures []string
	for i, node := range c.nodes {
		nodeReq := cloneRequestWithModel(request, node.Model)

		var lastErr error
		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if attempt > 0 {
				logging.Info("Retrying provider %s (attempt %d/%d)", node.Name, attempt+1, c.maxRetries+1)
				if err := sleepWithContext(ctx, retryBackoff(attempt)); err != nil {
					return nil, fmt.Errorf("retry interrupted: %w", err)
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

			streamClient, ok := node.Client.(llm.StreamingClient)
			if !ok {
				resp, err := node.Client.Chat(ctx, nodeReq)
				if err == nil {
					if i > 0 || attempt > 0 {
						logging.Warn("Fallback chain recovered on provider %s (position %d, attempt %d)", node.Name, i+1, attempt+1)
					}
					return resp, nil
				}
				lastErr = err
				if !isRetryableError(ctx, err) {
					break
				}
				logging.Warn("Provider %s failed (attempt %d/%d): %v", node.Name, attempt+1, c.maxRetries+1, err)
				continue
			}

			resp, err := streamClient.ChatStream(ctx, nodeReq, wrappedOnEvent)
			if err == nil {
				if i > 0 || attempt > 0 {
					logging.Warn("Fallback chain recovered on provider %s (position %d, attempt %d)", node.Name, i+1, attempt+1)
				}
				return resp, nil
			}

			lastErr = err
			if emitted {
				if isRetryableError(ctx, err) {
					logging.Warn("Provider %s failed after partial stream (attempt %d/%d): %v", node.Name, attempt+1, c.maxRetries+1, err)
					continue
				}
				break
			}
			if !isRetryableError(ctx, err) {
				break
			}
			logging.Warn("Provider %s failed (attempt %d/%d): %v", node.Name, attempt+1, c.maxRetries+1, err)
		}

		if !isFallbackableError(ctx, lastErr) {
			return nil, fmt.Errorf("%s failed: %w", node.Name, lastErr)
		}
		failures = append(failures, fmt.Sprintf("%s: %v", node.Name, lastErr))
		logging.Warn("Fallback chain provider %s exhausted retries, trying next provider: %v", node.Name, lastErr)
	}

	return nil, fmt.Errorf("all fallback providers failed: %s", strings.Join(failures, " | "))
}

func cloneRequestWithModel(request *llm.ChatRequest, model string) *llm.ChatRequest {
	if request == nil {
		return &llm.ChatRequest{Model: model}
	}
	copied := *request
	copied.Model = strings.TrimSpace(model)
	return &copied
}

// retryBackoff returns the backoff duration for the given attempt (exponential with jitter).
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

// sleepWithContext sleeps for the specified duration or returns early if context is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// isRetryableError determines if an error is worth retrying within the same provider.
// This is more permissive than isFallbackableError - includes transient network issues.
func isRetryableError(ctx context.Context, err error) bool {
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

	// Network and connection errors - worth retrying
	if strings.Contains(msg, "context canceled") {
		return false // User cancellation - don't retry
	}
	if strings.Contains(msg, "context deadline exceeded") {
		return true // Timeout - retry
	}
	if strings.Contains(msg, "failed to connect") || strings.Contains(msg, "request failed") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "no such host") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporarily unavailable") || strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}

	// Rate limits - retry with backoff
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "ratelimit") || strings.Contains(msg, "429") {
		return true
	}

	// Server errors (5xx) - retry
	if hasStatusCodeInRange(msg, 500, 599) {
		return true
	}

	// Overloaded errors
	if strings.Contains(msg, "overloaded") || strings.Contains(msg, "503") || strings.Contains(msg, "502") {
		return true
	}

	return false
}

// isFallbackableError determines if we should try the next provider in the chain.
// Auth errors should fallback to next provider, but context cancellation should not.
func isFallbackableError(ctx context.Context, err error) bool {
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

	// All retryable errors are also fallbackable
	if isRetryableError(ctx, err) {
		return true
	}

	// Auth/billing errors - fallback to next provider
	if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "invalid api key") || strings.Contains(msg, "billing") ||
		strings.Contains(msg, "insufficient") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "401") || strings.Contains(msg, "403") {
		return true
	}

	return false
}

func hasStatusCodeInRange(message string, min int, max int) bool {
	start := strings.Index(message, "(")
	for start >= 0 {
		end := strings.Index(message[start:], ")")
		if end < 0 {
			return false
		}
		codeText := strings.TrimSpace(message[start+1 : start+end])
		if code, err := strconv.Atoi(codeText); err == nil && code >= min && code <= max {
			return true
		}
		next := start + end + 1
		rest := message[next:]
		offset := strings.Index(rest, "(")
		if offset < 0 {
			return false
		}
		start = next + offset
	}
	return false
}
