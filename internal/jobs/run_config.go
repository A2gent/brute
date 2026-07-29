package jobs

import (
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const RunTargetAgent = "agent"

func NormalizeRunTarget(string) string {
	return RunTargetAgent
}

// ApplyRunConfigToSession copies direct-agent loop settings onto its execution session.
func ApplyRunConfigToSession(sess *session.Session, job *storage.RecurringJob) {
	if sess == nil || job == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	if provider := strings.TrimSpace(job.LLMProvider); provider != "" {
		sess.Metadata["provider"] = provider
	}
	if model := strings.TrimSpace(job.LLMModel); model != "" {
		sess.Metadata["model"] = model
	}

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
}
