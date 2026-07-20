package claudecli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/llm"
)

const (
	sidecarMsgRunRequest          = "run_request"
	sidecarMsgPermissionRequest   = "permission_request"
	sidecarMsgPermissionResponse  = "permission_response"
	sidecarToolAskUserQuestion    = "AskUserQuestion"
	sidecarStrippedOptionAllowed  = "allowedTools"
	sidecarStrippedOptionAccept   = "acceptEdits"
	sidecarStrippedOptionPermMode = "permissionMode"
)

type sidecarRunRequest struct {
	Type    string                 `json:"type"`
	Prompt  string                 `json:"prompt"`
	Options map[string]interface{} `json:"options"`
}

type sidecarPermissionRequest struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId"`
	ToolUseID string          `json:"toolUseID"`
	ToolName  string          `json:"toolName"`
	Input     json.RawMessage `json:"input"`
	Reason    string          `json:"reason,omitempty"`
	Title     string          `json:"title,omitempty"`
}

type sidecarPermissionResponse struct {
	Type         string                 `json:"type"`
	RequestID    string                 `json:"requestId"`
	Behavior     string                 `json:"behavior"`
	ToolUseID    string                 `json:"toolUseID,omitempty"`
	UpdatedInput map[string]interface{} `json:"updatedInput,omitempty"`
	Answers      map[string]string      `json:"answers,omitempty"`
	Message      string                 `json:"message,omitempty"`
	Interrupt    bool                   `json:"interrupt,omitempty"`
}

type askUserQuestion struct {
	Question    string                  `json:"question"`
	Header      string                  `json:"header,omitempty"`
	Options     []askUserQuestionOption `json:"options,omitempty"`
	MultiSelect bool                    `json:"multiSelect,omitempty"`
}

type askUserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func buildSidecarRunRequest(request *llm.ChatRequest, model string, opts Options, claudePath string) (sidecarRunRequest, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}
	options := map[string]interface{}{
		"cwd":   opts.WorkDir,
		"model": model,
	}
	if claudePath != "" {
		options["pathToClaudeCodeExecutable"] = claudePath
	}
	if envMap := environmentToMap(optionsCommandEnv(opts)); len(envMap) > 0 {
		options["env"] = envMap
	}
	if systemPrompt := buildSystemPrompt(request.SystemPrompt); systemPrompt != "" {
		options["systemPrompt"] = systemPrompt
	}
	if !opts.NoSessionPersistence {
		options["persistSession"] = true
		if raw, ok := ResolveProviderSessionCursor(opts.Identity, request.ProviderSessionCursor); ok && raw != "" {
			options["resume"] = raw
		}
	}
	if tools := sidecarToolsList(request); len(tools) > 0 {
		options["tools"] = tools
	}
	// WHY: auto-approval options would bypass the per-tool bridge before canUseTool runs.
	for _, key := range []string{sidecarStrippedOptionAllowed, sidecarStrippedOptionAccept, sidecarStrippedOptionPermMode} {
		delete(options, key)
	}
	return sidecarRunRequest{
		Type:    sidecarMsgRunRequest,
		Prompt:  buildPrompt(request),
		Options: options,
	}, nil
}

func sidecarToolsList(request *llm.ChatRequest) []string {
	toolsArg, _, includeTools := claudeToolsArgs(request)
	seen := map[string]struct{}{sidecarToolAskUserQuestion: {}}
	out := []string{sidecarToolAskUserQuestion}
	if includeTools && strings.TrimSpace(toolsArg) != "" {
		for _, name := range strings.Split(toolsArg, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func environmentToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func parseSidecarPermissionRequest(line string) (sidecarPermissionRequest, error) {
	var req sidecarPermissionRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return sidecarPermissionRequest{}, fmt.Errorf("failed to parse permission_request: %w", err)
	}
	if req.Type != sidecarMsgPermissionRequest {
		return sidecarPermissionRequest{}, fmt.Errorf("expected %s, got %q", sidecarMsgPermissionRequest, req.Type)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return sidecarPermissionRequest{}, fmt.Errorf("permission_request missing requestId")
	}
	return req, nil
}

func parseSidecarTypedMessage(line string) (map[string]interface{}, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, fmt.Errorf("empty message")
	}
	return msg, nil
}

func sidecarMessageType(msg map[string]interface{}) string {
	raw, _ := msg["type"].(string)
	return strings.TrimSpace(raw)
}

func approvalParamsFromPermissionRequest(sessionID string, req sidecarPermissionRequest) approval.RequestParams {
	toolName := strings.TrimSpace(req.ToolName)
	params := approval.RequestParams{
		SessionID: sessionID,
		ToolUseID: strings.TrimSpace(req.ToolUseID),
		ToolName:  toolName,
		Input:     copyJSONRaw(req.Input),
		Reason:    firstNonEmpty(req.Reason, req.Title),
	}
	if strings.EqualFold(toolName, sidecarToolAskUserQuestion) {
		if ask := askUserPayloadFromPermissionInput(req.Input); ask != nil {
			params.AskUser = ask
		}
	}
	return params
}

func askUserPayloadFromPermissionInput(raw json.RawMessage) *approval.AskUserPayload {
	questions := parseAskUserQuestions(raw)
	if len(questions) == 0 {
		return nil
	}
	first := questions[0]
	suggestions := make([]string, 0, len(first.Options))
	for _, opt := range first.Options {
		if label := strings.TrimSpace(opt.Label); label != "" {
			suggestions = append(suggestions, label)
		}
	}
	return &approval.AskUserPayload{
		Question:    firstNonEmpty(first.Question, first.Header),
		Suggestions: suggestions,
	}
}

func parseAskUserQuestions(raw json.RawMessage) []askUserQuestion {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Questions []askUserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload.Questions
}

func permissionResponseFromDecision(
	req sidecarPermissionRequest,
	result approval.Result,
	decisionErr error,
	resolve ApprovalResolvePayload,
) sidecarPermissionResponse {
	requestID := strings.TrimSpace(req.RequestID)
	toolUseID := strings.TrimSpace(req.ToolUseID)
	originalInput := permissionInputMap(req.Input)
	decision := result.Decision

	switch {
	case decisionErr != nil:
		message := decisionErr.Error()
		interrupt := false
		switch {
		case strings.Contains(message, "cancel"):
			message = "permission request cancelled"
			interrupt = true
		case strings.Contains(message, "timed out"):
			message = "permission request timed out"
		}
		return sidecarPermissionResponse{
			Type:      sidecarMsgPermissionResponse,
			RequestID: requestID,
			Behavior:  "deny",
			ToolUseID: toolUseID,
			Message:   message,
			Interrupt: interrupt,
		}
	case decision == approval.DecisionDeny:
		return sidecarPermissionResponse{
			Type:      sidecarMsgPermissionResponse,
			RequestID: requestID,
			Behavior:  "deny",
			ToolUseID: toolUseID,
			Message:   "denied",
		}
	default:
		resp := sidecarPermissionResponse{
			Type:      sidecarMsgPermissionResponse,
			RequestID: requestID,
			Behavior:  "allow",
			ToolUseID: toolUseID,
		}
		if strings.EqualFold(req.ToolName, sidecarToolAskUserQuestion) {
			answers := resolve.Answers
			if len(answers) == 0 {
				answers = askUserAnswersFromMessage(req.Input, resolve.Message)
			}
			resp.Answers = answers
			resp.UpdatedInput = buildAskUserUpdatedInput(originalInput, answers)
		} else {
			resp.UpdatedInput = originalInput
		}
		return resp
	}
}

func permissionInputMap(raw json.RawMessage) map[string]interface{} {
	out := map[string]interface{}{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out
}

func askUserAnswersFromMessage(input json.RawMessage, message string) map[string]string {
	questions := parseAskUserQuestions(input)
	if len(questions) == 0 {
		return nil
	}
	answer := strings.TrimSpace(message)
	if answer == "" {
		for _, opt := range questions[0].Options {
			if label := strings.TrimSpace(opt.Label); label != "" {
				answer = label
				break
			}
		}
	}
	if answer == "" {
		return nil
	}
	answers := make(map[string]string, len(questions))
	for _, q := range questions {
		key := firstNonEmpty(q.Question, q.Header)
		if key == "" {
			continue
		}
		answers[key] = answer
	}
	return answers
}

func buildAskUserUpdatedInput(original map[string]interface{}, answers map[string]string) map[string]interface{} {
	updated := make(map[string]interface{}, len(original)+1)
	for key, value := range original {
		updated[key] = value
	}
	if len(answers) > 0 {
		updated["answers"] = answers
	}
	if _, ok := original["questions"]; ok {
		updated["questions"] = original["questions"]
	}
	return updated
}

func copyJSONRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	return cp
}

func encodeNDJSONLine(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}
