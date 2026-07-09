package jobs

import (
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestApplyRunConfigToSessionWorkflow(t *testing.T) {
	definition := map[string]interface{}{"id": "wf-1", "name": "Review"}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal workflow definition: %v", err)
	}

	sess := session.New("build")
	job := &storage.RecurringJob{
		RunTarget:       RunTargetWorkflow,
		WorkflowID:      "wf-1",
		WorkflowName:    "Review",
		WorkflowDefJSON: string(definitionJSON),
		LLMProvider:     "openai",
		LLMModel:        "gpt-5.5",
	}

	ApplyRunConfigToSession(sess, job)

	if sess.Metadata["workflow_id"] != "wf-1" {
		t.Fatalf("workflow_id = %#v, want wf-1", sess.Metadata["workflow_id"])
	}
	if sess.Metadata["workflow_name"] != "Review" {
		t.Fatalf("workflow_name = %#v, want Review", sess.Metadata["workflow_name"])
	}
	if sess.Metadata["provider"] != "openai" || sess.Metadata["model"] != "gpt-5.5" {
		t.Fatalf("provider/model = %#v / %#v", sess.Metadata["provider"], sess.Metadata["model"])
	}
	storedDef, ok := sess.Metadata["workflow_definition"].(map[string]interface{})
	if !ok || storedDef["id"] != "wf-1" {
		t.Fatalf("workflow_definition = %#v", sess.Metadata["workflow_definition"])
	}
}

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
