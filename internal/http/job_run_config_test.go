package http

import (
	"testing"

	"github.com/A2gent/brute/internal/jobs"
	"github.com/A2gent/brute/internal/storage"
)

func TestApplyCreateJobRunConfigPersistsProviderAndModel(t *testing.T) {
	job := &storage.RecurringJob{}
	applyCreateJobRunConfig(job, CreateJobRequest{
		RunTarget:          jobs.RunTargetAgent,
		LaunchAgentID:      "builtin:host-agent",
		LaunchAgentName:    "Built-in agent on host",
		LaunchAgentRuntime: "host",
		LLMProvider:        "lmstudio",
		LLMModel:           "ornith",
	})

	if job.LLMProvider != "lmstudio" {
		t.Fatalf("LLMProvider = %q, want lmstudio", job.LLMProvider)
	}
	if job.LLMModel != "ornith" {
		t.Fatalf("LLMModel = %q, want ornith", job.LLMModel)
	}
	if job.RunTarget != jobs.RunTargetAgent {
		t.Fatalf("RunTarget = %q, want agent", job.RunTarget)
	}
	if job.LaunchAgentID != "builtin:host-agent" {
		t.Fatalf("LaunchAgentID = %q", job.LaunchAgentID)
	}
}

func TestApplyUpdateJobRunConfigPersistsProviderAndModel(t *testing.T) {
	job := &storage.RecurringJob{
		RunTarget:   jobs.RunTargetAgent,
		LLMProvider: "openai",
		LLMModel:    "gpt-5.5",
	}
	provider := "lmstudio"
	applyUpdateJobRunConfig(job, UpdateJobRequest{
		RunTarget:     jobs.RunTargetAgent,
		LaunchAgentID: "builtin:host-agent",
		LLMProvider:   &provider,
		LLMModel:      "ornith",
	})

	if job.LLMProvider != "lmstudio" {
		t.Fatalf("LLMProvider = %q, want lmstudio", job.LLMProvider)
	}
	if job.LLMModel != "ornith" {
		t.Fatalf("LLMModel = %q, want ornith", job.LLMModel)
	}
	if job.RunTarget != jobs.RunTargetAgent {
		t.Fatalf("RunTarget = %q, want agent", job.RunTarget)
	}
}

func TestApplyUpdateJobRunConfigProviderOnlyWithoutRunTarget(t *testing.T) {
	job := &storage.RecurringJob{
		RunTarget:   jobs.RunTargetAgent,
		LLMProvider: "openai",
		LLMModel:    "gpt-5.5",
	}
	provider := "lmstudio"
	applyUpdateJobRunConfig(job, UpdateJobRequest{
		LLMProvider: &provider,
		LLMModel:    "ornith",
	})

	if job.LLMProvider != "lmstudio" {
		t.Fatalf("LLMProvider = %q, want lmstudio", job.LLMProvider)
	}
	if job.LLMModel != "ornith" {
		t.Fatalf("LLMModel = %q, want ornith", job.LLMModel)
	}
}
