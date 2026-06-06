package http

import (
	"encoding/json"

	"testing"

	"github.com/A2gent/brute/internal/session"
)

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

func TestWorkflowSessionModificationActivityCountInspectsPipelineResults(t *testing.T) {
	child := session.New("worker")
	child.AddAssistantMessage("", []session.ToolCall{
		{
			ID:   "tc-pipeline",
			Name: "pipeline",
			Input: json.RawMessage(`{"steps":[
				{"tool":"write","input":{"path":"src/placeholder.ts","content":"placeholder"}},
				{"tool":"replace_lines","input":{"path":"src/app.ts","start_line":1,"end_line":1,"content":"export const value = 2;"}}
			]}`),
		},
	})
	child.AddToolResult([]session.ToolResult{{
		ToolCallID: "tc-pipeline",
		Name:       "pipeline",
		Content:    `[{"step":1,"tool":"write","success":true,"output":"Created src/placeholder.ts (11 bytes)"},{"step":2,"tool":"replace_lines","success":true,"output":"Updated src/app.ts"}]`,
	}})

	if got := workflowSessionModificationActivityCount(child); got != 1 {
		t.Fatalf("expected only the meaningful nested pipeline modification to count, got %d", got)
	}
}

func TestWorkflowNodeWorkStatusForSessionRetriesEnglishFalseToolAccessBlocker(t *testing.T) {
	node := workflowNodeRuntime{
		ID:    "n-main",
		Label: "Main agent",
		Kind:  "main",
	}
	child := session.New("build")
	child.AddUserMessage("implement it")
	child.AddAssistantMessage("", []session.ToolCall{{ID: "tc-1", Name: "find_files"}})

	output := "I cannot proceed because no tool calls were made for reading or editing files.\n\nNODE_STATUS: BLOCKED"
	if got := workflowNodeWorkStatusForSession(node, output, child, "please implement tracing in go code"); got != "in_progress" {
		t.Fatalf("expected false English tool-access blocker to be retried, got %q", got)
	}
}
