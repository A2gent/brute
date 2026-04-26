package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestHasRunnableWorkflow(t *testing.T) {
	srv := &Server{}

	t.Run("does not run simple user-main workflow in fan-out mode", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "main", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected simple user-main workflow to run directly in parent session")
		}
	})

	t.Run("runs multi-node workflow on initial turn", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "worker-a", "kind": "subagent"},
					map[string]interface{}{"id": "worker-b", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if !srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected fan-out workflow to run on initial turn")
		}
	})

	t.Run("reruns workflow after assistant has responded", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
					map[string]interface{}{"id": "worker-a", "kind": "subagent"},
					map[string]interface{}{"id": "worker-b", "kind": "main"},
				},
			},
		}
		sess.AddUserMessage("initial request")
		sess.AddAssistantMessage("workflow result", nil)
		sess.AddUserMessage("follow-up question")

		if !srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected workflow fan-out to run on follow-up turns")
		}
	})

	t.Run("ignores workflow definitions without actionable nodes", func(t *testing.T) {
		sess := session.New("build")
		sess.Metadata = map[string]interface{}{
			"workflow_definition": map[string]interface{}{
				"id": "wf-1",
				"nodes": []interface{}{
					map[string]interface{}{"id": "user", "kind": "user"},
				},
			},
		}
		sess.AddUserMessage("initial request")

		if srv.hasRunnableWorkflow(sess) {
			t.Fatalf("expected non-actionable workflow to be skipped")
		}
	})
}

func TestComposeWorkflowNodePromptIncludesParentContext(t *testing.T) {
	parent := session.New("build")
	parent.AddUserMessage("build the feature")
	parent.AddAssistantMessage("implemented first pass", nil)
	parent.AddUserMessage("please address the critic feedback")

	def := &workflowDefinitionRuntime{
		ID:   "wf-critic",
		Name: "User -> Agent <-> Critic",
	}
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Builder",
		Kind:  "main",
	}

	prompt := composeWorkflowNodePrompt(parent, def, node, "please address the critic feedback", []string{"critic says tests are missing"})

	if !strings.Contains(prompt, "Parent session context:") {
		t.Fatalf("expected parent session context in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "User: build the feature") {
		t.Fatalf("expected earlier user turn in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Assistant: implemented first pass") {
		t.Fatalf("expected earlier assistant turn in prompt, got: %s", prompt)
	}
	if strings.Count(prompt, "please address the critic feedback") != 1 {
		t.Fatalf("expected current user request once, got: %s", prompt)
	}
	if !strings.Contains(prompt, "critic says tests are missing") {
		t.Fatalf("expected upstream critic output in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "NODE_STATUS: COMPLETE") {
		t.Fatalf("expected node status handoff contract in prompt, got: %s", prompt)
	}
}

func TestWorkflowReadyNodesWaitsForCompleteUpstream(t *testing.T) {
	nodes := []workflowNodeRuntime{
		{ID: "user", Kind: "user"},
		{ID: "builder", Kind: "main"},
		{ID: "critic", Kind: "subagent"},
	}
	preds := map[string][]string{
		"builder": {"user"},
		"critic":  {"builder"},
	}
	succ := map[string][]string{
		"user":    {"builder"},
		"builder": {"critic"},
	}
	sccByNode, _ := workflowSCC(nodes, succ)
	actionable := map[string]workflowNodeRuntime{
		"builder": nodes[1],
		"critic":  nodes[2],
	}
	turnState := map[string]*workflowTurnNodeState{
		"builder": {LastConsumedByDep: map[string]int{}},
		"critic":  {LastConsumedByDep: map[string]int{}},
	}

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "builder" {
		t.Fatalf("expected builder to be ready first, got %#v", ready)
	}

	turnState["builder"].RunCount = 1
	turnState["builder"].LastConsumedByDep["user"] = 1
	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, turnState, sccByNode)
	if len(ready) != 0 {
		t.Fatalf("expected critic to wait while builder has no complete output, got %#v", ready)
	}

	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1, "builder": 1}, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "critic" {
		t.Fatalf("expected critic after builder complete output, got %#v", ready)
	}
}

func TestWorkflowNodeWorkStatus(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "complete", text: "done\nNODE_STATUS: COMPLETE", want: "complete"},
		{name: "in progress", text: "still editing\nNODE_STATUS: IN_PROGRESS", want: "in_progress"},
		{name: "blocked", text: "need answer\nNODE_STATUS: BLOCKED", want: "blocked"},
		{name: "missing defaults complete", text: "legacy output without status", want: "complete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowNodeWorkStatus(tc.text); got != tc.want {
				t.Fatalf("workflowNodeWorkStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}
