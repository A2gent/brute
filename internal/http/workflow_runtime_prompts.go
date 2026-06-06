package http

import (
	"fmt"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

func workflowNodePromptMessageMetadata(parent *session.Session, def *workflowDefinitionRuntime, node workflowNodeRuntime) map[string]interface{} {
	metadata := map[string]interface{}{
		"internal_handoff":     true,
		"handoff_kind":         "workflow_node",
		"workflow_node_id":     node.ID,
		"workflow_node_label":  node.Label,
		"workflow_node_kind":   node.Kind,
		"workflow_parent_role": "workflow",
	}
	if parent != nil {
		metadata["workflow_parent_id"] = parent.ID
	}
	if def != nil {
		metadata["workflow_id"] = def.ID
		metadata["workflow_name"] = def.Name
	}
	return metadata
}

func workflowReviewLoopWorkerInstruction(loop workflowNodeRuntime) string {
	if inst := strings.TrimSpace(loop.WorkerInstruction); inst != "" {
		return inst
	}
	if inst := strings.TrimSpace(loop.Instruction); inst != "" {
		return inst
	}
	return "Produce the requested work for the review loop. Incorporate critic feedback from prior loop turns before handing off."
}

func workflowReviewLoopReviewerInstruction(loop workflowNodeRuntime) string {
	if inst := strings.TrimSpace(loop.ReviewerInstruction); inst != "" {
		return inst + "\n\nEnd with VERDICT: APPROVED when work is acceptable, otherwise VERDICT: REVISE."
	}
	return "Review the worker output. If it is acceptable, end with VERDICT: APPROVED. Otherwise give concrete revision feedback and end with VERDICT: REVISE."
}

func workflowFinalOutput(def *workflowDefinitionRuntime, outputs map[string]string, succ map[string][]string) string {
	if def != nil && strings.EqualFold(strings.TrimSpace(def.Policy.StopCondition), "judge") {
		judgeID := strings.TrimSpace(def.Policy.JudgeNodeID)
		if judgeID != "" {
			if output := strings.TrimSpace(outputs[judgeID]); output != "" {
				return output
			}
		}
	}
	sinkIDs := make([]string, 0)
	for _, node := range def.Nodes {
		if strings.EqualFold(node.Kind, "user") {
			continue
		}
		if len(succ[node.ID]) == 0 {
			if strings.TrimSpace(outputs[node.ID]) != "" {
				sinkIDs = append(sinkIDs, node.ID)
			}
		}
	}
	if len(sinkIDs) == 0 {
		for _, node := range def.Nodes {
			if strings.EqualFold(node.Kind, "user") {
				continue
			}
			if strings.TrimSpace(outputs[node.ID]) != "" {
				sinkIDs = append(sinkIDs, node.ID)
			}
		}
	}
	sort.Strings(sinkIDs)
	if len(sinkIDs) == 0 {
		return "Workflow completed without output."
	}
	if len(sinkIDs) == 1 {
		return strings.TrimSpace(outputs[sinkIDs[0]])
	}
	parts := make([]string, 0, len(sinkIDs))
	for _, nodeID := range sinkIDs {
		out := strings.TrimSpace(outputs[nodeID])
		if out == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("### %s\n\n%s", nodeID, out))
	}
	if len(parts) == 0 {
		return "Workflow completed without output."
	}
	return strings.Join(parts, "\n\n")
}

func workflowBareStatusRetryPrompt(node workflowNodeRuntime) string {
	label := strings.TrimSpace(node.Label)
	if label == "" {
		label = strings.TrimSpace(node.ID)
	}
	if label == "" {
		label = "this node"
	}
	return fmt.Sprintf("%s previously returned only workflow status without a usable handoff. Continue the actual work now. Do not answer with only `NODE_STATUS`. If this node is responsible for implementation, your next response must first use an editing-capable tool (`edit`, `write`, `replace_lines`, or `insert_lines`) to make a meaningful change, or clearly explain a concrete external blocker unrelated to tool availability. Placeholder files, stubs, TODO-only edits, `bash`, `git diff`, and `git status` do not count as implementation progress.", label)
}

func workflowBlockedFinalOutput(def *workflowDefinitionRuntime, state *workflowRuntimeState) string {
	if state == nil || len(state.Nodes) == 0 {
		return "Workflow paused before completing."
	}
	labels := map[string]string{}
	if def != nil {
		for _, node := range def.Nodes {
			if id := strings.TrimSpace(node.ID); id != "" {
				labels[id] = strings.TrimSpace(node.Label)
			}
		}
	}
	blocked := make([]string, 0)
	details := make([]string, 0)
	for nodeID, node := range state.Nodes {
		if node == nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status != "blocked" && status != "in_progress" && status != "running" {
			continue
		}
		label := strings.TrimSpace(labels[nodeID])
		if label == "" {
			label = nodeID
		}
		blocked = append(blocked, fmt.Sprintf("%s (%s)", label, status))
		if errText := strings.TrimSpace(node.Error); errText != "" {
			details = append(details, fmt.Sprintf("%s: %s", label, errText))
		}
		if previewText := strings.TrimSpace(node.OutputPreview); previewText != "" {
			details = append(details, fmt.Sprintf("%s last output: %s", label, preview(previewText, 500)))
		}
	}
	sort.Strings(blocked)
	if len(blocked) == 0 {
		return "Workflow paused before completing."
	}
	sort.Strings(details)
	message := "Workflow paused before completing. Waiting on: " + strings.Join(blocked, ", ") + "."
	if len(details) > 0 {
		message += "\n\n" + strings.Join(details, "\n")
	}
	return message
}

func composeWorkflowNodePrompt(parent *session.Session, def *workflowDefinitionRuntime, node workflowNodeRuntime, userMessage string, upstreamOutputs []string, previousNodeOutput string) string {
	contextText := workflowParentSessionContext(parent, userMessage, 12, 12000)
	toolEvidenceText := strings.TrimSpace(userMessage + "\n" + contextText)
	return composeWorkflowNodePromptWithContext(def, node, userMessage, upstreamOutputs, previousNodeOutput, contextText, true, workflowNodeRequiresToolEvidence(node, toolEvidenceText))
}

func composeWorkflowNodePromptForChild(parent *session.Session, def *workflowDefinitionRuntime, node workflowNodeRuntime, userMessage string, upstreamOutputs []string, previousNodeOutput string, child *session.Session, fullContext bool) string {
	if fullContext {
		return composeWorkflowNodePrompt(parent, def, node, userMessage, upstreamOutputs, previousNodeOutput)
	}
	return composeWorkflowNodePromptWithContext(def, node, userMessage, upstreamOutputs, previousNodeOutput, "", false, workflowNodeRequiresToolEvidence(node, workflowToolEvidenceText(child, userMessage)))
}

func composeWorkflowNodePromptWithContext(def *workflowDefinitionRuntime, node workflowNodeRuntime, userMessage string, upstreamOutputs []string, previousNodeOutput string, contextText string, fullContext bool, requiresToolEvidence bool) string {
	name := ""
	if def != nil {
		name = strings.TrimSpace(def.Name)
	}
	if name == "" && def != nil {
		name = strings.TrimSpace(def.ID)
	}
	var b strings.Builder
	inst := ""
	if fullContext {
		inst = strings.TrimSpace(node.Instruction)
		if inst != "" {
			b.WriteString("Node instructions:\n")
			b.WriteString(inst)
			b.WriteString("\n")
			if workflowNodeInstructionLooksLikeOrchestrator(inst) {
				b.WriteString("For an orchestration node, create the handoff/plan needed by downstream workflow nodes. Do not implement downstream work yourself. Mark complete when the handoff is ready.\n")
			}
			b.WriteString("\nWorkflow context:\n")
		}
	}
	if fullContext {
		b.WriteString("You are executing one node in a multi-agent workflow.\n")
	} else {
		b.WriteString("You are continuing the same workflow node. Stable workflow context and node instructions were already provided earlier in this child session.\n")
	}
	if name != "" {
		b.WriteString("Workflow: " + name + "\n")
	}
	b.WriteString("Node: " + strings.TrimSpace(node.Label) + "\n")
	if contextText != "" {
		b.WriteString("\nParent session context:\n")
		b.WriteString(contextText)
		b.WriteString("\n")
	}
	b.WriteString("\nCurrent user request:\n")
	b.WriteString(strings.TrimSpace(userMessage))
	b.WriteString("\n")
	if len(upstreamOutputs) > 0 {
		if fullContext {
			b.WriteString("\nInputs from previous nodes:\n")
		} else {
			b.WriteString("\nNew inputs or review feedback since your last turn:\n")
		}
		for idx, item := range upstreamOutputs {
			item = workflowCleanNodeOutputForHandoff(item)
			if strings.TrimSpace(item) == "" {
				continue
			}
			b.WriteString(fmt.Sprintf("\n[%d]\n%s\n", idx+1, strings.TrimSpace(item)))
		}
	}
	previousNodeOutput = workflowCleanNodeOutputForHandoff(previousNodeOutput)
	if strings.TrimSpace(previousNodeOutput) != "" {
		b.WriteString("\nPrevious output from this same node that was not accepted as a complete handoff:\n")
		b.WriteString(strings.TrimSpace(previousNodeOutput))
		b.WriteString("\n")
		if requiresToolEvidence {
			b.WriteString("Continue from that state. Do not repeat the same progress update and do not explain again that edits are needed. Your next step must be to call an editing-capable file tool (`edit`, `write`, `replace_lines`, or `insert_lines`) before returning any final handoff text, unless there is a concrete external blocker unrelated to tool availability.\n")
			b.WriteString("Tools are available in this workflow node. Do not report that you cannot edit merely because the previous response did not include tool calls.\n")
		} else {
			b.WriteString("Continue from that state. Do not repeat the same progress update; perform the remaining work or explain the concrete blocker.\n")
		}
	}
	if def != nil && strings.EqualFold(strings.TrimSpace(def.Policy.StopCondition), "judge") {
		judgeID := strings.TrimSpace(def.Policy.JudgeNodeID)
		if judgeID != "" && judgeID == strings.TrimSpace(node.ID) {
			b.WriteString("\nJudge node instruction:\n")
			b.WriteString("Add a final line exactly as `VERDICT: APPROVED` when work is acceptable, otherwise `VERDICT: REVISE`.\n")
		}
	}
	b.WriteString("\nWorkflow handoff status:\n")
	b.WriteString("Do the node's actual work before handing off. A plan, intention, summary of what you will do, or request to start work is not complete.\n")
	if requiresToolEvidence {
		b.WriteString("For implementation nodes, use the available tools to inspect relevant files and make needed edits before marking complete. A read-only pass is not enough for an implementation request. If code changes are requested and you did not use an editing-capable file tool (`edit`, `write`, `replace_lines`, or `insert_lines`), you must continue with tool calls instead of returning another textual progress update. `bash`, `git diff`, and `git status` can verify work, but they do not count as file edits for workflow completion. A response containing only `NODE_STATUS` is not useful progress.\n")
	}
	b.WriteString("End your response with a final line exactly `NODE_STATUS: COMPLETE` only when this node's concrete deliverable is ready for downstream review or use.\n")
	b.WriteString("Use `NODE_STATUS: IN_PROGRESS` if more implementation work remains, or `NODE_STATUS: BLOCKED` if you cannot proceed without user input or an external dependency.\n")
	b.WriteString("\nReturn only this node's output.")
	return b.String()
}

func workflowCleanNodeOutputForHandoff(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "NODE_STATUS:") || strings.HasPrefix(upper, "VERDICT:") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func workflowLatestMeaningfulAssistantOutput(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		cleaned := workflowCleanNodeOutputForHandoff(msg.Content)
		if strings.TrimSpace(cleaned) != "" {
			return cleaned
		}
	}
	return ""
}

func workflowParentSessionContext(parent *session.Session, currentUserMessage string, maxMessages int, maxChars int) string {
	if parent == nil || len(parent.Messages) == 0 || maxMessages <= 0 || maxChars <= 0 {
		return ""
	}
	messages := parent.Messages
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if strings.EqualFold(strings.TrimSpace(last.Role), "user") && strings.TrimSpace(last.Content) == strings.TrimSpace(currentUserMessage) {
			messages = messages[:len(messages)-1]
		}
	}
	if len(messages) == 0 {
		return ""
	}
	start := len(messages) - maxMessages
	if start < 0 {
		start = 0
	}
	parts := make([]string, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if role == "" || content == "" {
			continue
		}
		switch strings.ToLower(role) {
		case "user":
			role = "User"
		case "assistant":
			role = "Assistant"
			content = workflowCleanNodeOutputForHandoff(content)
			if strings.TrimSpace(content) == "" {
				continue
			}
		case "system":
			role = "System"
		default:
			role = strings.ToUpper(role[:1]) + role[1:]
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, content))
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, "\n\n")
	if len(text) <= maxChars {
		return text
	}
	return strings.TrimSpace(text[len(text)-maxChars:])
}

func preview(text string, max int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= max {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:max]) + "..."
}
