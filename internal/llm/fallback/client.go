package fallback

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/logging"
)

// Node represents one provider node in fallback chain order.
type Node struct {
	Name   string
	Model  string
	Client llm.Client
}

// Client attempts providers in order and falls back on transient/provider-side failures.
type Client struct {
	nodes []Node
}

func NewClient(nodes []Node) *Client {
	copied := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Client == nil {
			continue
		}
		copied = append(copied, node)
	}
	return &Client{nodes: copied}
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	if len(c.nodes) == 0 {
		return nil, fmt.Errorf("fallback chain has no providers")
	}

	var failures []string
	for i, node := range c.nodes {
		nodeReq := cloneRequestWithModel(request, node.Model)
		resp, err := node.Client.Chat(ctx, nodeReq)
		if err == nil {
			if i > 0 {
				logging.Warn("Fallback chain recovered on provider %s (position %d)", node.Name, i+1)
			}
			return resp, nil
		}
		if !isFallbackableError(ctx, err) {
			return nil, fmt.Errorf("%s failed: %w", node.Name, err)
		}
		failures = append(failures, fmt.Sprintf("%s: %v", node.Name, err))
		logging.Warn("Fallback chain provider %s failed, trying next: %v", node.Name, err)
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
				if i > 0 {
					logging.Warn("Fallback chain recovered on provider %s (position %d)", node.Name, i+1)
				}
				return resp, nil
			}
			if !isFallbackableError(ctx, err) {
				return nil, fmt.Errorf("%s failed: %w", node.Name, err)
			}
			failures = append(failures, fmt.Sprintf("%s: %v", node.Name, err))
			logging.Warn("Fallback chain provider %s failed, trying next: %v", node.Name, err)
			continue
		}

		resp, err := streamClient.ChatStream(ctx, nodeReq, wrappedOnEvent)
		if err == nil {
			if i > 0 {
				logging.Warn("Fallback chain recovered on provider %s (position %d)", node.Name, i+1)
			}
			return resp, nil
		}
		if emitted {
			return nil, fmt.Errorf("%s failed after streaming partial output: %w", node.Name, err)
		}
		if !isFallbackableError(ctx, err) {
			return nil, fmt.Errorf("%s failed: %w", node.Name, err)
		}
		failures = append(failures, fmt.Sprintf("%s: %v", node.Name, err))
		logging.Warn("Fallback chain provider %s failed, trying next: %v", node.Name, err)
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

	// Explicit retry-worthy classes requested: rate-limit, auth/billing, provider 5xx, and reachability.
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "ratelimit") || strings.Contains(msg, "429") {
		return true
	}
	if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "authentication") || strings.Contains(msg, "invalid api key") || strings.Contains(msg, "billing") || strings.Contains(msg, "insufficient") || strings.Contains(msg, "quota") {
		return true
	}
	if hasStatusCodeInRange(msg, 500, 599) {
		return true
	}
	if strings.Contains(msg, "failed to connect") || strings.Contains(msg, "request failed") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp") || strings.Contains(msg, "no such host") || strings.Contains(msg, "timeout") || strings.Contains(msg, "temporarily unavailable") || strings.Contains(msg, "tls handshake timeout") || strings.Contains(msg, "eof") {
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
