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
	circleCIDefaultAPIBaseURL = "https://circleci.com"
	circleCIDefaultMaxResults = 20
	circleCIMaxResults        = 100
	circleCIResponseLimit     = 8 * 1024 * 1024
)

// CircleCIQueryTool exposes read-only CircleCI v2 API access through configured
// CircleCI integrations. It intentionally avoids mutation endpoints so agents can
// inspect failing deployments and pipeline state without changing CI state.
type CircleCIQueryTool struct {
	store  storage.Store
	client *http.Client
}

type CircleCIQueryParams struct {
	Operation       string `json:"operation"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	ProjectSlug     string `json:"project_slug,omitempty"`
	PipelineID      string `json:"pipeline_id,omitempty"`
	WorkflowID      string `json:"workflow_id,omitempty"`
	JobNumber       int    `json:"job_number,omitempty"`
	Branch          string `json:"branch,omitempty"`
	CircleToken     string `json:"circle_token,omitempty"`
	StartDate       string `json:"start_date,omitempty"`
	EndDate         string `json:"end_date,omitempty"`
	PageToken       string `json:"page_token,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

type circleCICredentials struct {
	Token   string
	BaseURL string
	Source  string
}

type circleCIListResponse struct {
	Items         []map[string]interface{} `json:"items"`
	NextPageToken string                   `json:"next_page_token"`
}

type circleCIFailedJob struct {
	PipelineID   string                 `json:"pipeline_id"`
	PipelineNum  interface{}            `json:"pipeline_number,omitempty"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name,omitempty"`
	Workflow     map[string]interface{} `json:"workflow,omitempty"`
	Job          map[string]interface{} `json:"job"`
}

func NewCircleCIQueryTool(store storage.Store) *CircleCIQueryTool {
	return &CircleCIQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *CircleCIQueryTool) Name() string {
	return "circleci_query"
}

func (t *CircleCIQueryTool) Description() string {
	return "Query CircleCI using configured integrations. Read-only operations help inspect failing deployments, pipelines, workflows, jobs, artifacts, and tests."
}

func (t *CircleCIQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Read-only operation: me, project_pipelines, pipeline_workflows, workflow_jobs, job_details, job_artifacts, job_tests, or recent_failed_jobs",
				"enum":        []string{"me", "project_pipelines", "pipeline_workflows", "workflow_jobs", "job_details", "job_artifacts", "job_tests", "recent_failed_jobs"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific CircleCI integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific CircleCI integration name to use (optional)",
			},
			"project_slug": map[string]interface{}{
				"type":        "string",
				"description": "CircleCI project slug, for example gh/org/repo, github/org/repo, or bitbucket/org/repo",
			},
			"pipeline_id": map[string]interface{}{
				"type":        "string",
				"description": "Pipeline UUID for pipeline_workflows",
			},
			"workflow_id": map[string]interface{}{
				"type":        "string",
				"description": "Workflow UUID for workflow_jobs",
			},
			"job_number": map[string]interface{}{
				"type":        "integer",
				"description": "CircleCI job number for job_details, job_artifacts, or job_tests",
			},
			"branch": map[string]interface{}{
				"type":        "string",
				"description": "Optional branch filter for project_pipelines/recent_failed_jobs",
			},
			"circle_token": map[string]interface{}{
				"type":        "string",
				"description": "Optional CircleCI token filter supported by project pipeline listing",
			},
			"start_date": map[string]interface{}{
				"type":        "string",
				"description": "Optional RFC3339 start date for job_tests",
			},
			"end_date": map[string]interface{}{
				"type":        "string",
				"description": "Optional RFC3339 end date for job_tests",
			},
			"page_token": map[string]interface{}{
				"type":        "string",
				"description": "CircleCI pagination token",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum rows to request or aggregate (1-100, default 20)",
			},
		},
		"required": []string{"operation"},
	}
}

func (t *CircleCIQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p CircleCIQueryParams
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

func (t *CircleCIQueryTool) executeRead(ctx context.Context, creds circleCICredentials, operation string, p CircleCIQueryParams) (string, error) {
	switch operation {
	case "me":
		return t.doCircleCIGet(ctx, creds, "/api/v2/me", nil)
	case "project_pipelines":
		projectSlug, err := requireCircleCIProjectSlug(p.ProjectSlug)
		if err != nil {
			return "", err
		}
		query := circleCIPaginationQuery(p)
		if branch := strings.TrimSpace(p.Branch); branch != "" {
			query.Set("branch", branch)
		}
		if circleToken := strings.TrimSpace(p.CircleToken); circleToken != "" {
			query.Set("circle-token", circleToken)
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(projectSlug)+"/pipeline", query)
	case "pipeline_workflows":
		pipelineID := strings.TrimSpace(p.PipelineID)
		if pipelineID == "" {
			return "", fmt.Errorf("pipeline_id is required for pipeline_workflows")
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/pipeline/"+url.PathEscape(pipelineID)+"/workflow", circleCIPaginationQuery(p))
	case "workflow_jobs":
		workflowID := strings.TrimSpace(p.WorkflowID)
		if workflowID == "" {
			return "", fmt.Errorf("workflow_id is required for workflow_jobs")
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/workflow/"+url.PathEscape(workflowID)+"/job", circleCIPaginationQuery(p))
	case "job_details":
		projectSlug, jobNumber, err := requireCircleCIJobRef(p)
		if err != nil {
			return "", err
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(projectSlug)+"/job/"+strconv.Itoa(jobNumber), nil)
	case "job_artifacts":
		projectSlug, jobNumber, err := requireCircleCIJobRef(p)
		if err != nil {
			return "", err
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(projectSlug)+"/"+strconv.Itoa(jobNumber)+"/artifacts", circleCIPaginationQuery(p))
	case "job_tests":
		projectSlug, jobNumber, err := requireCircleCIJobRef(p)
		if err != nil {
			return "", err
		}
		query := circleCIPaginationQuery(p)
		if startDate := strings.TrimSpace(p.StartDate); startDate != "" {
			query.Set("start-date", startDate)
		}
		if endDate := strings.TrimSpace(p.EndDate); endDate != "" {
			query.Set("end-date", endDate)
		}
		return t.doCircleCIGet(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(projectSlug)+"/"+strconv.Itoa(jobNumber)+"/tests", query)
	case "recent_failed_jobs":
		return t.recentFailedJobs(ctx, creds, p)
	default:
		return "", fmt.Errorf("unsupported operation %q", operation)
	}
}

func (t *CircleCIQueryTool) recentFailedJobs(ctx context.Context, creds circleCICredentials, p CircleCIQueryParams) (string, error) {
	projectSlug, err := requireCircleCIProjectSlug(p.ProjectSlug)
	if err != nil {
		return "", err
	}
	limit := clampCircleCIMaxResults(p.MaxResults)
	pipelineQuery := url.Values{}
	pipelineQuery.Set("max_results", strconv.Itoa(limit))
	if branch := strings.TrimSpace(p.Branch); branch != "" {
		pipelineQuery.Set("branch", branch)
	}
	if circleToken := strings.TrimSpace(p.CircleToken); circleToken != "" {
		pipelineQuery.Set("circle-token", circleToken)
	}

	pipelineBody, err := t.circleCIGetBytes(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(projectSlug)+"/pipeline", pipelineQuery)
	if err != nil {
		return "", err
	}
	var pipelines circleCIListResponse
	if err := json.Unmarshal(pipelineBody, &pipelines); err != nil {
		return "", fmt.Errorf("failed to decode CircleCI pipelines response: %w", err)
	}

	failedJobs := make([]circleCIFailedJob, 0)
	for _, pipeline := range pipelines.Items {
		if len(failedJobs) >= limit {
			break
		}
		pipelineID := stringFromMap(pipeline, "id")
		if pipelineID == "" {
			continue
		}
		workflowBody, err := t.circleCIGetBytes(ctx, creds, "/api/v2/pipeline/"+url.PathEscape(pipelineID)+"/workflow", nil)
		if err != nil {
			return "", err
		}
		var workflows circleCIListResponse
		if err := json.Unmarshal(workflowBody, &workflows); err != nil {
			return "", fmt.Errorf("failed to decode CircleCI workflows response: %w", err)
		}
		for _, workflow := range workflows.Items {
			if len(failedJobs) >= limit {
				break
			}
			workflowID := stringFromMap(workflow, "id")
			if workflowID == "" {
				continue
			}
			jobsBody, err := t.circleCIGetBytes(ctx, creds, "/api/v2/workflow/"+url.PathEscape(workflowID)+"/job", nil)
			if err != nil {
				return "", err
			}
			var jobs circleCIListResponse
			if err := json.Unmarshal(jobsBody, &jobs); err != nil {
				return "", fmt.Errorf("failed to decode CircleCI jobs response: %w", err)
			}
			for _, job := range jobs.Items {
				if len(failedJobs) >= limit {
					break
				}
				status := strings.ToLower(strings.TrimSpace(stringFromMap(job, "status")))
				if status != "failed" && status != "failing" {
					continue
				}
				failedJobs = append(failedJobs, circleCIFailedJob{
					PipelineID:   pipelineID,
					PipelineNum:  pipeline["number"],
					WorkflowID:   workflowID,
					WorkflowName: stringFromMap(workflow, "name"),
					Workflow:     workflow,
					Job:          job,
				})
			}
		}
	}

	payload := map[string]interface{}{
		"project_slug": projectSlug,
		"failed_jobs":  failedJobs,
		"count":        len(failedJobs),
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format CircleCI failed jobs: %w", err)
	}
	return fmt.Sprintf("CircleCI recent failed jobs from %s\n%s", creds.Source, string(pretty)), nil
}

func (t *CircleCIQueryTool) doCircleCIGet(ctx context.Context, creds circleCICredentials, path string, query url.Values) (string, error) {
	body, err := t.circleCIGetBytes(ctx, creds, path, query)
	if err != nil {
		return "", err
	}
	pretty, err := prettyCircleCIJSON(body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CircleCI %s response from %s\n%s", path, creds.Source, pretty), nil
}

func (t *CircleCIQueryTool) circleCIGetBytes(ctx context.Context, creds circleCICredentials, path string, query url.Values) ([]byte, error) {
	reqURL := creds.BaseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create CircleCI request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Circle-Token", creds.Token)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CircleCI request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, circleCIResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("failed to read CircleCI response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("CircleCI API error (status %d): %s", resp.StatusCode, msg)
	}
	return body, nil
}

func (t *CircleCIQueryTool) resolveCredentials(integrationID string, integrationName string) (circleCICredentials, error) {
	if t.store == nil {
		return circleCICredentials{}, fmt.Errorf("circleci integration is required; configure one in Integrations")
	}
	integration, err := t.selectIntegration(integrationID, integrationName)
	if err != nil {
		return circleCICredentials{}, err
	}
	return circleCICredentialsFromIntegration(integration)
}

func (t *CircleCIQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item != nil && item.Provider == "circleci" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled circleci integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("circleci integration with id %q not found or disabled", id)
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
			return nil, fmt.Errorf("multiple circleci integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("circleci integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple circleci integrations are enabled; pass integration_id or integration_name")
}

func circleCICredentialsFromIntegration(integration *storage.Integration) (circleCICredentials, error) {
	if integration == nil {
		return circleCICredentials{}, fmt.Errorf("integration is required")
	}
	config := integration.Config
	creds := circleCICredentials{
		Token:   strings.TrimSpace(config["api_token"]),
		BaseURL: strings.TrimRight(strings.TrimSpace(config["api_base_url"]), "/"),
		Source:  strings.TrimSpace(integration.Name),
	}
	if creds.BaseURL == "" {
		creds.BaseURL = circleCIDefaultAPIBaseURL
	}
	if creds.Source == "" {
		creds.Source = integration.ID
	}
	if creds.Token == "" {
		return circleCICredentials{}, fmt.Errorf("selected circleci integration is missing api_token")
	}
	parsed, err := url.Parse(creds.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return circleCICredentials{}, fmt.Errorf("circleci api_base_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return circleCICredentials{}, fmt.Errorf("circleci api_base_url must use http or https")
	}
	return creds, nil
}

func requireCircleCIProjectSlug(projectSlug string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(projectSlug), "/")
	if trimmed == "" {
		return "", fmt.Errorf("project_slug is required")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 {
		return "", fmt.Errorf("project_slug must look like gh/org/repo")
	}
	return trimmed, nil
}

func requireCircleCIJobRef(p CircleCIQueryParams) (string, int, error) {
	projectSlug, err := requireCircleCIProjectSlug(p.ProjectSlug)
	if err != nil {
		return "", 0, err
	}
	if p.JobNumber <= 0 {
		return "", 0, fmt.Errorf("job_number is required")
	}
	return projectSlug, p.JobNumber, nil
}

func escapeCircleCIProjectSlug(projectSlug string) string {
	parts := strings.Split(strings.Trim(projectSlug, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func circleCIPaginationQuery(p CircleCIQueryParams) url.Values {
	query := url.Values{}
	query.Set("max_results", strconv.Itoa(clampCircleCIMaxResults(p.MaxResults)))
	if pageToken := strings.TrimSpace(p.PageToken); pageToken != "" {
		query.Set("page-token", pageToken)
	}
	return query
}

func clampCircleCIMaxResults(value int) int {
	if value <= 0 {
		return circleCIDefaultMaxResults
	}
	if value > circleCIMaxResults {
		return circleCIMaxResults
	}
	return value
}

func prettyCircleCIJSON(body []byte) (string, error) {
	var payload interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode CircleCI response: %w", err)
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to format CircleCI response: %w", err)
	}
	return string(pretty), nil
}

func stringFromMap(payload map[string]interface{}, key string) string {
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// Ensure CircleCIQueryTool implements Tool.
var _ tools.Tool = (*CircleCIQueryTool)(nil)
