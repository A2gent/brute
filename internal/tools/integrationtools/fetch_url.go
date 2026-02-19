package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/A2gent/brute/internal/tools"
)

// FetchURLTool fetches a URL and returns its content as markdown.
type FetchURLTool struct{}

type FetchURLParams struct {
	URL string `json:"url"`
}

func NewFetchURLTool() *FetchURLTool {
	return &FetchURLTool{}
}

func (t *FetchURLTool) Name() string {
	return "fetch_url"
}

func (t *FetchURLTool) Description() string {
	return "Fetch a web page and return its content as markdown. Useful for reading documentation or articles."
}

func (t *FetchURLTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch",
			},
		},
		"required": []string{"url"},
	}
}

func (t *FetchURLTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p FetchURLParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	urlStr := strings.TrimSpace(p.URL)
	if urlStr == "" {
		return &tools.Result{Success: false, Error: "url is required"}, nil
	}

	// Create a client with a timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create request: %v", err)}, nil
	}

	// Set a User-Agent to avoid being blocked by some sites
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("HTTP error: %s", resp.Status),
		}, nil
	}

	// Limit response size to 5MB to prevent memory issues
	const maxBytes = 5 * 1024 * 1024
	bodyReader := io.LimitReader(resp.Body, maxBytes)

	// Convert HTML to Markdown
	converter := md.NewConverter("", true, nil)
	markdown, err := converter.ConvertReader(bodyReader)
	if err != nil {
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("failed to convert HTML to markdown: %v", err),
		}, nil
	}

	return &tools.Result{
		Success: true,
		Output:  markdown.String(),
	}, nil
}

// Ensure FetchURLTool implements Tool.
var _ tools.Tool = (*FetchURLTool)(nil)
