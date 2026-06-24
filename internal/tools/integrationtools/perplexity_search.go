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

const perplexityChatCompletionsEndpoint = "https://api.perplexity.ai/chat/completions"

// PerplexitySearchTool runs web-aware search prompts via configured Perplexity integrations.
type PerplexitySearchTool struct {
	store  storage.Store
	client *http.Client
}

type PerplexitySearchParams struct {
	Query                  string `json:"query"`
	IntegrationID          string `json:"integration_id,omitempty"`
	IntegrationName        string `json:"integration_name,omitempty"`
	Model                  string `json:"model,omitempty"`
	SearchDomainFilter     string `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter    string `json:"search_recency_filter,omitempty"`
	ReturnCitations        *bool  `json:"return_citations,omitempty"`
	ReturnImages           *bool  `json:"return_images,omitempty"`
	ReturnRelatedQuestions *bool  `json:"return_related_questions,omitempty"`
}

type perplexityChatCompletionRequest struct {
	Model                  string                  `json:"model"`
	Messages               []perplexityChatMessage `json:"messages"`
	ReturnCitations        bool                    `json:"return_citations,omitempty"`
	ReturnImages           bool                    `json:"return_images,omitempty"`
	ReturnRelatedQuestions bool                    `json:"return_related_questions,omitempty"`
	SearchDomainFilter     []string                `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter    string                  `json:"search_recency_filter,omitempty"`
}

type perplexityChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perplexityChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Citations []string `json:"citations"`
}

func NewPerplexitySearchTool(store storage.Store) *PerplexitySearchTool {
	return &PerplexitySearchTool{
		store: store,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (t *PerplexitySearchTool) Name() string {
	return "perplexity_search"
}

func (t *PerplexitySearchTool) Description() string {
	return "Search the web using Perplexity integrations. Returns an answer with optional citations from live web results."
}

func (t *PerplexitySearchTool) Schema() map[string]interface{} {
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
			"model": map[string]interface{}{
				"type":        "string",
				"description": "Perplexity model name (default sonar)",
			},
			"search_domain_filter": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated list of domains to include in web search (optional)",
			},
			"search_recency_filter": map[string]interface{}{
				"type":        "string",
				"description": "Optional recency filter such as day, week, month, or year",
			},
			"return_citations": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to return citations alongside the answer (default true)",
			},
			"return_images": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to ask Perplexity for related images (default false)",
			},
			"return_related_questions": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to ask for related follow-up questions (default false)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *PerplexitySearchTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p PerplexitySearchParams
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
		return &tools.Result{Success: false, Error: "selected perplexity integration is missing api_key"}, nil
	}

	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = strings.TrimSpace(integration.Config["model"])
	}
	if model == "" {
		model = "sonar"
	}

	reqBody := perplexityChatCompletionRequest{
		Model: model,
		Messages: []perplexityChatMessage{
			{Role: "system", Content: "You are a web research assistant. Answer the user's question using current web knowledge and keep the response concise but useful."},
			{Role: "user", Content: query},
		},
		ReturnCitations: true,
	}
	if p.ReturnCitations != nil {
		reqBody.ReturnCitations = *p.ReturnCitations
	}
	if p.ReturnImages != nil {
		reqBody.ReturnImages = *p.ReturnImages
	}
	if p.ReturnRelatedQuestions != nil {
		reqBody.ReturnRelatedQuestions = *p.ReturnRelatedQuestions
	}
	if recency := strings.ToLower(strings.TrimSpace(p.SearchRecencyFilter)); recency != "" {
		reqBody.SearchRecencyFilter = recency
	}
	reqBody.SearchDomainFilter = splitCSV(p.SearchDomainFilter)

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to marshal request: %v", err)}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, perplexityChatCompletionsEndpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("perplexity search request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read perplexity search response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("perplexity API error (status %d): %s", resp.StatusCode, msg)}, nil
	}

	var payload perplexityChatCompletionResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to decode perplexity response: %v", err)}, nil
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "Perplexity Search results for %q\n", query)
	if len(payload.Choices) == 0 {
		out.WriteString("No answer returned.\n")
		return &tools.Result{Success: true, Output: out.String()}, nil
	}

	answer := strings.TrimSpace(payload.Choices[0].Message.Content)
	if answer == "" {
		answer = "(empty answer)"
	}
	fmt.Fprintf(&out, "\nAnswer: %s\n", answer)
	if len(payload.Citations) > 0 {
		out.WriteString("\nCitations:\n")
		for idx, citation := range payload.Citations {
			citation = strings.TrimSpace(citation)
			if citation == "" {
				continue
			}
			fmt.Fprintf(&out, "%d. %s\n", idx+1, citation)
		}
	}

	return &tools.Result{Success: true, Output: out.String()}, nil
}

func (t *PerplexitySearchTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "perplexity" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled perplexity integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("perplexity integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("perplexity integration named %q not found", name)
		default:
			return nil, fmt.Errorf("multiple perplexity integrations matched name %q; pass integration_id", name)
		}
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple perplexity integrations are enabled; pass integration_id or integration_name")
}
