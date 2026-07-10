package http

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/storage"
)

func workflowDefinitionJSON(def map[string]interface{}) string {
	if len(def) == 0 {
		return ""
	}
	body, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	return string(body)
}

func workflowDefinitionFromJSON(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var def map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return nil
	}
	return def
}

func applyCreateJobRunConfig(job *storage.RecurringJob, req CreateJobRequest) {
	if job == nil {
		return
	}
	job.RunTarget = jobs.NormalizeRunTarget(req.RunTarget)
	if job.RunTarget == jobs.RunTargetAgent {
		job.WorkflowID = ""
		job.WorkflowName = ""
		job.WorkflowDefJSON = ""
		job.LaunchAgentID = strings.TrimSpace(req.LaunchAgentID)
		job.LaunchAgentName = strings.TrimSpace(req.LaunchAgentName)
		job.LaunchAgentRun = strings.TrimSpace(req.LaunchAgentRuntime)
		job.UnifiedAgentID = strings.TrimSpace(req.UnifiedAgentID)
		job.DockerAgentID = strings.TrimSpace(req.DockerAgentID)
	} else {
		job.LaunchAgentID = ""
		job.LaunchAgentName = ""
		job.LaunchAgentRun = ""
		job.UnifiedAgentID = ""
		job.DockerAgentID = ""
		job.WorkflowID = strings.TrimSpace(req.WorkflowID)
		job.WorkflowName = strings.TrimSpace(req.WorkflowName)
		job.WorkflowDefJSON = workflowDefinitionJSON(req.WorkflowDefinition)
	}
	job.LLMProvider = normalizeJobLLMProvider(req.LLMProvider)
	job.LLMModel = strings.TrimSpace(req.LLMModel)
}

func applyUpdateJobRunConfig(job *storage.RecurringJob, req UpdateJobRequest) {
	if job == nil {
		return
	}
	if strings.TrimSpace(req.RunTarget) != "" {
		job.RunTarget = jobs.NormalizeRunTarget(req.RunTarget)
	}
	if strings.TrimSpace(req.RunTarget) == "" {
		if req.LLMProvider != nil {
			job.LLMProvider = normalizeJobLLMProvider(*req.LLMProvider)
		}
		if req.LLMModel != "" {
			job.LLMModel = strings.TrimSpace(req.LLMModel)
		}
		return
	}

	if job.RunTarget == jobs.RunTargetAgent {
		job.WorkflowID = ""
		job.WorkflowName = ""
		job.WorkflowDefJSON = ""
		job.LaunchAgentID = strings.TrimSpace(req.LaunchAgentID)
		job.LaunchAgentName = strings.TrimSpace(req.LaunchAgentName)
		job.LaunchAgentRun = strings.TrimSpace(req.LaunchAgentRuntime)
		job.UnifiedAgentID = strings.TrimSpace(req.UnifiedAgentID)
		job.DockerAgentID = strings.TrimSpace(req.DockerAgentID)
	} else {
		job.LaunchAgentID = ""
		job.LaunchAgentName = ""
		job.LaunchAgentRun = ""
		job.UnifiedAgentID = ""
		job.DockerAgentID = ""
		job.WorkflowID = strings.TrimSpace(req.WorkflowID)
		job.WorkflowName = strings.TrimSpace(req.WorkflowName)
		if req.WorkflowDefinition != nil {
			job.WorkflowDefJSON = workflowDefinitionJSON(*req.WorkflowDefinition)
		}
	}
	if req.LLMProvider != nil {
		job.LLMProvider = normalizeJobLLMProvider(*req.LLMProvider)
	}
	job.LLMModel = strings.TrimSpace(req.LLMModel)
}
