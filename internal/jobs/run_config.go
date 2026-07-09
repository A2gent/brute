package jobs

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const (
	RunTargetWorkflow = "workflow"
	RunTargetAgent    = "agent"
)

func NormalizeRunTarget(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), RunTargetAgent) {
		return RunTargetAgent
	}
	return RunTargetWorkflow
}

// ApplyRunConfigToSession copies loop run target settings onto a job session so
// scheduled/manual executions use the same workflow/agent routing as chat sessions.
func ApplyRunConfigToSession(sess *session.Session, job *storage.RecurringJob) {
	if sess == nil || job == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	provider := strings.TrimSpace(job.LLMProvider)
	model := strings.TrimSpace(job.LLMModel)
	if provider != "" {
		sess.Metadata["provider"] = provider
	}
	if model != "" {
		sess.Metadata["model"] = model
	}

	if NormalizeRunTarget(job.RunTarget) == RunTargetAgent {
		sess.Metadata["launch_target"] = RunTargetAgent
		if launchID := strings.TrimSpace(job.LaunchAgentID); launchID != "" {
			sess.Metadata["launch_agent_id"] = launchID
		}
		if launchName := strings.TrimSpace(job.LaunchAgentName); launchName != "" {
			sess.Metadata["launch_agent_name"] = launchName
		}
		if launchRuntime := strings.TrimSpace(job.LaunchAgentRun); launchRuntime != "" {
			sess.Metadata["launch_agent_runtime"] = launchRuntime
		}
		if unifiedID := strings.TrimSpace(job.UnifiedAgentID); unifiedID != "" {
			sess.Metadata["unified_agent_id"] = unifiedID
		}
		if dockerID := strings.TrimSpace(job.DockerAgentID); dockerID != "" {
			sess.Metadata["docker_agent_id"] = dockerID
		}
		return
	}

	if workflowID := strings.TrimSpace(job.WorkflowID); workflowID != "" {
		sess.Metadata["workflow_id"] = workflowID
	}
	if workflowName := strings.TrimSpace(job.WorkflowName); workflowName != "" {
		sess.Metadata["workflow_name"] = workflowName
	}
	if workflowJSON := strings.TrimSpace(job.WorkflowDefJSON); workflowJSON != "" {
		var definition map[string]interface{}
		if err := json.Unmarshal([]byte(workflowJSON), &definition); err == nil && len(definition) > 0 {
			sess.Metadata["workflow_definition"] = definition
		}
	}
}
