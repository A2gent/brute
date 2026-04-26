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

	prompt := composeWorkflowNodePrompt(parent, def, node, "please address the critic feedback", []string{"critic says tests are missing"}, "")

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

func TestComposeWorkflowNodePromptSkipsToolInstructionForResearch(t *testing.T) {
	parent := session.New("parent")
	def := &workflowDefinitionRuntime{Name: "research"}
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Main agent",
		Kind:  "main",
	}

	prompt := composeWorkflowNodePrompt(parent, def, node, "research whether tires are safe indoors", nil, "")

	if strings.Contains(prompt, "inspect relevant files") {
		t.Fatalf("did not expect code tool instruction for research prompt, got: %s", prompt)
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

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "builder" {
		t.Fatalf("expected builder to be ready first, got %#v", ready)
	}

	turnState["builder"].RunCount = 1
	turnState["builder"].LastConsumedByDep["user"] = 1
	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, nil, turnState, sccByNode)
	if len(ready) != 0 {
		t.Fatalf("expected critic to wait while builder has no complete output, got %#v", ready)
	}

	ready = workflowReadyNodes(actionable, preds, map[string]int{"user": 1, "builder": 1}, nil, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "critic" {
		t.Fatalf("expected critic after builder complete output, got %#v", ready)
	}
}

func TestWorkflowReadyNodesRetriesInProgressNode(t *testing.T) {
	nodes := []workflowNodeRuntime{
		{ID: "user", Kind: "user"},
		{ID: "builder", Kind: "main"},
	}
	preds := map[string][]string{
		"builder": {"user"},
	}
	succ := map[string][]string{
		"user": {"builder"},
	}
	sccByNode, _ := workflowSCC(nodes, succ)
	actionable := map[string]workflowNodeRuntime{
		"builder": nodes[1],
	}
	turnState := map[string]*workflowTurnNodeState{
		"builder": {
			RunCount:          1,
			LastConsumedByDep: map[string]int{"user": 1},
		},
	}

	ready := workflowReadyNodes(actionable, preds, map[string]int{"user": 1}, map[string]bool{"builder": true}, turnState, sccByNode)
	if len(ready) != 1 || ready[0].ID != "builder" {
		t.Fatalf("expected in-progress builder to be retried, got %#v", ready)
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

func TestWorkflowNodeWorkStatusForSessionRequiresBuilderToolEvidence(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Builder",
		Kind:  "main",
	}
	child := session.New("build")
	child.AddUserMessage("implement it")
	child.AddAssistantMessage("Done.\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "please fix the button code"); got != "in_progress" {
		t.Fatalf("expected builder without tool activity to be in_progress, got %q", got)
	}

	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-1", Name: "read"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "please fix the button code"); got != "in_progress" {
		t.Fatalf("expected builder with read-only activity to remain in_progress, got %q", got)
	}

	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-2", Name: "edit"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "please fix the button code"); got != "complete" {
		t.Fatalf("expected builder with modification activity to be complete, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionRequiresDeveloperModificationEvidence(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "review-loop__worker",
		Label: "developer",
		Kind:  "subagent",
	}
	child := session.New("build")
	child.AddUserMessage("implement it")
	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-1", Name: "find_files"}})
	child.AddAssistantMessage("Done.\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "implement tracing in code"); got != "in_progress" {
		t.Fatalf("expected developer without modification activity to be in_progress, got %q", got)
	}

	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-2", Name: "replace_lines"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "implement tracing in code"); got != "complete" {
		t.Fatalf("expected developer with modification-capable activity to be complete, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionRetriesFalseToolAccessBlocker(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Main agent",
		Kind:  "main",
	}
	child := session.New("build")
	child.AddUserMessage("implement it")
	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-1", Name: "find_files"}})

	output := "Не могу выполнить доработку: не было ни одного tool-вызова для чтения/редактирования файлов.\n\nNODE_STATUS: BLOCKED"
	if got := workflowNodeWorkStatusForSession(node, output, child, "please implement tracing in go code"); got != "in_progress" {
		t.Fatalf("expected false tool-access blocker to be retried, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionRetriesBareBlockedImplementation(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Main agent",
		Kind:  "main",
	}
	child := session.New("build")
	child.AddUserMessage("implement it")
	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-1", Name: "read"}})

	if got := workflowNodeWorkStatusForSession(node, "NODE_STATUS: BLOCKED", child, "please implement tracing in go code"); got != "in_progress" {
		t.Fatalf("expected bare implementation blocker to be retried, got %q", got)
	}
}

func TestWorkflowStateHasBlockedOrInProgressNode(t *testing.T) {
	state := &workflowRuntimeState{
		Nodes: map[string]*workflowRuntimeNodeState{
			"main": {Status: "in_progress"},
		},
	}
	if !workflowStateHasBlockedOrInProgressNode(state) {
		t.Fatal("expected in-progress node to keep workflow unfinished")
	}
}

func TestWorkflowNodeWorkStatusForSessionAllowsNonCodeMainWithoutTools(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Main agent",
		Kind:  "main",
	}
	child := session.New("research")
	child.AddUserMessage("research whether tires are safe indoors")
	child.AddAssistantMessage("Findings...\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Findings...\nNODE_STATUS: COMPLETE", child, "research whether tires are safe indoors"); got != "complete" {
		t.Fatalf("expected non-code main completion without tool activity to be accepted, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionAllowsCriticWithoutTools(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-critic",
		Label: "Critic",
		Kind:  "subagent",
	}
	child := session.New("build")
	child.AddUserMessage("review it")
	child.AddAssistantMessage("Findings...\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Findings...\nNODE_STATUS: COMPLETE", child, "please review it"); got != "complete" {
		t.Fatalf("expected critic completion without tool activity to be accepted, got %q", got)
	}
}
