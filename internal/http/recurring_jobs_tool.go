package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type recurringJobsTool struct {
	server *Server
}

type recurringJobsParams struct {
	Action string `json:"action"`

	// list
	IncludeDisabled *bool `json:"include_disabled,omitempty"`

	// create
	Name             string `json:"name,omitempty"`
	ScheduleText     string `json:"schedule_text,omitempty"`
	TaskPrompt       string `json:"task_prompt,omitempty"`
	TaskPromptSource string `json:"task_prompt_source,omitempty"` // "text" | "file"
	TaskPromptFile   string `json:"task_prompt_file,omitempty"`
	LLMProvider      string `json:"llm_provider,omitempty"`
	Enabled          *bool  `json:"enabled,omitempty"`

	// delete, run_now
	JobID string `json:"job_id,omitempty"`
}

func newRecurringJobsTool(server *Server) *recurringJobsTool {
	return &recurringJobsTool{server: server}
}

func (t *recurringJobsTool) Name() string {
	return "recurring_jobs"
}

func (t *recurringJobsTool) Description() string {
	return `Manage recurring jobs end-to-end.
Actions:
- list: list recurring jobs
- create: create a new recurring job from natural-language schedule and task prompt
- delete: delete an existing recurring job by id
- run_now: trigger immediate execution of a recurring job by id`
}

func (t *recurringJobsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Operation to perform",
				"enum":        []string{"list", "create", "delete", "run_now"},
			},
			"include_disabled": map[string]interface{}{
				"type":        "boolean",
				"description": "Only for action=list. Include disabled jobs (default: true).",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Required for action=create. Job name.",
			},
			"schedule_text": map[string]interface{}{
				"type":        "string",
				"description": "Required for action=create. Human schedule text (example: every weekday at 9am).",
			},
			"task_prompt": map[string]interface{}{
				"type":        "string",
				"description": "Required for action=create when task_prompt_source is text. Instructions the job should run.",
			},
			"task_prompt_source": map[string]interface{}{
				"type":        "string",
				"description": "Optional for action=create. One of text or file (default: text).",
				"enum":        []string{"text", "file"},
			},
			"task_prompt_file": map[string]interface{}{
				"type":        "string",
				"description": "Required for action=create when task_prompt_source is file. Absolute path to markdown/text prompt file.",
			},
			"llm_provider": map[string]interface{}{
				"type":        "string",
				"description": "Optional for action=create. Provider override for this job.",
			},
			"enabled": map[string]interface{}{
				"type":        "boolean",
				"description": "Optional for action=create. Defaults to true.",
			},
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "Required for action=delete or action=run_now.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *recurringJobsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p recurringJobsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(p.Action)) {
	case "list":
		return t.handleList(p)
	case "create":
		return t.handleCreate(ctx, p)
	case "delete":
		return t.handleDelete(p)
	case "run_now":
		return t.handleRunNow(ctx, p)
	default:
		return &tools.Result{Success: false, Error: "invalid action; expected one of: list, create, delete, run_now"}, nil
	}
}

func (t *recurringJobsTool) handleList(p recurringJobsParams) (*tools.Result, error) {
	includeDisabled := true
	if p.IncludeDisabled != nil {
		includeDisabled = *p.IncludeDisabled
	}

	jobs, err := t.server.store.ListJobs()
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to list jobs: " + err.Error()}, nil
	}

	outJobs := make([]JobResponse, 0, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}
		if !includeDisabled && !job.Enabled {
			continue
		}
		outJobs = append(outJobs, t.server.jobToResponse(job))
	}

	payload := map[string]interface{}{
		"count": len(outJobs),
		"jobs":  outJobs,
	}
	return jsonToolOutput(payload)
}

func (t *recurringJobsTool) handleCreate(ctx context.Context, p recurringJobsParams) (*tools.Result, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return &tools.Result{Success: false, Error: "name is required for action=create"}, nil
	}

	scheduleText := strings.TrimSpace(p.ScheduleText)
	if scheduleText == "" {
		return &tools.Result{Success: false, Error: "schedule_text is required for action=create"}, nil
	}

	taskPromptSource := jobs.NormalizeTaskPromptSource(p.TaskPromptSource)
	taskPromptFile := strings.TrimSpace(p.TaskPromptFile)
	taskPrompt := strings.TrimSpace(p.TaskPrompt)
	if taskPromptSource == jobs.TaskPromptSourceFile {
		if taskPromptFile == "" {
			return &tools.Result{Success: false, Error: "task_prompt_file is required when task_prompt_source=file"}, nil
		}
		taskPrompt = jobs.BuildTaskPromptForFile(taskPromptFile)
	} else if taskPrompt == "" {
		return &tools.Result{Success: false, Error: "task_prompt is required when task_prompt_source=text"}, nil
	}

	llmProvider := normalizeJobLLMProvider(p.LLMProvider)
	if llmProvider != "" {
		if err := t.server.validateProviderRefForExecution(llmProvider); err != nil {
			return &tools.Result{Success: false, Error: "unsupported llm_provider: " + llmProvider + " (" + err.Error() + ")"}, nil
		}
	}

	cronExpr, err := t.server.parseScheduleToCron(ctx, scheduleText)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to parse schedule: " + err.Error()}, nil
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}

	now := time.Now()
	job := &storage.RecurringJob{
		ID:               uuid.New().String(),
		Name:             name,
		ScheduleHuman:    scheduleText,
		ScheduleCron:     cronExpr,
		TaskPrompt:       taskPrompt,
		TaskPromptSource: taskPromptSource,
		TaskPromptFile:   taskPromptFile,
		LLMProvider:      llmProvider,
		Enabled:          enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if nextRun, err := t.server.calculateNextRun(cronExpr, now); err == nil {
		job.NextRunAt = &nextRun
	}

	if err := t.server.store.SaveJob(job); err != nil {
		return &tools.Result{Success: false, Error: "failed to save job: " + err.Error()}, nil
	}

	payload := map[string]interface{}{
		"created": true,
		"job":     t.server.jobToResponse(job),
	}
	return jsonToolOutput(payload)
}

func (t *recurringJobsTool) handleDelete(p recurringJobsParams) (*tools.Result, error) {
	jobID := strings.TrimSpace(p.JobID)
	if jobID == "" {
		return &tools.Result{Success: false, Error: "job_id is required for action=delete"}, nil
	}

	protected, err := t.server.isProtectedThinkingJob(jobID)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to check protected jobs: " + err.Error()}, nil
	}
	if protected {
		return &tools.Result{Success: false, Error: "this job is managed by Thinking settings and cannot be deleted directly"}, nil
	}

	if err := t.server.store.DeleteJob(jobID); err != nil {
		return &tools.Result{Success: false, Error: "failed to delete job: " + err.Error()}, nil
	}

	payload := map[string]interface{}{
		"deleted": true,
		"job_id":  jobID,
	}
	return jsonToolOutput(payload)
}

func (t *recurringJobsTool) handleRunNow(ctx context.Context, p recurringJobsParams) (*tools.Result, error) {
	jobID := strings.TrimSpace(p.JobID)
	if jobID == "" {
		return &tools.Result{Success: false, Error: "job_id is required for action=run_now"}, nil
	}

	job, err := t.server.store.GetJob(jobID)
	if err != nil {
		return &tools.Result{Success: false, Error: "job not found: " + err.Error()}, nil
	}

	exec, err := t.server.executeJob(ctx, job)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to execute job: " + err.Error()}, nil
	}

	payload := map[string]interface{}{
		"triggered":  true,
		"job_id":     jobID,
		"execution":  t.server.executionToResponse(exec),
		"job_status": t.server.jobToResponse(job),
	}
	return jsonToolOutput(payload)
}

func jsonToolOutput(payload map[string]interface{}) (*tools.Result, error) {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}
	return &tools.Result{Success: true, Output: string(body)}, nil
}

var _ tools.Tool = (*recurringJobsTool)(nil)
