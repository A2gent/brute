package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	jiraDefaultMaxResults = 20
	jiraMaxResults        = 100
	jiraResponseLimit     = 8 * 1024 * 1024
	jiraDefaultFields     = "summary,status,assignee,reporter,created,updated,issuetype,priority,project"
)

// JiraQueryTool exposes read-only Jira Cloud API access via configured integrations
// or JIRA_BASE_URL, JIRA_EMAIL, JIRA_API_TOKEN environment variables.
type JiraQueryTool struct {
	store  storage.Store
	client *http.Client
}

type JiraQueryParams struct {
	Operation       string `json:"operation"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	JQL             string `json:"jql,omitempty"`
	IssueKey        string `json:"issue_key,omitempty"`
	ProjectKey      string `json:"project_key,omitempty"`
	ProjectKeyOrID  string `json:"project_key_or_id,omitempty"`
	BoardID         int    `json:"board_id,omitempty"`
	BoardName       string `json:"board_name,omitempty"`
	BoardType       string `json:"board_type,omitempty"`
	SprintState     string `json:"sprint_state,omitempty"`
	Fields          string `json:"fields,omitempty"`
	Expand          string `json:"expand,omitempty"`
	NextPageToken   string `json:"next_page_token,omitempty"`
	StartAt         int    `json:"start_at,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

type jiraCredentials struct {
	BaseURL string
	Email   string
	Token   string
	Source  string
}

func NewJiraQueryTool(store storage.Store) *JiraQueryTool {
	return &JiraQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *JiraQueryTool) Name() string {
	return "jira_query"
}

func (t *JiraQueryTool) Description() string {
	return "Query Atlassian Jira using configured Jira integrations or JIRA_BASE_URL, JIRA_EMAIL, and JIRA_API_TOKEN env vars. Read-only operations only."
}

func (t *JiraQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Read-only operation to run: myself, search_issues, get_issue, get_projects, get_project, get_boards, or get_sprints",
				"enum":        []string{"myself", "search_issues", "get_issue", "get_projects", "get_project", "get_boards", "get_sprints"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Jira integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Jira integration name to use (optional)",
			},
			"jql": map[string]interface{}{
				"type":        "string",
				"description": "Jira Query Language string for search_issues",
			},
			"issue_key": map[string]interface{}{
				"type":        "string",
				"description": "Issue key for get_issue, for example PROJ-123",
			},
			"project_key": map[string]interface{}{
				"type":        "string",
				"description": "Project key for get_project, for example PROJ",
			},
			"project_key_or_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional project key or ID filter for get_boards",
			},
			"board_id": map[string]interface{}{
				"type":        "integer",
				"description": "Board ID for get_sprints",
			},
			"board_name": map[string]interface{}{
				"type":        "string",
				"description": "Optional board name filter for get_boards",
			},
			"board_type": map[string]interface{}{
				"type":        "string",
				"description": "Optional board type for get_boards",
				"enum":        []string{"scrum", "kanban", "simple"},
			},
			"sprint_state": map[string]interface{}{
				"type":        "string",
				"description": "Optional sprint state filter for get_sprints",
				"enum":        []string{"active", "future", "closed"},
			},
			"fields": map[string]interface{}{
				"type":        "string",
				"description": "Comma-separated issue fields for search_issues/get_issue. Defaults to common summary/status fields. Use *all only when needed.",
			},
			"expand": map[string]interface{}{
				"type":        "string",
				"description": "Optional comma-separated Jira expand values",
			},
			"next_page_token": map[string]interface{}{
				"type":        "string",
				"description": "Next page token returned by search_issues on Jira's /search/jql endpoint",
			},
			"start_at": map[string]interface{}{
				"type":        "integer",
				"description": "Pagination offset for list operations that still use startAt (default 0)",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum results to return for list/search operations (1-100, default 20)",
			},
		},
		"required": []string{"operation"},
	}
}

func (t *JiraQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p JiraQueryParams
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

	payload, err := t.executeRead(ctx, creds, operation, p)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: payload}, nil
}

func (t *JiraQueryTool) executeRead(ctx context.Context, creds jiraCredentials, operation string, p JiraQueryParams) (string, error) {
	switch operation {
	case "myself":
		return t.doJiraGet(ctx, creds, "/rest/api/3/myself", nil)
	case "search_issues":
		jql := strings.TrimSpace(p.JQL)
		if jql == "" {
			return "", fmt.Errorf("jql is required for search_issues")
		}
		body := map[string]interface{}{
			"jql":        jql,
			"maxResults": clampJiraMaxResults(p.MaxResults),
			"fields":     splitCommaList(issueFieldsOrDefault(p.Fields)),
		}
		if expand := strings.TrimSpace(p.Expand); expand != "" {
			body["expand"] = expand
		}
		if nextPageToken := strings.TrimSpace(p.NextPageToken); nextPageToken != "" {
			body["nextPageToken"] = nextPageToken
		}
		return t.doJiraPost(ctx, creds, "/rest/api/3/search/jql", body)
	case "get_issue":
		issueKey := strings.TrimSpace(p.IssueKey)
		if issueKey == "" {
			return "", fmt.Errorf("issue_key is required for get_issue")
		}
		query := url.Values{}
		query.Set("fields", issueFieldsOrDefault(p.Fields))
		if expand := strings.TrimSpace(p.Expand); expand != "" {
			query.Set("expand", expand)
		}
		return t.doJiraGet(ctx, creds, "/rest/api/3/issue/"+url.PathEscape(issueKey), query)
	case "get_projects":
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(nonNegative(p.StartAt)))
		query.Set("maxResults", strconv.Itoa(clampJiraMaxResults(p.MaxResults)))
		return t.doJiraGet(ctx, creds, "/rest/api/3/project/search", query)
	case "get_project":
		projectKey := strings.TrimSpace(p.ProjectKey)
		if projectKey == "" {
			return "", fmt.Errorf("project_key is required for get_project")
		}
		return t.doJiraGet(ctx, creds, "/rest/api/3/project/"+url.PathEscape(projectKey), nil)
	case "get_boards":
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(nonNegative(p.StartAt)))
		query.Set("maxResults", strconv.Itoa(clampJiraMaxResults(p.MaxResults)))
		if project := strings.TrimSpace(p.ProjectKeyOrID); project != "" {
			query.Set("projectKeyOrId", project)
		}
		if name := strings.TrimSpace(p.BoardName); name != "" {
			query.Set("name", name)
		}
		if boardType := strings.ToLower(strings.TrimSpace(p.BoardType)); boardType != "" {
			if boardType != "scrum" && boardType != "kanban" && boardType != "simple" {
				return "", fmt.Errorf("board_type must be one of: scrum, kanban, simple")
			}
			query.Set("type", boardType)
		}
		return t.doJiraGet(ctx, creds, "/rest/agile/1.0/board", query)
	case "get_sprints":
		if p.BoardID <= 0 {
			return "", fmt.Errorf("board_id is required for get_sprints")
		}
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(nonNegative(p.StartAt)))
		query.Set("maxResults", strconv.Itoa(clampJiraMaxResults(p.MaxResults)))
		if state := strings.ToLower(strings.TrimSpace(p.SprintState)); state != "" {
			if state != "active" && state != "future" && state != "closed" {
				return "", fmt.Errorf("sprint_state must be one of: active, future, closed")
			}
			query.Set("state", state)
		}
		return t.doJiraGet(ctx, creds, "/rest/agile/1.0/board/"+strconv.Itoa(p.BoardID)+"/sprint", query)
	default:
		return "", fmt.Errorf("unsupported operation %q", operation)
	}
}

func (t *JiraQueryTool) resolveCredentials(integrationID string, integrationName string) (jiraCredentials, error) {
	if t.store != nil {
		integration, err := t.selectIntegration(integrationID, integrationName)
		if err != nil {
			if strings.TrimSpace(integrationID) != "" || strings.TrimSpace(integrationName) != "" {
				return jiraCredentials{}, err
			}
		} else if integration != nil {
			return credentialsFromIntegration(integration)
		}
	}
	return credentialsFromEnv()
}

func (t *JiraQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item != nil && item.Provider == "jira" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled jira integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("jira integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple jira integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("jira integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple jira integrations are enabled; pass integration_id or integration_name")
}

func credentialsFromIntegration(integration *storage.Integration) (jiraCredentials, error) {
	if integration == nil {
		return jiraCredentials{}, fmt.Errorf("integration is required")
	}
	config := integration.Config
	creds := jiraCredentials{
		BaseURL: strings.TrimSpace(config["base_url"]),
		Email:   strings.TrimSpace(config["email"]),
		Token:   strings.TrimSpace(config["api_token"]),
		Source:  strings.TrimSpace(integration.Name),
	}
	if creds.Source == "" {
		creds.Source = integration.ID
	}
	return validateJiraCredentials(creds)
}

func credentialsFromEnv() (jiraCredentials, error) {
	return validateJiraCredentials(jiraCredentials{
		BaseURL: strings.TrimSpace(os.Getenv("JIRA_BASE_URL")),
		Email:   strings.TrimSpace(os.Getenv("JIRA_EMAIL")),
		Token:   strings.TrimSpace(os.Getenv("JIRA_API_TOKEN")),
		Source:  "environment",
	})
}

func validateJiraCredentials(creds jiraCredentials) (jiraCredentials, error) {
	if creds.BaseURL == "" || creds.Email == "" || creds.Token == "" {
		return jiraCredentials{}, fmt.Errorf("jira credentials are required; configure a jira integration or set JIRA_BASE_URL, JIRA_EMAIL, and JIRA_API_TOKEN")
	}
	parsed, err := url.Parse(creds.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return jiraCredentials{}, fmt.Errorf("jira base_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return jiraCredentials{}, fmt.Errorf("jira base_url must use http or https")
	}
	creds.BaseURL = strings.TrimRight(creds.BaseURL, "/")
	return creds, nil
}

func (t *JiraQueryTool) doJiraGet(ctx context.Context, creds jiraCredentials, path string, query url.Values) (string, error) {
	reqURL := creds.BaseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Jira request: %w", err)
	}
	req.SetBasicAuth(creds.Email, creds.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Jira request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, jiraResponseLimit))
	if err != nil {
		return "", fmt.Errorf("failed to read Jira response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Jira API error (status %d): %s", resp.StatusCode, msg)
	}

	pretty, err := prettyJSON(body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Jira %s response from %s\n%s", path, creds.Source, pretty), nil
}

func (t *JiraQueryTool) doJiraPost(ctx context.Context, creds jiraCredentials, path string, payload interface{}) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode Jira request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, creds.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create Jira request: %w", err)
	}
	req.SetBasicAuth(creds.Email, creds.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Jira request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, jiraResponseLimit))
	if err != nil {
		return "", fmt.Errorf("failed to read Jira response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(responseBody))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Jira API error (status %d): %s", resp.StatusCode, msg)
	}

	pretty, err := prettyJSON(responseBody)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Jira %s response from %s\n%s", path, creds.Source, pretty), nil
}

func prettyJSON(body []byte) (string, error) {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("failed to decode Jira response: %w", err)
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format Jira response: %w", err)
	}
	return string(pretty), nil
}

func clampJiraMaxResults(value int) int {
	if value <= 0 {
		return jiraDefaultMaxResults
	}
	if value > jiraMaxResults {
		return jiraMaxResults
	}
	return value
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func issueFieldsOrDefault(fields string) string {
	trimmed := strings.TrimSpace(fields)
	if trimmed == "" {
		return jiraDefaultFields
	}
	return trimmed
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}
