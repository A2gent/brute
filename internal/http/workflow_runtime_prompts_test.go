package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

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

func TestComposeWorkflowNodePromptStartsWithNodeInstructions(t *testing.T) {
	parent := session.New("build")
	def := &workflowDefinitionRuntime{Name: "DEV & CRITIC"}
	node := workflowNodeRuntime{
		ID:          "n-main",
		Label:       "Main agent",
		Kind:        "main",
		Instruction: "Implement the requested change before handing off.",
	}

	prompt := composeWorkflowNodePrompt(parent, def, node, "please fix previews", nil, "")

	if !strings.HasPrefix(prompt, "Node instructions:\nImplement the requested change before handing off.") {
		t.Fatalf("expected prompt preview to start with node instructions, got: %s", prompt)
	}
	if !strings.Contains(prompt, "\nWorkflow context:\nYou are executing one node in a multi-agent workflow.") {
		t.Fatalf("expected workflow context after node instructions, got: %s", prompt)
	}
	if strings.Index(prompt, "Node instructions:") > strings.Index(prompt, "You are executing one node in a multi-agent workflow.") {
		t.Fatalf("expected node instructions before workflow context, got: %s", prompt)
	}
}

func TestComposeWorkflowNodePromptForChildFullContextStartsWithNodeInstructions(t *testing.T) {
	parent := session.New("build")
	child := session.New("child")
	def := &workflowDefinitionRuntime{Name: "DEV & CRITIC"}
	node := workflowNodeRuntime{
		ID:          "developer",
		Label:       "Developer",
		Kind:        "subagent",
		Instruction: "Produce or revise the requested work.",
	}

	prompt := composeWorkflowNodePromptForChild(parent, def, node, "please fix previews", nil, "", child, true)

	if !strings.HasPrefix(prompt, "Node instructions:\nProduce or revise the requested work.") {
		t.Fatalf("expected child session prompt preview to start with node instructions, got: %s", prompt)
	}
	if workflowIdx := strings.Index(prompt, "Workflow context:\nYou are executing one node in a multi-agent workflow."); workflowIdx == -1 {
		t.Fatalf("expected workflow context after node instructions, got: %s", prompt)
	} else if workflowIdx < strings.Index(prompt, "Node instructions:") {
		t.Fatalf("expected node instructions before workflow context, got: %s", prompt)
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

func TestComposeWorkflowNodePromptAddsJudgeVerdictInstructionOnlyForJudgeNode(t *testing.T) {
	def := &workflowDefinitionRuntime{
		Name: "review",
		Policy: workflowPolicyRuntime{
			StopCondition: "judge",
			JudgeNodeID:   "critic",
		},
	}

	criticPrompt := composeWorkflowNodePromptWithContext(def, workflowNodeRuntime{ID: "critic", Label: "Critic", Kind: "subagent"}, "review it", nil, "", "", true, false)
	if !strings.Contains(criticPrompt, "Judge node instruction:") || !strings.Contains(criticPrompt, "VERDICT: APPROVED") {
		t.Fatalf("expected judge node prompt to require explicit verdict, got: %s", criticPrompt)
	}

	workerPrompt := composeWorkflowNodePromptWithContext(def, workflowNodeRuntime{ID: "worker", Label: "Worker", Kind: "subagent"}, "implement it", nil, "", "", true, false)
	if strings.Contains(workerPrompt, "Judge node instruction:") || strings.Contains(workerPrompt, "VERDICT: APPROVED") {
		t.Fatalf("expected non-judge node prompt to avoid verdict instruction, got: %s", workerPrompt)
	}
}
