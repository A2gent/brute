package http

import (
	"encoding/json"
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

func TestComposeWorkflowNodePromptStripsControlLinesFromInputs(t *testing.T) {
	parent := session.New("build")
	parent.AddUserMessage("build the feature")
	parent.AddAssistantMessage("implemented first pass\nNODE_STATUS: COMPLETE", nil)
	parent.AddUserMessage("please review")

	def := &workflowDefinitionRuntime{Name: "review"}
	node := workflowNodeRuntime{
		ID:    "critic",
		Label: "Critic",
		Kind:  "subagent",
	}

	prompt := composeWorkflowNodePrompt(
		parent,
		def,
		node,
		"please review",
		[]string{"worker output\nNODE_STATUS: COMPLETE\nVERDICT: APPROVED"},
		"previous try\nNODE_STATUS: IN_PROGRESS",
	)

	if strings.Contains(prompt, "implemented first pass\nNODE_STATUS: COMPLETE") {
		t.Fatalf("expected parent assistant status line to be stripped, got: %s", prompt)
	}
	if strings.Contains(prompt, "worker output\nNODE_STATUS: COMPLETE") || strings.Contains(prompt, "VERDICT: APPROVED") {
		t.Fatalf("expected upstream control lines to be stripped, got: %s", prompt)
	}
	if strings.Contains(prompt, "previous try\nNODE_STATUS: IN_PROGRESS") {
		t.Fatalf("expected previous output status line to be stripped, got: %s", prompt)
	}
	if !strings.Contains(prompt, "implemented first pass") || !strings.Contains(prompt, "worker output") || !strings.Contains(prompt, "previous try") {
		t.Fatalf("expected semantic output to remain, got: %s", prompt)
	}
	if !strings.Contains(prompt, "End your response with a final line exactly `NODE_STATUS: COMPLETE`") {
		t.Fatalf("expected status contract instructions to remain, got: %s", prompt)
	}
}

func TestComposeWorkflowNodePromptWithContextAllowsNilDefinition(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "worker",
		Label: "Worker",
		Kind:  "main",
	}

	prompt := composeWorkflowNodePromptWithContext(nil, node, "summarize the plan", nil, "", "", false, false)

	if !strings.Contains(prompt, "Node: Worker") {
		t.Fatalf("expected prompt to include node label, got: %s", prompt)
	}
	if strings.Contains(prompt, "Workflow:") {
		t.Fatalf("did not expect empty workflow heading for nil definition, got: %s", prompt)
	}
}

func TestWorkflowCleanNodeOutputForHandoffStripsControlLines(t *testing.T) {
	output := "work done\nNODE_STATUS: COMPLETE\n\nVERDICT: APPROVED\nnext detail"
	clean := workflowCleanNodeOutputForHandoff(output)

	if clean != "work done\n\nnext detail" {
		t.Fatalf("unexpected cleaned output: %q", clean)
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

func TestComposeWorkflowNodePromptUsesParentContextForToolEvidence(t *testing.T) {
	parent := session.New("parent")
	parent.AddUserMessage("please implement tracing in go code")
	parent.AddAssistantMessage("Workflow paused before completing.", nil)
	parent.AddUserMessage("check /Users/artjom/git/a2gent/brute")

	def := &workflowDefinitionRuntime{Name: "build-review"}
	node := workflowNodeRuntime{
		ID:    "review-loop__worker",
		Label: "developer",
		Kind:  "subagent",
	}

	prompt := composeWorkflowNodePrompt(parent, def, node, "check /Users/artjom/git/a2gent/brute", nil, "")

	if !strings.Contains(prompt, "For implementation nodes, use the available tools") {
		t.Fatalf("expected implementation tool guidance from parent context, got: %s", prompt)
	}
}

func TestComposeWorkflowNodeDeltaPromptSkipsStableContext(t *testing.T) {
	parent := session.New("parent")
	parent.AddUserMessage("please implement tracing in go code")
	parent.AddAssistantMessage("large orchestration handoff", nil)
	child := session.New("build")
	child.Metadata = map[string]interface{}{workflowContextSeededKey: true}
	child.AddUserMessage("Initial full workflow prompt: please implement tracing in go code")

	def := &workflowDefinitionRuntime{Name: "build-review"}
	node := workflowNodeRuntime{
		ID:    "review-loop__worker",
		Label: "developer",
		Kind:  "subagent",
	}

	prompt := composeWorkflowNodePromptForChild(parent, def, node, "continue", []string{"critic feedback only"}, "status-only retry", child, false)

	if strings.Contains(prompt, "Parent session context:") || strings.Contains(prompt, "Node instructions:") || strings.Contains(prompt, "large orchestration handoff") {
		t.Fatalf("expected delta prompt to skip stable context, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Stable workflow context and node instructions were already provided earlier") {
		t.Fatalf("expected delta prompt marker, got: %s", prompt)
	}
	if !strings.Contains(prompt, "critic feedback only") {
		t.Fatalf("expected new feedback in delta prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "For implementation nodes, use the available tools") {
		t.Fatalf("expected tool guidance to use child history evidence, got: %s", prompt)
	}
}

func TestComposeWorkflowNodePromptGuidesOrchestratorNode(t *testing.T) {
	parent := session.New("parent")
	def := &workflowDefinitionRuntime{Name: "build-review"}
	node := workflowNodeRuntime{
		ID:          "n-main",
		Label:       "Main agent",
		Kind:        "main",
		Instruction: "Your role is orchestrating user's task with other sub-agents.",
	}

	prompt := composeWorkflowNodePrompt(parent, def, node, "please implement tracing in go code", nil, "")

	if strings.Contains(prompt, "inspect relevant files") {
		t.Fatalf("did not expect implementation tool instruction for orchestrator prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "For an orchestration node, create the handoff/plan needed by downstream workflow nodes") {
		t.Fatalf("expected orchestration handoff guidance, got: %s", prompt)
	}
}

func TestNewWorkflowGraphIgnoresMalformedEdges(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{
			{ID: "user", Kind: "user"},
			{ID: "builder", Kind: "main"},
			{ID: "critic", Kind: "subagent"},
		},
		Edges: []workflowEdgeRuntime{
			{From: "user", To: "builder"},
			{From: "builder", To: "critic"},
			{From: "missing", To: "critic"},
			{From: "builder", To: "missing"},
			{From: "", To: "critic"},
		},
	}

	graph := newWorkflowGraph(def)

	if graph.HasCycle {
		t.Fatal("did not expect an acyclic graph to report a cycle")
	}
	if got := graph.Preds["critic"]; len(got) != 1 || got[0] != "builder" {
		t.Fatalf("expected only valid builder predecessor for critic, got %#v", got)
	}
	if got := graph.Succ["builder"]; len(got) != 1 || got[0] != "critic" {
		t.Fatalf("expected only valid critic successor for builder, got %#v", got)
	}
}

func TestNewWorkflowGraphDetectsSelfCycle(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{{ID: "worker", Kind: "main"}},
		Edges: []workflowEdgeRuntime{{From: "worker", To: "worker"}},
	}

	graph := newWorkflowGraph(def)

	if !graph.HasCycle {
		t.Fatal("expected self-edge to be detected as a cycle")
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

func TestWorkflowNodesBlockedByNeverRunDepsIgnoresCycleInternalDeps(t *testing.T) {
	unreached := []string{"worker", "critic", "reporter"}
	preds := map[string][]string{
		"worker":   {"critic"},
		"critic":   {"worker"},
		"reporter": {"never-ran"},
	}
	sccByNode := map[string]int{
		"worker":    1,
		"critic":    1,
		"reporter":  2,
		"never-ran": 3,
	}

	blocked := workflowNodesBlockedByNeverRunDeps(unreached, preds, map[string]int{"worker": 0, "critic": 0, "reporter": 0}, sccByNode)

	if len(blocked) != 1 || blocked[0] != "reporter" {
		t.Fatalf("expected only reporter to be blocked by an external never-run dep, got %#v", blocked)
	}
	diagnostic := workflowPendingDependencyDiagnostic(blocked, preds, map[string]int{}, sccByNode)
	if !strings.Contains(diagnostic, "reporter<-never-ran") {
		t.Fatalf("expected diagnostic to name missing dependency, got %q", diagnostic)
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
	child.AddToolResult([]session.ToolResult{{ToolCallID: "tc-1", Name: "read", Content: "file content"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "please fix the button code"); got != "in_progress" {
		t.Fatalf("expected builder with read-only activity to remain in_progress, got %q", got)
	}

	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-2", Name: "edit"}})
	child.AddToolResult([]session.ToolResult{{ToolCallID: "tc-2", Name: "edit", Content: "Updated src/Button.tsx"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "please fix the button code"); got != "complete" {
		t.Fatalf("expected builder with modification activity to be complete, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionAllowsOrchestratorWithoutToolEvidence(t *testing.T) {
	node := workflowNodeRuntime{
		ID:          "n-main",
		Label:       "Main agent",
		Kind:        "main",
		Instruction: "Your role is orchestrating user's task with other sub-agents.",
	}
	child := session.New("build")
	child.AddUserMessage("plan the implementation")
	child.AddAssistantMessage("Developer should add timing around tool execution and expose duration in the UI.\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Developer should add timing around tool execution and expose duration in the UI.\nNODE_STATUS: COMPLETE", child, "please implement tracing in go code"); got != "complete" {
		t.Fatalf("expected orchestrator handoff without modification activity to be complete, got %q", got)
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
	child.AddToolResult([]session.ToolResult{{ToolCallID: "tc-1", Name: "find_files", Content: "web-app/src/App.tsx"}})
	child.AddAssistantMessage("Done.\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "implement tracing in code"); got != "in_progress" {
		t.Fatalf("expected developer without modification activity to be in_progress, got %q", got)
	}

	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-2", Name: "replace_lines"}})
	child.AddToolResult([]session.ToolResult{{ToolCallID: "tc-2", Name: "replace_lines", Content: "Updated web-app/src/App.tsx"}})
	if got := workflowNodeWorkStatusForSession(node, "Done.\nNODE_STATUS: COMPLETE", child, "implement tracing in code"); got != "complete" {
		t.Fatalf("expected developer with modification-capable activity to be complete, got %q", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionUsesWorkflowPromptForToolEvidence(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "review-loop__worker",
		Label: "developer",
		Kind:  "subagent",
	}
	child := session.New("build")
	child.AddUserMessage("Original task: please implement tracing in go code.\n\nCurrent user request:\ncheck /Users/artjom/git/a2gent/brute")
	child.AddAssistantMessage("Path exists.\nNODE_STATUS: COMPLETE", nil)

	if got := workflowNodeWorkStatusForSession(node, "Path exists.\nNODE_STATUS: COMPLETE", child, "check /Users/artjom/git/a2gent/brute"); got != "in_progress" {
		t.Fatalf("expected developer without modification activity to remain in_progress from workflow prompt context, got %q", got)
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

func TestWorkflowBlockedFinalOutputExplainsPausedWorkflow(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Nodes: []workflowNodeRuntime{
			{ID: "main", Label: "Main agent"},
			{ID: "worker", Label: "Developer"},
		},
	}
	state := &workflowRuntimeState{
		Nodes: map[string]*workflowRuntimeNodeState{
			"main":   {Status: "in_progress", Error: "Need another pass", OutputPreview: "Partial handoff"},
			"worker": {Status: "pending"},
		},
	}

	got := workflowBlockedFinalOutput(def, state)
	if !strings.Contains(got, "Workflow paused before completing") || !strings.Contains(got, "Main agent (in_progress)") {
		t.Fatalf("unexpected blocked final output: %s", got)
	}
	if !strings.Contains(got, "Need another pass") || !strings.Contains(got, "Partial handoff") {
		t.Fatalf("expected blocked output to include details, got: %s", got)
	}
}

func TestWorkflowBareStatusRetryPromptGuidesContinuation(t *testing.T) {
	got := workflowBareStatusRetryPrompt(workflowNodeRuntime{ID: "worker", Label: "Developer"})

	if !strings.Contains(got, "previously returned only workflow status") || !strings.Contains(got, "editing-capable tool") {
		t.Fatalf("unexpected retry prompt: %s", got)
	}
	if !strings.Contains(got, "Do not answer with only `NODE_STATUS`") || !strings.Contains(got, "Placeholder files") {
		t.Fatalf("unexpected retry prompt: %s", got)
	}
}

func TestComposeWorkflowNodePromptForImplementationRetryRequiresEditTool(t *testing.T) {
	def := &workflowDefinitionRuntime{Name: "review"}
	node := workflowNodeRuntime{
		ID:    "developer",
		Label: "developer",
		Kind:  "subagent",
	}

	prompt := composeWorkflowNodePromptWithContext(def, node, "please implement tracing in code", nil, "I only read files so far.", "", false, true)

	if !strings.Contains(prompt, "Your next step must be to call an editing-capable file tool") {
		t.Fatalf("expected retry prompt to require editing-capable tool, got: %s", prompt)
	}
	if !strings.Contains(prompt, "`bash`, `git diff`, and `git status` can verify work, but they do not count as file edits") {
		t.Fatalf("expected implementation retry to reject bash as editing evidence, got: %s", prompt)
	}
	if strings.Contains(prompt, "perform the remaining work or explain the concrete blocker") {
		t.Fatalf("expected implementation retry to avoid permissive blocker wording, got: %s", prompt)
	}
}

func TestWorkflowLatestMeaningfulAssistantOutputSkipsBareStatus(t *testing.T) {
	child := session.New("worker")
	child.AddAssistantMessage("Useful investigation notes.\nNODE_STATUS: IN_PROGRESS", nil)
	child.AddAssistantMessage("NODE_STATUS: IN_PROGRESS", nil)

	got := workflowLatestMeaningfulAssistantOutput(child)
	if got != "Useful investigation notes." {
		t.Fatalf("expected latest meaningful output, got %q", got)
	}
}

func TestWorkflowSessionModificationActivityCountCountsOnlyEditTools(t *testing.T) {
	child := session.New("worker")
	child.AddAssistantMessage("", []session.ToolCall{
		{ID: "tc-read", Name: "read"},
		{ID: "tc-bash", Name: "bash"},
		{ID: "tc-edit", Name: "edit"},
		{ID: "tc-insert", Name: "insert_lines"},
	})
	child.AddToolResult([]session.ToolResult{
		{ToolCallID: "tc-read", Name: "read", Content: "read ok"},
		{ToolCallID: "tc-bash", Name: "bash", Content: "bash ok"},
		{ToolCallID: "tc-edit", Name: "edit", Content: "Updated src/app.ts"},
		{ToolCallID: "tc-insert", Name: "insert_lines", Content: "Inserted lines"},
	})

	if got := workflowSessionModificationActivityCount(child); got != 2 {
		t.Fatalf("expected 2 modification activities, got %d", got)
	}
}

func TestWorkflowSessionModificationActivityCountIgnoresFailedAndPlaceholderWrites(t *testing.T) {
	child := session.New("worker")
	child.AddAssistantMessage("", []session.ToolCall{
		{ID: "tc-failed", Name: "edit", Input: json.RawMessage(`{"path":"src/app.ts","old_string":"a","new_string":"b"}`)},
		{ID: "tc-placeholder", Name: "write", Input: json.RawMessage(`{"path":"src/GitChangesInline.tsx","content":"placeholder"}`)},
		{ID: "tc-real", Name: "write", Input: json.RawMessage(`{"path":"src/GitChangesInline.tsx","content":"export function GitChangesInline() {\n  return <section>Changed files</section>;\n}\n"}`)},
	})
	child.AddToolResult([]session.ToolResult{
		{ToolCallID: "tc-failed", Name: "edit", Content: "Error: old_string not found", IsError: true},
		{ToolCallID: "tc-placeholder", Name: "write", Content: "Created src/GitChangesInline.tsx (11 bytes)"},
		{ToolCallID: "tc-real", Name: "write", Content: "Updated src/GitChangesInline.tsx"},
	})

	if got := workflowSessionModificationActivityCount(child); got != 1 {
		t.Fatalf("expected only the real write to count, got %d", got)
	}
}

func TestWorkflowSessionModificationActivityCountInspectsParallelResults(t *testing.T) {
	child := session.New("worker")
	child.AddAssistantMessage("", []session.ToolCall{
		{
			ID:   "tc-parallel",
			Name: "parallel",
			Input: json.RawMessage(`{"steps":[
				{"tool":"write","input":{"path":"src/placeholder.ts","content":"placeholder"}},
				{"tool":"replace_lines","input":{"path":"src/app.ts","start_line":1,"end_line":1,"content":"export const value = 1;"}}
			]}`),
		},
	})
	child.AddToolResult([]session.ToolResult{
		{
			ToolCallID: "tc-parallel",
			Name:       "parallel",
			Content:    `[{"step":1,"tool":"write","success":true,"output":"Created src/placeholder.ts (11 bytes)"},{"step":2,"tool":"replace_lines","success":true,"output":"Updated src/app.ts"}]`,
		},
	})

	if got := workflowSessionModificationActivityCount(child); got != 1 {
		t.Fatalf("expected only the meaningful nested modification to count, got %d", got)
	}
}

func TestWorkflowMarkUnfinishedNodesBlockedExplainsTurnCap(t *testing.T) {
	state := &workflowRuntimeState{
		Nodes: map[string]*workflowRuntimeNodeState{
			"worker": {Status: "in_progress", OutputPreview: "Read-only progress"},
			"critic": {Status: "pending"},
		},
	}

	workflowMarkUnfinishedNodesBlocked(state, "turn_cap")

	worker := state.Nodes["worker"]
	if worker.Status != "blocked" {
		t.Fatalf("expected worker to be blocked, got %q", worker.Status)
	}
	if !strings.Contains(worker.Error, "turn limit") {
		t.Fatalf("expected turn-limit error, got %q", worker.Error)
	}
	if state.Nodes["critic"].Status != "pending" {
		t.Fatalf("expected pending node to remain pending, got %q", state.Nodes["critic"].Status)
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

func TestWorkflowJudgeApprovedAcceptsNaturalReviewSuccess(t *testing.T) {
	cases := []string{
		"The implementation has been successfully verified, corrected for performance and layout issues, and confirmed to build.",
		"The codebase changes successfully implemented the architectural plan outlined.",
		"Looks good overall. The worker completed the requested changes.",
		"No blocking issues remain.",
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if !workflowJudgeApproved(tc) {
				t.Fatalf("expected natural review success text to approve the workflow: %q", tc)
			}
		})
	}
}

func TestWorkflowJudgeApprovedRejectsExplicitChangeRequests(t *testing.T) {
	cases := []string{
		"VERDICT: REJECTED\nTests are failing.",
		"VERDICT: REVISE\nPlease address the race condition.",
		"Not approved: please fix the failing build.",
		"Needs changes before this can land.",
		"No blocking issues remain, but changes requested by product.",
		"I found blocking issues in the implementation.",
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if workflowJudgeApproved(tc) {
				t.Fatalf("expected explicit rejection to remain unapproved: %q", tc)
			}
		})
	}
}
