package lmstudio

import (
	"context"
	"fmt"
	"net/http"

	"github.com/A2gent/brute/internal/llm"
)

func (c *Client) prepareRequest(ctx context.Context, request *llm.ChatRequest) (http.Header, func(), error) {
	if c.PrepareRequest == nil {
		return nil, func() {}, nil
	}
	headers, cleanup, err := c.PrepareRequest(ctx, request)
	if cleanup == nil {
		cleanup = func() {}
	}
	if err != nil {
		return nil, cleanup, fmt.Errorf("prepare request: %w", err)
	}
	return headers, cleanup, nil
}

func mergeRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
