package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const tavilySearchEndpoint = "https://api.tavily.com/search"

// TavilySearchTool runs web searches via configured Tavily integrations.
type TavilySearchTool struct {
	store  storage.Store
	client *http.Client
}

type TavilySearchParams struct {
	Query             string `json:"query"`
	IntegrationID     string `json:"integration_id,omitempty"`
	IntegrationName   string `json:"integration_name,omitempty"`
	SearchDepth       string `json:"search_depth,omitempty"`
	MaxResults        int    `json:"max_results,omitempty"`
	Topic             string `json:"topic,omitempty"`
	IncludeAnswer     bool   `json:"include_answer,omitempty"`
	IncludeRawContent bool   `json:"include_raw_content,omitempty"`
	IncludeDomains    string `json:"include_domains,omitempty"`
	ExcludeDomains    string `json:"exclude_domains,omitempty"`
	Days              int    `json:"days,omitempty"`
}

type tavilySearchRequest struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	IncludeAnswer     bool     `json:"include_answer,omitempty"`
	IncludeRawContent bool     `json:"include_raw_content,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
	Days              int      `json:"days,omitempty"`
}

type tavilySearchResponse struct {
	Answer  string `json:"answer"`
	Results []struct {
		Title      string  `json:"title"`
		URL        string  `json:"url"`
		Content    string  `json:"content"`
		RawContent string  `json:"raw_content"`
		Score      float64 `json:"score"`
	} `json:"results"`
}

func NewTavilySearchTool(store storage.Store) *TavilySearchTool {
	return &TavilySearchTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *TavilySearchTool) Name() string {
	return "tavily_search"
}

func (t *TavilySearchTool) Description() string {
	return "Search the web using Tavily integrations. Good for agent-oriented research with concise answers and sources."
}

func (t *TavilySearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query text",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific integration name to use (optional)",
			},
			"search_depth": map[string]interface{}{
				"type":        "string",
				"description": "Search depth: basic or advanced. Defaults to basic.",
				"enum":        []string{"basic", "advanced"},
			},
			"topic": map[string]interface{}{
				"type":        "string",
				"description": "Topic focus: general or news. Defaults to general.",
				"enum":        []string{"general", "news"},
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results (1-20, default 5)",
			},
			"include_answer": map[string]interface{}{
				"type":        "boolean",
				"description": "Include Tavily's generated answer summary (default false)",
			},
			"include_raw_content": map[string]interface{}{
				"type":        "boolean",
				"description": "Include raw page content in results when available (default false)",
			},
			"include_domains": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated list of domains to include (optional)",
			},
			"exclude_domains": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated list of domains to exclude (optional)",
			},
			"days": map[string]interface{}{
				"type":        "integer",
				"description": "For topic=news, optionally limit recency window in days",
			},
		},
		"required": []string{"query"},
	}
}

func (t *TavilySearchTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p TavilySearchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	query := strings.TrimSpace(p.Query)
	if query == "" {
		return &tools.Result{Success: false, Error: "query is required"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return &tools.Result{Success: false, Error: "selected tavily integration is missing api_key"}, nil
	}

	reqBody := tavilySearchRequest{
		Query:       query,
		SearchDepth: "basic",
		Topic:       "general",
		MaxResults:  5,
	}
	if depth := strings.ToLower(strings.TrimSpace(p.SearchDepth)); depth != "" {
		if depth != "basic" && depth != "advanced" {
			return &tools.Result{Success: false, Error: "search_depth must be one of: basic, advanced"}, nil
		}
		reqBody.SearchDepth = depth
	}
	if topic := strings.ToLower(strings.TrimSpace(p.Topic)); topic != "" {
		if topic != "general" && topic != "news" {
			return &tools.Result{Success: false, Error: "topic must be one of: general, news"}, nil
		}
		reqBody.Topic = topic
	}
	if p.MaxResults > 0 {
		reqBody.MaxResults = p.MaxResults
	}
	if reqBody.MaxResults > 20 {
		reqBody.MaxResults = 20
	}
	if reqBody.MaxResults < 1 {
		reqBody.MaxResults = 1
	}
	if p.Days > 0 {
		reqBody.Days = p.Days
	}
	reqBody.IncludeAnswer = p.IncludeAnswer
	reqBody.IncludeRawContent = p.IncludeRawContent
	reqBody.IncludeDomains = splitCSV(p.IncludeDomains)
	reqBody.ExcludeDomains = splitCSV(p.ExcludeDomains)

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to marshal request: %v", err)}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchEndpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("tavily search request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read tavily search response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("tavily search API error (status %d): %s", resp.StatusCode, msg)}, nil
	}

	var payload tavilySearchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to decode tavily search response: %v", err)}, nil
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "Tavily Search results for %q\n", query)
	if answer := strings.TrimSpace(payload.Answer); answer != "" {
		fmt.Fprintf(&out, "\nAnswer: %s\n", answer)
	}
	if len(payload.Results) == 0 {
		out.WriteString("No results returned.\n")
		return &tools.Result{Success: true, Output: out.String()}, nil
	}
	for idx, item := range payload.Results {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(untitled)"
		}
		url := strings.TrimSpace(item.URL)
		content := strings.TrimSpace(item.Content)
		if content == "" && p.IncludeRawContent {
			content = strings.TrimSpace(item.RawContent)
		}
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&out, "\n%d. %s\n", idx+1, title)
		if url != "" {
			fmt.Fprintf(&out, "URL: %s\n", url)
		}
		if content != "" {
			fmt.Fprintf(&out, "Snippet: %s\n", content)
		}
		if item.Score > 0 {
			fmt.Fprintf(&out, "Score: %.3f\n", item.Score)
		}
	}

	return &tools.Result{Success: true, Output: out.String()}, nil
}

func (t *TavilySearchTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "tavily" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled tavily integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("tavily integration with id %q not found or disabled", id)
	}

	name := strings.TrimSpace(integrationName)
	if name != "" {
		matches := make([]*storage.Integration, 0, 1)
		for _, item := range candidates {
			if strings.EqualFold(strings.TrimSpace(item.Name), name) {
				matches = append(matches, item)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return nil, fmt.Errorf("tavily integration named %q not found", name)
		default:
			return nil, fmt.Errorf("multiple tavily integrations matched name %q; pass integration_id", name)
		}
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple tavily integrations are enabled; pass integration_id or integration_name")
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
