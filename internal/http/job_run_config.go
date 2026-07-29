package http

import (
	"strings"

	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/storage"
)

func applyCreateJobRunConfig(job *storage.RecurringJob, req CreateJobRequest) {
	if job == nil {
		return
	}
	job.RunTarget = jobs.RunTargetAgent
	job.LaunchAgentID = strings.TrimSpace(req.LaunchAgentID)
	job.LaunchAgentName = strings.TrimSpace(req.LaunchAgentName)
	job.LaunchAgentRun = strings.TrimSpace(req.LaunchAgentRuntime)
	job.UnifiedAgentID = strings.TrimSpace(req.UnifiedAgentID)
	job.DockerAgentID = strings.TrimSpace(req.DockerAgentID)
	job.LLMProvider = normalizeJobLLMProvider(req.LLMProvider)
	job.LLMModel = strings.TrimSpace(req.LLMModel)
}

func applyUpdateJobRunConfig(job *storage.RecurringJob, req UpdateJobRequest) {
	if job == nil {
		return
	}
	if strings.TrimSpace(req.RunTarget) != "" {
		job.RunTarget = jobs.RunTargetAgent
		job.LaunchAgentID = strings.TrimSpace(req.LaunchAgentID)
		job.LaunchAgentName = strings.TrimSpace(req.LaunchAgentName)
		job.LaunchAgentRun = strings.TrimSpace(req.LaunchAgentRuntime)
		job.UnifiedAgentID = strings.TrimSpace(req.UnifiedAgentID)
		job.DockerAgentID = strings.TrimSpace(req.DockerAgentID)
	}
	if req.LLMProvider != nil {
		job.LLMProvider = normalizeJobLLMProvider(*req.LLMProvider)
	}
	if strings.TrimSpace(req.RunTarget) != "" || req.LLMModel != "" {
		job.LLMModel = strings.TrimSpace(req.LLMModel)
	}
}
