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

const (
	sentryDefaultBaseURL    = "https://sentry.io"
	sentryDefaultMaxResults = 25
	sentryMaxResults        = 100
	sentryResponseLimit     = 8 * 1024 * 1024
)

// SentryQueryTool exposes read-only Sentry API access through configured Sentry
// integrations so agents can investigate issues and events without mutating state.
type SentryQueryTool struct {
	store  storage.Store
	client *http.Client
}

type SentryQueryParams struct {
	Operation       string `json:"operation"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	Project         string `json:"project,omitempty"`
	IssueID         string `json:"issue_id,omitempty"`
	EventID         string `json:"event_id,omitempty"`
	Query           string `json:"query,omitempty"`
	StatsPeriod     string `json:"stats_period,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

type sentryCredentials struct {
	Token   string
	Org     string
	BaseURL string
	Source  string
}

func NewSentryQueryTool(store storage.Store) *SentryQueryTool {
	return &SentryQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *SentryQueryTool) Name() string {
	return "sentry_query"
}

func (t *SentryQueryTool) Description() string {
	return "Query Sentry using a configured integration. Read-only operations help inspect projects, issues, and error events."
}

func (t *SentryQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Read-only operation: list_projects, list_issues, get_issue, list_issue_events, get_event, or latest_event",
				"enum":        []string{"list_projects", "list_issues", "get_issue", "list_issue_events", "get_event", "latest_event"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Sentry integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Sentry integration name to use (optional)",
			},
			"project": map[string]interface{}{
				"type":        "string",
				"description": "Project slug; optional filter for list_issues, required for get_event",
			},
			"issue_id": map[string]interface{}{
				"type":        "string",
				"description": "Issue ID for get_issue, list_issue_events, and latest_event",
			},
			"event_id": map[string]interface{}{
				"type":        "string",
				"description": "Event ID for get_event",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Sentry search query for list_issues (default: is:unresolved)",
			},
			"stats_period": map[string]interface{}{
				"type":        "string",
				"description": "Time window for list_issues, e.g. 24h or 14d",
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Pagination cursor",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum rows to request (1-100, default 25)",
			},
		},
		"required": []string{"operation"},
	}
}

func (t *SentryQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p SentryQueryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	operation := strings.ToLower(strings.TrimSpace(p.Operation))
	if operation == "" {
		return &tools.Result{Success: false, Error: "operation is required"}, nil
	}

	creds, err := t.resolveCredentials(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	output, err := t.executeRead(ctx, creds, operation, p)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: output}, nil
}

func (t *SentryQueryTool) executeRead(ctx context.Context, creds sentryCredentials, operation string, p SentryQueryParams) (string, error) {
	org := url.PathEscape(creds.Org)
	switch operation {
	case "list_projects":
		return t.doSentryGet(ctx, creds, "/api/0/organizations/"+org+"/projects/", sentryPaginationQuery(p))
	case "list_issues":
		query := sentryPaginationQuery(p)
		q := strings.TrimSpace(p.Query)
		if q == "" {
			q = "is:unresolved"
		}
		query.Set("query", q)
		if period := strings.TrimSpace(p.StatsPeriod); period != "" {
			query.Set("statsPeriod", period)
		}
		if project := strings.TrimSpace(p.Project); project != "" {
			return t.doSentryGet(ctx, creds, "/api/0/projects/"+org+"/"+url.PathEscape(project)+"/issues/", query)
		}
		return t.doSentryGet(ctx, creds, "/api/0/organizations/"+org+"/issues/", query)
	case "get_issue":
		issueID, err := requireSentryID("issue_id", p.IssueID)
		if err != nil {
			return "", err
		}
		return t.doSentryGet(ctx, creds, "/api/0/organizations/"+org+"/issues/"+url.PathEscape(issueID)+"/", nil)
	case "list_issue_events":
		issueID, err := requireSentryID("issue_id", p.IssueID)
		if err != nil {
			return "", err
		}
		return t.doSentryGet(ctx, creds, "/api/0/organizations/"+org+"/issues/"+url.PathEscape(issueID)+"/events/", sentryPaginationQuery(p))
	case "latest_event":
		issueID, err := requireSentryID("issue_id", p.IssueID)
		if err != nil {
			return "", err
		}
		return t.doSentryGet(ctx, creds, "/api/0/organizations/"+org+"/issues/"+url.PathEscape(issueID)+"/events/latest/", nil)
	case "get_event":
		project, err := requireSentryID("project", p.Project)
		if err != nil {
			return "", err
		}
		eventID, err := requireSentryID("event_id", p.EventID)
		if err != nil {
			return "", err
		}
		return t.doSentryGet(ctx, creds, "/api/0/projects/"+org+"/"+url.PathEscape(project)+"/events/"+url.PathEscape(eventID)+"/", nil)
	default:
		return "", fmt.Errorf("unsupported operation %q", operation)
	}
}

func (t *SentryQueryTool) doSentryGet(ctx context.Context, creds sentryCredentials, path string, query url.Values) (string, error) {
	body, err := sentryGet(ctx, t.client, creds.BaseURL, creds.Token, path, query)
	if err != nil {
		return "", err
	}
	var payload interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode Sentry response: %w", err)
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format Sentry response: %w", err)
	}
	return fmt.Sprintf("Sentry %s response from %s\n%s", path, creds.Source, pretty), nil
}

// sentryGet performs an authenticated GET against the Sentry API and returns the raw body.
func sentryGet(ctx context.Context, client *http.Client, baseURL, token, path string, query url.Values) ([]byte, error) {
	reqURL := strings.TrimRight(baseURL, "/") + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Sentry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Sentry request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, sentryResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("failed to read Sentry response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("Sentry API error (status %d): %s%s", resp.StatusCode, msg, sentryErrorHint(resp.StatusCode))
	}
	return body, nil
}

func (t *SentryQueryTool) resolveCredentials(integrationID string, integrationName string) (sentryCredentials, error) {
	if t.store == nil {
		return sentryCredentials{}, fmt.Errorf("sentry integration is required; configure one in Integrations")
	}
	integration, err := t.selectIntegration(integrationID, integrationName)
	if err != nil {
		return sentryCredentials{}, err
	}
	return sentryCredentialsFromIntegration(integration)
}

func (t *SentryQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item != nil && item.Provider == "sentry" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled sentry integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("sentry integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple sentry integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("sentry integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple sentry integrations are enabled; pass integration_id or integration_name")
}

// sentryCredentialsFromIntegration validates and normalizes Sentry integration config.
func sentryCredentialsFromIntegration(integration *storage.Integration) (sentryCredentials, error) {
	if integration == nil {
		return sentryCredentials{}, fmt.Errorf("integration is required")
	}
	config := integration.Config
	creds := sentryCredentials{
		Token:   strings.TrimSpace(config["api_token"]),
		Org:     strings.Trim(strings.TrimSpace(config["organization"]), "/"),
		BaseURL: strings.TrimRight(strings.TrimSpace(config["base_url"]), "/"),
		Source:  strings.TrimSpace(integration.Name),
	}
	if creds.BaseURL == "" {
		creds.BaseURL = sentryDefaultBaseURL
	}
	if creds.Source == "" {
		creds.Source = integration.ID
	}
	if creds.Token == "" {
		return sentryCredentials{}, fmt.Errorf("selected sentry integration is missing api_token")
	}
	if creds.Org == "" {
		return sentryCredentials{}, fmt.Errorf("selected sentry integration is missing organization")
	}
	parsed, err := url.Parse(creds.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return sentryCredentials{}, fmt.Errorf("sentry base_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return sentryCredentials{}, fmt.Errorf("sentry base_url must use http or https")
	}
	return creds, nil
}

func requireSentryID(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return trimmed, nil
}

func sentryPaginationQuery(p SentryQueryParams) url.Values {
	query := url.Values{}
	limit := p.MaxResults
	if limit <= 0 {
		limit = sentryDefaultMaxResults
	} else if limit > sentryMaxResults {
		limit = sentryMaxResults
	}
	query.Set("limit", strconv.Itoa(limit))
	if cursor := strings.TrimSpace(p.Cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	return query
}

// sentryErrorHint explains the usual causes of auth failures so the Integrations "Test" result is actionable.
func sentryErrorHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return " (hint: the token is invalid or expired; create a new one)"
	case http.StatusForbidden:
		return " (hint: the token needs the scopes org:read, project:read and event:read, and its user must be a member of the configured organization slug; self-hosted or EU/DE accounts also need Base URL set)"
	case http.StatusNotFound:
		return " (hint: check the organization slug and Base URL)"
	}
	return ""
}

// ProbeSentry checks the token and organization of an integration (saved or not)
// with a read-only project listing; it does not touch the integration store.
func ProbeSentry(ctx context.Context, integration *storage.Integration) error {
	creds, err := sentryCredentialsFromIntegration(integration)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	query := url.Values{}
	query.Set("limit", "1")
	_, err = sentryGet(ctx, client, creds.BaseURL, creds.Token, "/api/0/organizations/"+url.PathEscape(creds.Org)+"/projects/", query)
	return err
}
