package jobs

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestApplyRunConfigToSessionAgent(t *testing.T) {
	sess := session.New("build")
	job := &storage.RecurringJob{
		RunTarget:       RunTargetAgent,
		LaunchAgentID:   "agent-1",
		LaunchAgentName: "Reviewer",
		LaunchAgentRun:  "docker",
		UnifiedAgentID:  "agent-1",
		LLMProvider:     "openai",
	}

	ApplyRunConfigToSession(sess, job)

	if sess.Metadata["launch_target"] != RunTargetAgent {
		t.Fatalf("launch_target = %#v, want agent", sess.Metadata["launch_target"])
	}
	if sess.Metadata["unified_agent_id"] != "agent-1" {
		t.Fatalf("unified_agent_id = %#v", sess.Metadata["unified_agent_id"])
	}
	if sess.Metadata["launch_agent_name"] != "Reviewer" {
		t.Fatalf("launch_agent_name = %#v", sess.Metadata["launch_agent_name"])
	}
}
