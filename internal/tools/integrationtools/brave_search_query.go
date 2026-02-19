package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const braveWebSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"

// BraveSearchQueryTool runs web searches via configured Brave Search integrations.
type BraveSearchQueryTool struct {
	store  storage.Store
	client *http.Client
}

type BraveSearchQueryParams struct {
	Query           string `json:"query"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	Count           int    `json:"count,omitempty"`
	Offset          int    `json:"offset,omitempty"`
	SafeSearch      string `json:"safesearch,omitempty"`
	Freshness       string `json:"freshness,omitempty"`
}

type braveWebSearchResponse struct {
	Query struct {
		Original string `json:"original"`
	} `json:"query"`
	Web struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Description   string   `json:"description"`
			Age           string   `json:"age"`
			ExtraSnippets []string `json:"extra_snippets"`
		} `json:"results"`
	} `json:"web"`
}

func NewBraveSearchQueryTool(store storage.Store) *BraveSearchQueryTool {
	return &BraveSearchQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *BraveSearchQueryTool) Name() string {
	return "brave_search_query"
}

func (t *BraveSearchQueryTool) Description() string {
	return "Search the web using enabled Brave Search integrations. Returns result titles, links, and snippets."
}

func (t *BraveSearchQueryTool) Schema() map[string]interface{} {
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
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "Result count (1-20, default 10)",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Result offset for pagination (0+)",
			},
			"safesearch": map[string]interface{}{
				"type":        "string",
				"description": "SafeSearch level: off, moderate, or strict. Defaults to integration setting, then moderate.",
				"enum":        []string{"off", "moderate", "strict"},
			},
			"freshness": map[string]interface{}{
				"type":        "string",
				"description": "Optional recency filter: pd (day), pw (week), pm (month), or py (year).",
				"enum":        []string{"pd", "pw", "pm", "py"},
			},
		},
		"required": []string{"query"},
	}
}

func (t *BraveSearchQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p BraveSearchQueryParams
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
		return &tools.Result{Success: false, Error: "selected brave_search integration is missing api_key"}, nil
	}

	count := p.Count
	if count <= 0 {
		count = 10
	}
	if count > 20 {
		count = 20
	}

	offset := p.Offset
	if offset < 0 {
		offset = 0
	}

	safeSearch := strings.ToLower(strings.TrimSpace(p.SafeSearch))
	if safeSearch == "" {
		safeSearch = strings.ToLower(strings.TrimSpace(integration.Config["safesearch"]))
	}
	if safeSearch == "" {
		safeSearch = "moderate"
	}
	if safeSearch != "off" && safeSearch != "moderate" && safeSearch != "strict" {
		return &tools.Result{Success: false, Error: "safesearch must be one of: off, moderate, strict"}, nil
	}

	freshness := strings.ToLower(strings.TrimSpace(p.Freshness))
	if freshness != "" && freshness != "pd" && freshness != "pw" && freshness != "pm" && freshness != "py" {
		return &tools.Result{Success: false, Error: "freshness must be one of: pd, pw, pm, py"}, nil
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("count", strconv.Itoa(count))
	if offset > 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
	values.Set("safesearch", safeSearch)
	if freshness != "" {
		values.Set("freshness", freshness)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		braveWebSearchEndpoint+"?"+values.Encode(),
		nil,
	)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create request: %v", err)}, nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("brave search request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read brave search response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("brave search API error (status %d): %s", resp.StatusCode, msg),
		}, nil
	}

	var payload braveWebSearchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("failed to decode brave search response: %v", err),
		}, nil
	}

	var out bytes.Buffer
	originalQuery := strings.TrimSpace(payload.Query.Original)
	if originalQuery == "" {
		originalQuery = query
	}
	fmt.Fprintf(&out, "Brave Search results for %q\n", originalQuery)

	results := payload.Web.Results
	if len(results) == 0 {
		out.WriteString("No web results returned.\n")
		return &tools.Result{Success: true, Output: out.String()}, nil
	}

	for idx, item := range results {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(untitled)"
		}
		link := strings.TrimSpace(item.URL)
		desc := strings.TrimSpace(item.Description)
		if desc == "" && len(item.ExtraSnippets) > 0 {
			desc = strings.TrimSpace(item.ExtraSnippets[0])
		}
		age := strings.TrimSpace(item.Age)

		fmt.Fprintf(&out, "\n%d. %s\n", idx+1, title)
		if link != "" {
			fmt.Fprintf(&out, "URL: %s\n", link)
		}
		if desc != "" {
			fmt.Fprintf(&out, "Snippet: %s\n", desc)
		}
		if age != "" {
			fmt.Fprintf(&out, "Age: %s\n", age)
		}
	}

	return &tools.Result{Success: true, Output: out.String()}, nil
}

func (t *BraveSearchQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "brave_search" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled brave_search integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("brave_search integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var matched []*storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) == name {
				matched = append(matched, item)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
		if len(matched) > 1 {
			return nil, fmt.Errorf("multiple brave_search integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("brave_search integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple brave_search integrations are enabled; pass integration_id or integration_name")
}

// Ensure BraveSearchQueryTool implements Tool.
var _ tools.Tool = (*BraveSearchQueryTool)(nil)
