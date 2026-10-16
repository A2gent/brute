// job_gate.go implements the optional Jev pre-check that lets monitoring-style recurring jobs
// skip the full LLM session when nothing new is actionable.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
)

const (
	jobGateMaxStateChars = 16000
	// Below this noul probability "yes, actionable" is considered unlikely and the run is skipped.
	jobGateRunThreshold = 0.5
)

type jobGateDecision struct {
	Run    bool
	Reason string
}

// evaluateJobGate fetches new items with the job's read-only gate tool and asks Jev (noul)
// whether anything is actionable for the job goal. It fails open: any error runs the job.
func (s *Server) evaluateJobGate(ctx context.Context, job *storage.RecurringJob) jobGateDecision {
	if job == nil || strings.TrimSpace(job.GateTool) == "" {
		return jobGateDecision{Run: true}
	}
	runOnError := func(err error) jobGateDecision {
		logging.Warn("Jev gate for job %s failed, running anyway: %v", job.ID, err)
		return jobGateDecision{Run: true, Reason: "gate error: " + err.Error()}
	}

	client, _ := s.systemOneClient()
	if client == nil {
		return runOnError(fmt.Errorf("no System One classifier configured"))
	}
	params := json.RawMessage(strings.TrimSpace(job.GateParams))
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if job.GateTool == "telegram_user_read_messages" {
		params = withDefaultFalse(params, "advance_cursor")
	}
	result, err := s.toolManager.Execute(ctx, job.GateTool, params)
	if err != nil {
		return runOnError(err)
	}
	if result == nil || !result.Success {
		msg := "gate tool failed"
		if result != nil && result.Error != "" {
			msg = result.Error
		}
		return runOnError(fmt.Errorf("%s", msg))
	}
	items := strings.TrimSpace(result.Output)
	if items == "" {
		return jobGateDecision{Reason: "gate: no new items from " + job.GateTool}
	}
	if len(items) > jobGateMaxStateChars {
		items = items[:jobGateMaxStateChars]
	}

	question := strings.TrimSpace(job.GateQuestion)
	if question == "" {
		question = "Is anything in these new items actionable or urgent for this job goal? Goal: " + strings.TrimSpace(job.TaskPrompt)
	}
	gateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.SystemOne(gateCtx, jev.SystemOneRequest{
		State:     items,
		Questions: map[string]jev.Question{"actionable": {Type: "noul", Instructions: question}},
	})
	if err != nil {
		return runOnError(err)
	}
	answer, ok := resp.Answers["actionable"]
	if !ok || answer.Noul == nil {
		return runOnError(fmt.Errorf("jev returned no noul answer"))
	}
	if *answer.Noul >= jobGateRunThreshold {
		return jobGateDecision{Run: true, Reason: fmt.Sprintf("gate: actionable (noul=%.2f)", *answer.Noul)}
	}
	return jobGateDecision{Reason: fmt.Sprintf("gate: nothing actionable (noul=%.2f)", *answer.Noul)}
}

// recordSkippedJobExecution stores a finished "skipped" execution without creating a session.
func (s *Server) recordSkippedJobExecution(job *storage.RecurringJob, reason string) (*storage.JobExecution, error) {
	now := time.Now()
	exec := &storage.JobExecution{
		ID: uuid.New().String(), JobID: job.ID, Status: "skipped",
		Output: reason, StartedAt: now, FinishedAt: &now,
	}
	if err := s.saveJobExecution(exec); err != nil {
		return nil, fmt.Errorf("failed to record skipped execution: %w", err)
	}
	return exec, nil
}

// withDefaultFalse peeks at Telegram messages without advancing the cursor, so the LLM run
// that follows a positive gate can still read them.
func withDefaultFalse(params json.RawMessage, key string) json.RawMessage {
	m := map[string]any{}
	if err := json.Unmarshal(params, &m); err != nil {
		return params
	}
	if _, ok := m[key]; !ok {
		m[key] = false
	}
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}
