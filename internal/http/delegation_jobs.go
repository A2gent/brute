// delegation_jobs.go runs delegate_to_agent in the background when it is called
// through the CLI MCP bridge. Claude CLI aborts any MCP tools/call after
// MCP_TOOL_TIMEOUT regardless of progress, so a long child run must not be one
// blocking call: the tool returns a job ID and the CLI polls wait_for_agent.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	delegationJobChildWait       = 90 * time.Second
	delegationJobDefaultWait     = 240 * time.Second
	delegationJobMaxWait         = 480 * time.Second
	delegationJobCancelWait      = 10 * time.Second
	delegationJobRetention       = time.Hour
	delegationJobProgressEntries = 5
)

type delegationAsyncKey struct{}

func withDelegationAsync(ctx context.Context) context.Context {
	return context.WithValue(ctx, delegationAsyncKey{}, true)
}

func delegationAsync(ctx context.Context) bool {
	async, _ := ctx.Value(delegationAsyncKey{}).(bool)
	return async
}

type delegationJob struct {
	id              string
	parentSessionID string
	agentID         string
	startedAt       time.Time
	cancel          context.CancelFunc
	done            chan struct{}
	childReady      chan struct{}

	mu             sync.Mutex
	childSessionID string
	progress       []string
	result         *tools.Result
	err            error
	finishedAt     time.Time
}

func (j *delegationJob) observe(ev tools.ProgressEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id, _ := ev.Metadata["child_session_id"].(string); id != "" && j.childSessionID == "" {
		j.childSessionID = id
		close(j.childReady)
	}
	content := strings.TrimSpace(ev.Content)
	if content == "" || (len(j.progress) > 0 && j.progress[len(j.progress)-1] == content) {
		return
	}
	j.progress = append(j.progress, truncateForLog(content, 300))
	if len(j.progress) > delegationJobProgressEntries {
		j.progress = j.progress[len(j.progress)-delegationJobProgressEntries:]
	}
}

func (j *delegationJob) finish(result *tools.Result, err error) {
	j.mu.Lock()
	j.result, j.err, j.finishedAt = result, err, time.Now()
	j.mu.Unlock()
	close(j.done)
}

func (j *delegationJob) finalResult() (*tools.Result, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil || j.result == nil {
		return j.result, j.err
	}
	result := *j.result
	result.Metadata = map[string]interface{}{"job_id": j.id}
	for key, value := range j.result.Metadata {
		result.Metadata[key] = value
	}
	return &result, nil
}

func (j *delegationJob) statusResult(status string) (*tools.Result, error) {
	j.mu.Lock()
	payload := map[string]interface{}{
		"job_id":           j.id,
		"agent_id":         j.agentID,
		"child_session_id": j.childSessionID,
		"status":           status,
		"elapsed_seconds":  int(time.Since(j.startedAt).Seconds()),
		"recent_progress":  append([]string(nil), j.progress...),
	}
	j.mu.Unlock()
	if status == "running" {
		payload["next"] = "Call wait_for_agent with this job_id to get the result."
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode job status: %w", err)
	}
	return &tools.Result{Success: true, Output: string(body), Metadata: map[string]interface{}{
		"job_id":           j.id,
		"child_session_id": payload["child_session_id"],
	}}, nil
}

type delegationJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*delegationJob
}

func (r *delegationJobRegistry) add(job *delegationJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = make(map[string]*delegationJob)
	}
	for id, old := range r.jobs {
		old.mu.Lock()
		stale := !old.finishedAt.IsZero() && time.Since(old.finishedAt) > delegationJobRetention
		old.mu.Unlock()
		if stale {
			delete(r.jobs, id)
		}
	}
	r.jobs[job.id] = job
}

func (r *delegationJobRegistry) get(id, parentSessionID string) *delegationJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[strings.TrimSpace(id)]
	if job == nil || job.parentSessionID != parentSessionID {
		return nil
	}
	return job
}

func (r *delegationJobRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.jobs, id)
	r.mu.Unlock()
}

func (r *delegationJobRegistry) cancelParent(parentSessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.parentSessionID == parentSessionID {
			job.cancel()
		}
	}
}

// startDelegationJob detaches run from the MCP call so the child survives the
// CLI aborting or finishing the call. It returns once the child session exists;
// a run that ends before that (unknown agent, failed start) returns directly.
func (s *Server) startDelegationJob(ctx context.Context, agentID string, run func(context.Context) (*tools.Result, error)) (*tools.Result, error) {
	parentSessionID, _ := ctx.Value("session_id").(string)
	job := &delegationJob{
		id:              "job-" + uuid.NewString(),
		parentSessionID: parentSessionID,
		agentID:         agentID,
		startedAt:       time.Now(),
		done:            make(chan struct{}),
		childReady:      make(chan struct{}),
	}
	parentCtx := context.WithoutCancel(ctx)
	jobCtx, cancel := context.WithCancel(parentCtx)
	job.cancel = cancel
	jobCtx = tools.WithProgressCallback(jobCtx, func(ev tools.ProgressEvent) {
		job.observe(ev)
		tools.ReportProgress(parentCtx, ev)
	})
	s.delegationJobs.add(job)
	go func() {
		defer cancel()
		result, err := run(jobCtx)
		job.finish(result, err)
	}()

	timer := time.NewTimer(delegationJobChildWait)
	defer timer.Stop()
	select {
	case <-job.done:
		s.delegationJobs.remove(job.id)
		return job.finalResult()
	case <-job.childReady:
	case <-timer.C:
	case <-ctx.Done():
	}
	return job.statusResult("running")
}

type waitForAgentTool struct {
	server *Server
}

func (t *waitForAgentTool) Name() string { return "wait_for_agent" }

func (t *waitForAgentTool) Description() string {
	return `Wait for a background delegate_to_agent job. Returns the agent's final response when the job finishes, or status "running" with recent progress when max_wait_seconds passes first; call again to keep waiting.`
}

func (t *waitForAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "job_id returned by delegate_to_agent.",
			},
			"max_wait_seconds": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("How long to block before returning a running status (default %d, max %d).", int(delegationJobDefaultWait.Seconds()), int(delegationJobMaxWait.Seconds())),
			},
		},
		"required": []string{"job_id"},
	}
}

func (t *waitForAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p struct {
		JobID          string `json:"job_id"`
		MaxWaitSeconds int    `json:"max_wait_seconds"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	parentSessionID, _ := ctx.Value("session_id").(string)
	job := t.server.delegationJobs.get(p.JobID, parentSessionID)
	if job == nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("delegation job %q not found in this session", p.JobID)}, nil
	}
	wait := delegationJobDefaultWait
	if p.MaxWaitSeconds > 0 {
		wait = min(time.Duration(p.MaxWaitSeconds)*time.Second, delegationJobMaxWait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-job.done:
		t.server.delegationJobs.remove(job.id)
		return job.finalResult()
	case <-timer.C:
	case <-ctx.Done():
	}
	return job.statusResult("running")
}

type cancelAgentTool struct {
	server *Server
}

func (t *cancelAgentTool) Name() string { return "cancel_agent" }

func (t *cancelAgentTool) Description() string {
	return "Cancel a background delegate_to_agent job and stop its child session."
}

func (t *cancelAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"job_id": map[string]interface{}{
				"type":        "string",
				"description": "job_id returned by delegate_to_agent.",
			},
		},
		"required": []string{"job_id"},
	}
}

func (t *cancelAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	parentSessionID, _ := ctx.Value("session_id").(string)
	job := t.server.delegationJobs.get(p.JobID, parentSessionID)
	if job == nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("delegation job %q not found in this session", p.JobID)}, nil
	}
	job.cancel()
	select {
	case <-job.done:
	case <-time.After(delegationJobCancelWait):
	}
	t.server.delegationJobs.remove(job.id)
	return job.statusResult("canceled")
}

var (
	_ tools.Tool = (*waitForAgentTool)(nil)
	_ tools.Tool = (*cancelAgentTool)(nil)
)
