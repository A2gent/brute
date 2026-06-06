package http

import (
	"encoding/json"

	"strings"

	"github.com/A2gent/brute/internal/session"
)

func workflowNodeWorkStatusForSession(node workflowNodeRuntime, output string, child *session.Session, userMessage string) string {
	status := workflowNodeWorkStatus(output)
	if !workflowNodeRequiresToolEvidence(node, workflowToolEvidenceText(child, userMessage)) {
		return status
	}
	hasModificationActivity := workflowSessionHasModificationActivity(child)
	if status == "blocked" && !hasModificationActivity {
		if workflowOutputLooksLikeToolAvailabilityConfusion(output) || workflowOutputIsBareStatus(output) {
			return "in_progress"
		}
	}
	if status != "complete" {
		return status
	}
	if hasModificationActivity {
		return status
	}
	return "in_progress"
}

func workflowToolEvidenceText(sess *session.Session, userMessage string) string {
	parts := []string{strings.TrimSpace(userMessage)}
	if sess != nil {
		for _, msg := range sess.Messages {
			if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
				continue
			}
			content := strings.TrimSpace(msg.Content)
			if content == "" {
				continue
			}
			parts = append(parts, content)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func workflowNodeRequiresToolEvidence(node workflowNodeRuntime, userMessage string) bool {
	if !workflowRequestLooksLikeToolWork(userMessage) {
		return false
	}
	if workflowNodeInstructionLooksLikeOrchestrator(node.Instruction) {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(node.Kind))
	if kind != "main" && kind != "subagent" {
		return false
	}
	identity := strings.ToLower(strings.TrimSpace(node.Label + " " + node.ID + " " + node.Ref))
	if identity == "" {
		return true
	}
	return strings.Contains(identity, "build") ||
		strings.Contains(identity, "developer") ||
		strings.Contains(identity, "implement") ||
		strings.Contains(identity, "worker") ||
		strings.Contains(identity, "main")
}

func workflowNodeInstructionLooksLikeOrchestrator(instruction string) bool {
	text := strings.ToLower(strings.TrimSpace(instruction))
	if text == "" {
		return false
	}
	orchestrationSignals := []string{
		"orchestrat",
		"coordinate",
		"delegate",
		"handoff",
		"hand off",
		"route",
		"routing",
	}
	for _, signal := range orchestrationSignals {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func workflowRequestLooksLikeToolWork(userMessage string) bool {
	text := strings.ToLower(strings.TrimSpace(userMessage))
	if text == "" {
		return false
	}
	indicators := []string{
		"code",
		"repo",
		"repository",
		"file",
		"files",
		"function",
		"class",
		"component",
		"api",
		"endpoint",
		"database",
		"migration",
		"schema",
		"test",
		"tests",
		"bug",
		"fix",
		"implement",
		"refactor",
		"patch",
		"edit",
		"update",
		"typescript",
		"javascript",
		"react",
		"golang",
		"go ",
		"css",
		"html",
	}
	for _, indicator := range indicators {
		if strings.Contains(text, indicator) {
			return true
		}
	}
	return false
}

func workflowSessionHasModificationActivity(sess *session.Session) bool {
	return workflowSessionModificationActivityCount(sess) > 0
}

func workflowSessionModificationActivityCount(sess *session.Session) int {
	if sess == nil {
		return 0
	}
	count := 0
	callsByID := make(map[string]session.ToolCall)
	for _, msg := range sess.Messages {
		for _, call := range msg.ToolCalls {
			callsByID[call.ID] = call
		}
		for _, result := range msg.ToolResults {
			count += workflowModificationActivityForToolResult(result, callsByID)
		}
	}
	return count
}

func workflowModificationActivityForToolResult(result session.ToolResult, callsByID map[string]session.ToolCall) int {
	if result.IsError {
		return 0
	}
	call := callsByID[result.ToolCallID]
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = strings.TrimSpace(call.Name)
	}
	if strings.EqualFold(name, "parallel") || strings.EqualFold(name, "pipeline") {
		return workflowNestedModificationActivityCount(result, call)
	}
	if !workflowToolCanModifyFiles(name) {
		return 0
	}
	if !workflowToolResultLooksSuccessful(result.Content) {
		return 0
	}
	if !workflowModificationToolCallLooksMeaningful(name, call.Input) {
		return 0
	}
	return 1
}

func workflowNestedModificationActivityCount(result session.ToolResult, call session.ToolCall) int {
	var nestedCalls []struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if len(call.Input) > 0 {
		var params struct {
			Steps []struct {
				Tool  string          `json:"tool"`
				Input json.RawMessage `json:"input"`
			} `json:"steps"`
		}
		if err := json.Unmarshal(call.Input, &params); err == nil {
			for _, step := range params.Steps {
				nestedCalls = append(nestedCalls, struct {
					Tool  string          `json:"tool"`
					Input json.RawMessage `json:"input"`
				}{Tool: step.Tool, Input: step.Input})
			}
		}
	}
	var nestedResults []struct {
		Step    int    `json:"step"`
		Tool    string `json:"tool"`
		Success bool   `json:"success"`
		Output  string `json:"output"`
	}
	if err := json.Unmarshal([]byte(result.Content), &nestedResults); err != nil {
		return 0
	}
	count := 0
	for _, nested := range nestedResults {
		if !nested.Success || !workflowToolCanModifyFiles(nested.Tool) || !workflowToolResultLooksSuccessful(nested.Output) {
			continue
		}
		var input json.RawMessage
		if nested.Step > 0 && nested.Step <= len(nestedCalls) {
			input = nestedCalls[nested.Step-1].Input
		}
		if workflowModificationToolCallLooksMeaningful(nested.Tool, input) {
			count++
		}
	}
	return count
}

func workflowToolResultLooksSuccessful(content string) bool {
	text := strings.ToLower(strings.TrimSpace(content))
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "error:") {
		return false
	}
	return true
}

func workflowModificationToolCallLooksMeaningful(name string, input json.RawMessage) bool {
	if !strings.EqualFold(strings.TrimSpace(name), "write") {
		return true
	}
	if len(input) == 0 {
		return true
	}
	var params struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return true
	}
	return !workflowWriteContentLooksPlaceholder(params.Content)
}

func workflowWriteContentLooksPlaceholder(content string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(content)), " "))
	if normalized == "" {
		return true
	}
	placeholderValues := map[string]bool{
		"placeholder":      true,
		"todo":             true,
		"todo: implement":  true,
		"stub":             true,
		"tbd":              true,
		"not implemented":  true,
		"coming soon":      true,
		"work in progress": true,
		"wip":              true,
	}
	if placeholderValues[normalized] {
		return true
	}
	if len([]rune(normalized)) <= 32 {
		for marker := range placeholderValues {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
	}
	return false
}

func workflowToolCanModifyFiles(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit", "replace_lines", "insert_lines", "write":
		return true
	default:
		return false
	}
}

func workflowOutputLooksLikeToolAvailabilityConfusion(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	toolMentions := []string{
		"no tool call",
		"no tool-call",
		"no tool calls",
		"no tool-calls",
		"не было ни одного tool",
		"не было tool",
		"нужен запуск инструмент",
		"нужны инструменты",
		"нет tool",
	}
	for _, mention := range toolMentions {
		if strings.Contains(text, mention) {
			return true
		}
	}
	return false
}

func workflowOutputIsBareStatus(output string) bool {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	meaningful := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		meaningful++
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "NODE_STATUS:") {
			return false
		}
	}
	return meaningful > 0
}

func workflowNodeWorkStatus(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "NODE_STATUS:") {
			value := strings.TrimSpace(line[len("NODE_STATUS:"):])
			switch strings.ToUpper(value) {
			case "COMPLETE", "COMPLETED", "DONE":
				return "complete"
			case "IN_PROGRESS", "IN PROGRESS", "PROGRESS", "WORKING":
				return "in_progress"
			case "BLOCKED", "WAITING", "NEEDS_INPUT", "NEEDS INPUT":
				return "blocked"
			default:
				return "in_progress"
			}
		}
		break
	}
	return "complete"
}

func workflowJudgeApproved(output string) bool {
	upper := strings.ToUpper(strings.TrimSpace(output))
	if upper == "" {
		return false
	}
	rejectedPhrases := []string{
		"VERDICT: REJECTED",
		"VERDICT: REVISE",
		"VERDICT: REVISION",
		"NOT APPROVED",
		"CANNOT APPROVE",
		"CAN'T APPROVE",
		"NEEDS CHANGES",
		"NEEDS CHANGE",
		"CHANGES REQUESTED",
		"REQUEST CHANGES",
		"REVISION REQUIRED",
		"REQUIRES REVISION",
		"PLEASE FIX",
		"MUST FIX",
		"FIX REQUIRED",
		"TESTS ARE FAILING",
		"FAILING BUILD",
		"BUILD IS FAILING",
		"BUILD FAILED",
		"VERIFICATION FAILED",
		"FOUND BLOCKING ISSUE",
		"FOUND BLOCKING ISSUES",
		"HAS BLOCKING ISSUE",
		"HAS BLOCKING ISSUES",
	}
	for _, phrase := range rejectedPhrases {
		if strings.Contains(upper, phrase) {
			return false
		}
	}
	if strings.Contains(upper, "VERDICT: APPROVED") {
		return true
	}
	approvedPhrases := []string{
		"LGTM",
		"APPROVED",
		"SUCCESSFULLY VERIFIED",
		"VERIFIED AND CONFIRMED",
		"CONFIRMED TO BUILD",
		"BUILD CONFIRMED",
		"NO BLOCKING ISSUES",
	}
	for _, phrase := range approvedPhrases {
		if strings.Contains(upper, phrase) {
			return true
		}
	}
	return true
}
