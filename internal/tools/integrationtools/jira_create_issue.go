package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const jiraDefaultIssueType = "Task"

// JiraCreateIssueTool creates Jira issues. It is separate from the read-only
// jira_query so that query access can be granted without write access.
type JiraCreateIssueTool struct {
	query *JiraQueryTool
}

type jiraCreateIssueParams struct {
	IntegrationID     string   `json:"integration_id,omitempty"`
	IntegrationName   string   `json:"integration_name,omitempty"`
	ProjectKey        string   `json:"project_key"`
	Summary           string   `json:"summary"`
	IssueType         string   `json:"issue_type,omitempty"`
	Description       string   `json:"description,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	Priority          string   `json:"priority,omitempty"`
	AssigneeAccountID string   `json:"assignee_account_id,omitempty"`
	ParentKey         string   `json:"parent_key,omitempty"`
}

func NewJiraCreateIssueTool(store storage.Store) *JiraCreateIssueTool {
	return &JiraCreateIssueTool{query: NewJiraQueryTool(store)}
}

func (t *JiraCreateIssueTool) Name() string {
	return "jira_create_issue"
}

func (t *JiraCreateIssueTool) Description() string {
	return "Create a Jira issue (task, bug, story, subtask) using configured Jira integrations or JIRA_BASE_URL, JIRA_EMAIL, and JIRA_API_TOKEN env vars. Use jira_query get_issue_types to find valid issue types for a project."
}

func (t *JiraCreateIssueTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Jira integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Jira integration name to use (optional)",
			},
			"project_key": map[string]interface{}{
				"type":        "string",
				"description": "Project key to create the issue in, for example PROJ",
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "Issue title",
			},
			"issue_type": map[string]interface{}{
				"type":        "string",
				"description": "Issue type name as configured in the project, for example Task, Bug, Story or Subtask (default Task)",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Plain-text description; blank lines separate paragraphs",
			},
			"labels": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Labels to add (no spaces)",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"description": "Priority name, for example High",
			},
			"assignee_account_id": map[string]interface{}{
				"type":        "string",
				"description": "Atlassian account ID of the assignee (see jira_query myself or issue assignee fields)",
			},
			"parent_key": map[string]interface{}{
				"type":        "string",
				"description": "Parent issue key for subtasks or epic children, for example PROJ-1",
			},
		},
		"required": []string{"project_key", "summary"},
	}
}

func (t *JiraCreateIssueTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p jiraCreateIssueParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	fields, err := jiraCreateIssueFields(p)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	creds, err := t.query.resolveCredentials(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	body, err := t.query.doJiraPostRaw(ctx, creds, "/rest/api/3/issue", map[string]interface{}{"fields": fields})
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Key == "" {
		return &tools.Result{Success: false, Error: "Jira did not return the created issue key"}, nil
	}
	browseURL := creds.BaseURL + "/browse/" + created.Key
	return &tools.Result{
		Success:  true,
		Output:   fmt.Sprintf("Created Jira issue %s (id %s) via %s\n%s", created.Key, created.ID, creds.Source, browseURL),
		Metadata: map[string]interface{}{"issue_key": created.Key, "issue_id": created.ID, "url": browseURL},
	}, nil
}

// jiraCreateIssueFields omits unset optional fields: Jira rejects any field,
// even an empty one, that is not on the project's create screen.
func jiraCreateIssueFields(p jiraCreateIssueParams) (map[string]interface{}, error) {
	projectKey := strings.TrimSpace(p.ProjectKey)
	if projectKey == "" {
		return nil, fmt.Errorf("project_key is required")
	}
	summary := strings.TrimSpace(p.Summary)
	if summary == "" {
		return nil, fmt.Errorf("summary is required")
	}
	issueType := strings.TrimSpace(p.IssueType)
	if issueType == "" {
		issueType = jiraDefaultIssueType
	}

	fields := map[string]interface{}{
		"project":   map[string]string{"key": projectKey},
		"summary":   summary,
		"issuetype": map[string]string{"name": issueType},
	}
	if description := strings.TrimSpace(p.Description); description != "" {
		fields["description"] = jiraADFFromText(description)
	}
	var labels []string
	for _, label := range p.Labels {
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) > 0 {
		fields["labels"] = labels
	}
	if priority := strings.TrimSpace(p.Priority); priority != "" {
		fields["priority"] = map[string]string{"name": priority}
	}
	if assignee := strings.TrimSpace(p.AssigneeAccountID); assignee != "" {
		fields["assignee"] = map[string]string{"accountId": assignee}
	}
	if parent := strings.TrimSpace(p.ParentKey); parent != "" {
		fields["parent"] = map[string]string{"key": parent}
	}
	return fields, nil
}

// jiraADFFromText converts plain text into Atlassian Document Format, which
// Jira's v3 API requires for rich-text fields: blank lines start a paragraph,
// single newlines become hard breaks.
func jiraADFFromText(text string) map[string]interface{} {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var paragraphs []interface{}
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var content []interface{}
		for i, line := range strings.Split(block, "\n") {
			if i > 0 {
				content = append(content, map[string]string{"type": "hardBreak"})
			}
			if line != "" {
				content = append(content, map[string]string{"type": "text", "text": line})
			}
		}
		paragraphs = append(paragraphs, map[string]interface{}{"type": "paragraph", "content": content})
	}
	return map[string]interface{}{"type": "doc", "version": 1, "content": paragraphs}
}
