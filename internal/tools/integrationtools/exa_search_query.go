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

const exaSearchEndpoint = "https://api.exa.ai/search"

// ExaSearchQueryTool runs web searches via configured Exa integrations.
type ExaSearchQueryTool struct {
	store  storage.Store
	client *http.Client
}

type ExaSearchQueryParams struct {
	Query           string `json:"query"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	Type            string `json:"type,omitempty"`
	Category        string `json:"category,omitempty"`
	NumResults      int    `json:"num_results,omitempty"`
	IncludeDomains  string `json:"include_domains,omitempty"`
	ExcludeDomains  string `json:"exclude_domains,omitempty"`
}

type exaSearchRequest struct {
	Query          string   `json:"query"`
	Type           string   `json:"type,omitempty"`
	Category       string   `json:"category,omitempty"`
	NumResults     int      `json:"num_results,omitempty"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
	Contents       struct {
		Text struct {
			MaxCharacters int `json:"max_characters"`
		} `json:"text"`
	} `json:"contents"`
}

type exaSearchResponse struct {
	Results []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Text  string `json:"text"`
	} `json:"results"`
}

func NewExaSearchQueryTool(store storage.Store) *ExaSearchQueryTool {
	return &ExaSearchQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *ExaSearchQueryTool) Name() string {
	return "exa_search"
}

func (t *ExaSearchQueryTool) Description() string {
	return "Search the web using Exa.ai. Best for retrieving high-quality, relevant content."
}

func (t *ExaSearchQueryTool) Schema() map[string]interface{} {
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
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Search type: neural (or auto), keyword. Defaults to auto.",
				"enum":        []string{"auto", "neural", "keyword"},
			},
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Category: company, research paper, news, pdf, github, tweet, personal site, linkedin profile, financial report. Defaults to general search (null).",
				"enum":        []string{"company", "research paper", "news", "pdf", "github", "tweet", "personal site", "linkedin profile", "financial report"},
			},
			"num_results": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results (1-10, default 5)",
			},
			"include_domains": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated list of domains to include (optional)",
			},
			"exclude_domains": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated list of domains to exclude (optional)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *ExaSearchQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p ExaSearchQueryParams
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
		return &tools.Result{Success: false, Error: "selected exa integration is missing api_key"}, nil
	}

	reqBody := exaSearchRequest{
		Query:      query,
		Type:       "auto",
		NumResults: 5,
	}

	if p.Type != "" {
		reqBody.Type = p.Type
	}
	if p.Category != "" {
		reqBody.Category = p.Category
	}
	if p.NumResults > 0 {
		reqBody.NumResults = p.NumResults
	}
	if reqBody.NumResults > 10 {
		reqBody.NumResults = 10
	}

	if p.IncludeDomains != "" {
		reqBody.IncludeDomains = strings.Split(p.IncludeDomains, ",")
		for i := range reqBody.IncludeDomains {
			reqBody.IncludeDomains[i] = strings.TrimSpace(reqBody.IncludeDomains[i])
		}
	}
	if p.ExcludeDomains != "" {
		reqBody.ExcludeDomains = strings.Split(p.ExcludeDomains, ",")
		for i := range reqBody.ExcludeDomains {
			reqBody.ExcludeDomains[i] = strings.TrimSpace(reqBody.ExcludeDomains[i])
		}
	}

	// Always request text content for RAG
	reqBody.Contents.Text.MaxCharacters = 2000

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to marshal request: %v", err)}, nil
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		exaSearchEndpoint,
		bytes.NewBuffer(jsonBody),
	)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("exa search request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read exa search response: %v", err)}, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("exa search API error (status %d): %s", resp.StatusCode, msg),
		}, nil
	}

	var payload exaSearchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("failed to decode exa search response: %v", err),
		}, nil
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "Exa Search results for %q\n", query)

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
		text := strings.TrimSpace(item.Text)

		fmt.Fprintf(&out, "\n%d. %s\n", idx+1, title)
		if url != "" {
			fmt.Fprintf(&out, "URL: %s\n", url)
		}
		if text != "" {
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			fmt.Fprintf(&out, "Content: %s\n", text)
		}
	}

	return &tools.Result{Success: true, Output: out.String()}, nil
}

func (t *ExaSearchQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "exa" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled exa integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("exa integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple exa integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("exa integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple exa integrations are enabled; pass integration_id or integration_name")
}

// Ensure ExaSearchQueryTool implements Tool.
var _ tools.Tool = (*ExaSearchQueryTool)(nil)
