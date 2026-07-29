package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agent"
	httpserver "github.com/A2gent/brute/internal/http"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

const tuiLiveStreamMetadataKey = "tui_live_stream"

func (m Model) updateTick() (Model, []tea.Cmd) {
	if m.processing {
		m.loadingIndex = (m.loadingIndex + 1) % len(m.loadingFrames)
	}
	if m.showLogsView {
		m.refreshLogsView()
	}
	return m, []tea.Cmd{tickCmd(), updateMemoryCmd()}
}

func (m Model) updateMemory(msg memoryUpdateMsg) Model {
	m.memoryMB = msg.memoryMB
	return m
}

func (m Model) updateServerPort(msg serverPortMsg) Model {
	m.serverPort = msg.port
	return m
}

func (m Model) updateSessionSync(msg sessionSyncMsg) (Model, []tea.Cmd) {
	cmds := make([]tea.Cmd, 0, 1)
	if msg.session != nil {
		if msg.session.Status == session.StatusInputRequired && !m.showQuestionPrompt {
			if question, err := m.sessionManager.GetPendingQuestion(msg.session.ID); err == nil && question != nil {
				m.pendingQuestion = question
				m.showQuestionPrompt = true
				m.questionOptionIndex = 0
				m.processing = false
				m.updateViewportHeight()
			}
		}

		if shouldApplySessionSync(m, msg.session) {
			m.applySyncedSession(msg.session)
		} else {
			m.session = msg.session
		}
	}
	cmds = append(cmds, sessionSyncCmd(m.sessionManager, m.session.ID))
	return m, cmds
}

func (m Model) updateAgentResponse(msg agentResponseMsg) (Model, []tea.Cmd) {
	cmds := []tea.Cmd{}
	logging.Debug("TUI received agentResponseMsg: done=%v err=%v tokens=%d/%d", msg.done, msg.err != nil, msg.inputTokens, msg.outputTokens)

	m.totalInputTokens += msg.inputTokens
	m.totalOutputTokens += msg.outputTokens

	if msg.err != nil {
		m.processing = false
		m.cancelFunc = nil
		m.cancelPending = false
		m.activeRunStatus = ""
		m.activeRunDetail = ""
		m.messages = append(m.messages, message{
			role:      "error",
			content:   msg.err.Error(),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, cmds
	}

	if !msg.done {
		return m, cmds
	}

	m.processing = false
	m.cancelFunc = nil
	m.cancelPending = false
	m.activeRunStatus = ""
	m.activeRunDetail = ""
	logging.Debug("TUI: Agent done, processing=%v queuedMessages=%d", m.processing, len(m.queuedMessages))

	if freshSess, err := m.sessionManager.Get(m.session.ID); err == nil {
		m.applySyncedSession(freshSess)
		if freshSess.Status == session.StatusInputRequired {
			if question, qErr := m.sessionManager.GetPendingQuestion(freshSess.ID); qErr == nil && question != nil {
				m.pendingQuestion = question
				m.showQuestionPrompt = true
				m.questionOptionIndex = 0
				logging.Debug("TUI: Loaded pending question: %s", question.Header)
				m.updateViewportHeight()
			}
		}
	}

	if msg.content != "" && !lastAssistantContentMatches(m.messages, msg.content) {
		m.messages = append(m.messages, message{
			role:      "assistant",
			content:   msg.content,
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
	}
	m.lastSyncedMessageCount = len(m.session.Messages)

	if len(m.queuedMessages) > 0 {
		nextInput := m.queuedMessages[0]
		m.queuedMessages = m.queuedMessages[1:]

		for i := range m.messages {
			if m.messages[i].role == "queued" && m.messages[i].content == nextInput {
				m.messages[i].role = "user"
				m.messages[i].timestamp = time.Now()
				break
			}
		}

		m.session.AddUserMessage(nextInput)
		if userMsg := m.session.GetLastMessage(); userMsg != nil {
			for i := range m.messages {
				if m.messages[i].role == "user" && m.messages[i].content == nextInput && strings.TrimSpace(m.messages[i].id) == "" {
					m.messages[i].id = userMsg.ID
					m.messages[i].timestamp = userMsg.Timestamp
					break
				}
			}
		}
		m.lastUserInputTime = time.Now()
		m.processing = true
		m.activeRunStatus = "Sending request"
		m.activeRunDetail = ""
		m.session.SetStatus(session.StatusRunning)
		_ = m.sessionManager.Save(m.session)
		m.lastSyncedMessageCount = len(m.session.Messages)
		m.lastSyncedSessionUpdatedAt = m.session.UpdatedAt
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		cmd, cancel := m.runAgent(nextInput)
		m.cancelFunc = cancel
		m.cancelPending = false
		cmds = append(cmds, cmd)
	}

	return m, cmds
}

func (m Model) updateAgentEvent(ev agent.Event) Model {
	switch ev.Type {
	case agent.EventAssistantDelta:
		m.activeRunStatus = "Receiving response"
		m.appendAssistantDelta(ev.Delta)
	case agent.EventToolExecuting:
		m.activeRunStatus = toolExecutingStatus(ev.Step, len(ev.ToolCalls))
		m.activeRunDetail = summarizeAgentToolCalls(ev.ToolCalls)
		m.attachToolCallsToLiveAssistant(ev.ToolCalls)
	case agent.EventToolProgress:
		if ev.ToolProgress == nil {
			break
		}
		m.activeRunStatus = "Tool progress"
		m.activeRunDetail = summarizeAgentToolProgress(ev.ToolProgress)
	case agent.EventToolCompleted:
		m.activeRunStatus = "Tool completed"
		if ev.Step > 0 {
			m.activeRunDetail = fmt.Sprintf("step %d finished", ev.Step)
		}
		m.applyCurrentSessionMessages()
	case agent.EventStepCompleted:
		m.activeRunStatus = "Preparing next step"
		if ev.Step > 0 {
			m.activeRunDetail = fmt.Sprintf("step %d complete", ev.Step)
		}
		m.applyCurrentSessionMessages()
	case agent.EventProviderTrace:
		if ev.Provider == nil {
			break
		}
		m.activeRunStatus, m.activeRunDetail = summarizeProviderTrace(ev.Provider)
	}
	m.refreshMessagesViewport()
	return m
}

func (m Model) updateExternalSessionEvent(event httpserver.ChatStreamEvent) Model {
	switch event.Type {
	case "status":
		if m.session != nil && strings.TrimSpace(event.Status) != "" {
			m.session.SetStatus(session.Status(event.Status))
		}
	case "assistant_delta":
		m.activeRunStatus = "Receiving response"
		m.appendAssistantDelta(event.Delta)
	case "tool_executing":
		toolCalls := externalToolCallsToAgentEvents(event.ToolCalls)
		m.activeRunStatus = toolExecutingStatus(event.Step, len(toolCalls))
		m.activeRunDetail = summarizeAgentToolCalls(toolCalls)
		if event.Message != nil {
			m.mergeExternalMessage(*event.Message)
		}
		m.attachToolCallsToLiveAssistant(toolCalls)
	case "tool_progress":
		if event.ToolProgress != nil {
			progress := externalToolProgressToAgentEvent(event.ToolProgress)
			m.activeRunStatus = "Tool progress"
			m.activeRunDetail = summarizeAgentToolProgress(&progress)
		}
	case "tool_completed":
		m.activeRunStatus = "Tool completed"
		if event.Step > 0 {
			m.activeRunDetail = fmt.Sprintf("step %d finished", event.Step)
		}
		if strings.TrimSpace(event.Status) != "" && m.session != nil {
			m.session.SetStatus(session.Status(event.Status))
		}
		if len(event.Messages) > 0 {
			m.messages = externalMessagesToTUI(event.Messages)
			m.clearLiveAssistantMetadata()
		} else if event.Message != nil {
			m.mergeExternalMessage(*event.Message)
		}
	case "step_completed":
		m.activeRunStatus = "Preparing next step"
		if event.Step > 0 {
			m.activeRunDetail = fmt.Sprintf("step %d complete", event.Step)
		}
	case "input_required":
		m.activeRunStatus = ""
		m.activeRunDetail = ""
		if strings.TrimSpace(event.Status) != "" && m.session != nil {
			m.session.SetStatus(session.Status(event.Status))
		}
		if len(event.Messages) > 0 {
			m.messages = externalMessagesToTUI(event.Messages)
			m.clearLiveAssistantMetadata()
		}
	case "done":
		m.activeRunStatus = ""
		m.activeRunDetail = ""
		if strings.TrimSpace(event.Status) != "" && m.session != nil {
			m.session.SetStatus(session.Status(event.Status))
		}
		if len(event.Messages) > 0 {
			m.messages = externalMessagesToTUI(event.Messages)
			m.clearLiveAssistantMetadata()
		}
		if event.Usage != nil {
			m.totalInputTokens += event.Usage.InputTokens
			m.totalOutputTokens += event.Usage.OutputTokens
		}
	case "error":
		m.activeRunStatus = ""
		m.activeRunDetail = ""
		if strings.TrimSpace(event.Status) != "" && m.session != nil {
			m.session.SetStatus(session.Status(event.Status))
		}
		if len(event.Messages) > 0 {
			m.messages = externalMessagesToTUI(event.Messages)
			m.clearLiveAssistantMetadata()
		}
		if strings.TrimSpace(event.Error) != "" {
			m.messages = append(m.messages, message{
				role:      "error",
				content:   strings.TrimSpace(event.Error),
				timestamp: time.Now(),
			})
		}
	case "provider_trace":
		if event.Provider != nil {
			trace := agent.ProviderTraceEvent{
				Provider:      event.Provider.Provider,
				Model:         event.Provider.Model,
				Attempt:       event.Provider.Attempt,
				MaxAttempts:   event.Provider.MaxAttempts,
				NodeIndex:     event.Provider.NodeIndex,
				TotalNodes:    event.Provider.TotalNodes,
				Phase:         event.Provider.Phase,
				Reason:        event.Provider.Reason,
				FallbackTo:    event.Provider.FallbackTo,
				FallbackModel: event.Provider.FallbackModel,
				Recovered:     event.Provider.Recovered,
			}
			m.activeRunStatus, m.activeRunDetail = summarizeProviderTrace(&trace)
		}
	}
	m.refreshMessagesViewport()
	return m
}

func shouldApplySessionSync(m Model, sess *session.Session) bool {
	if sess == nil {
		return false
	}
	if len(sess.Messages) != m.lastSyncedMessageCount {
		return true
	}
	return sess.UpdatedAt.After(m.lastSyncedSessionUpdatedAt)
}

func (m *Model) applySyncedSession(sess *session.Session) {
	if sess == nil {
		return
	}
	liveMessage, previousID, hasLive := m.liveAssistantMessage()
	next := messagesFromSession(sess)
	if hasLive && !persistedMessagesContainLiveAssistant(next, liveMessage) {
		next = insertLiveAssistantAfter(next, previousID, liveMessage)
	}

	m.session = sess
	m.messages = next
	m.lastSyncedMessageCount = len(sess.Messages)
	m.lastSyncedSessionUpdatedAt = sess.UpdatedAt
	m.taskSummary = sess.Title
	m.applySessionTokenMetadata(sess)
	m.refreshMessagesViewport()
}

func (m *Model) applyCurrentSessionMessages() {
	if m.session == nil {
		return
	}
	next := messagesFromSession(m.session)
	if len(next) == 0 {
		return
	}
	m.messages = preserveLocalOnlyMessages(next, m.messages)
	m.lastSyncedMessageCount = len(m.session.Messages)
	m.lastSyncedSessionUpdatedAt = m.session.UpdatedAt
}

func messagesFromSession(sess *session.Session) []message {
	if sess == nil {
		return nil
	}
	out := make([]message, 0, len(sess.Messages))
	for _, sessionMsg := range sess.Messages {
		out = append(out, message{
			id:          sessionMsg.ID,
			role:        sessionMsg.Role,
			content:     sessionMsg.Content,
			timestamp:   sessionMsg.Timestamp,
			toolCalls:   sessionMsg.ToolCalls,
			toolResults: sessionMsg.ToolResults,
			metadata:    sessionMsg.Metadata,
		})
	}
	return out
}

func (m *Model) appendAssistantDelta(delta string) {
	if delta == "" {
		return
	}
	idx := -1
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role != "assistant" {
			continue
		}
		if messageMetadataBool(m.messages[i].metadata, tuiLiveStreamMetadataKey) {
			idx = i
			break
		}
		if i == len(m.messages)-1 && len(m.messages[i].toolCalls) == 0 {
			idx = i
			break
		}
		break
	}
	if idx < 0 {
		m.messages = append(m.messages, message{
			role:      "assistant",
			content:   delta,
			timestamp: time.Now(),
			metadata:  map[string]interface{}{tuiLiveStreamMetadataKey: true},
		})
		return
	}
	m.messages[idx].content += delta
	if m.messages[idx].metadata == nil {
		m.messages[idx].metadata = make(map[string]interface{})
	}
	m.messages[idx].metadata[tuiLiveStreamMetadataKey] = true
}

func (m *Model) attachToolCallsToLiveAssistant(toolCalls []agent.ToolCallEvent) {
	if len(toolCalls) == 0 {
		return
	}
	idx := -1
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role == "assistant" {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.messages = append(m.messages, message{
			role:      "assistant",
			timestamp: time.Now(),
			metadata:  map[string]interface{}{tuiLiveStreamMetadataKey: true},
		})
		idx = len(m.messages) - 1
	}

	existing := make(map[string]struct{}, len(m.messages[idx].toolCalls))
	for _, tc := range m.messages[idx].toolCalls {
		existing[tc.ID] = struct{}{}
	}
	for _, tc := range toolCalls {
		if strings.TrimSpace(tc.ID) == "" {
			continue
		}
		if _, ok := existing[tc.ID]; ok {
			continue
		}
		input := json.RawMessage(strings.TrimSpace(tc.Input))
		if len(input) == 0 || !json.Valid(input) {
			encoded, _ := json.Marshal(tc.Input)
			input = encoded
		}
		m.messages[idx].toolCalls = append(m.messages[idx].toolCalls, session.ToolCall{
			ID:               tc.ID,
			Name:             tc.Name,
			Input:            input,
			ThoughtSignature: tc.ThoughtSignature,
		})
	}
	if m.messages[idx].metadata == nil {
		m.messages[idx].metadata = make(map[string]interface{})
	}
	m.messages[idx].metadata[tuiLiveStreamMetadataKey] = true
}

func (m Model) liveAssistantMessage() (message, string, bool) {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role != "assistant" || !messageMetadataBool(m.messages[i].metadata, tuiLiveStreamMetadataKey) {
			continue
		}
		previousID := ""
		for j := i - 1; j >= 0; j-- {
			if strings.TrimSpace(m.messages[j].id) != "" {
				previousID = m.messages[j].id
				break
			}
		}
		return m.messages[i], previousID, true
	}
	return message{}, "", false
}

func persistedMessagesContainLiveAssistant(persisted []message, live message) bool {
	liveContent := strings.TrimSpace(live.content)
	for _, msg := range persisted {
		if msg.role != "assistant" {
			continue
		}
		if liveContent != "" && strings.Contains(strings.TrimSpace(msg.content), liveContent) {
			return true
		}
		if len(live.toolCalls) > 0 && toolCallIDsContain(msg.toolCalls, live.toolCalls[0].ID) {
			return true
		}
	}
	return false
}

func insertLiveAssistantAfter(messages []message, previousID string, live message) []message {
	if strings.TrimSpace(previousID) == "" {
		return append(messages, live)
	}
	for i, msg := range messages {
		if msg.id != previousID {
			continue
		}
		out := make([]message, 0, len(messages)+1)
		out = append(out, messages[:i+1]...)
		out = append(out, live)
		out = append(out, messages[i+1:]...)
		return out
	}
	return append(messages, live)
}

func preserveLocalOnlyMessages(next []message, previous []message) []message {
	live, previousID, ok := liveAssistantFromMessages(previous)
	if !ok || persistedMessagesContainLiveAssistant(next, live) {
		return next
	}
	return insertLiveAssistantAfter(next, previousID, live)
}

func liveAssistantFromMessages(messages []message) (message, string, bool) {
	model := Model{messages: messages}
	return model.liveAssistantMessage()
}

func toolCallIDsContain(toolCalls []session.ToolCall, id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	for _, tc := range toolCalls {
		if tc.ID == id {
			return true
		}
	}
	return false
}

func messageMetadataBool(metadata map[string]interface{}, key string) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func (m *Model) refreshMessagesViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

func lastAssistantContentMatches(messages []message, content string) bool {
	want := strings.TrimSpace(content)
	if want == "" {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].role != "assistant" {
			continue
		}
		return strings.TrimSpace(messages[i].content) == want
	}
	return false
}

func toolExecutingStatus(step int, count int) string {
	if step <= 0 {
		if count == 1 {
			return "Running tool"
		}
		return "Running tools"
	}
	if count == 1 {
		return fmt.Sprintf("Running step %d tool", step)
	}
	return fmt.Sprintf("Running step %d tools", step)
}

func summarizeAgentToolCalls(toolCalls []agent.ToolCallEvent) string {
	if len(toolCalls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		parts = append(parts, summarizeAgentToolCall(tc))
	}
	return strings.Join(parts, ", ")
}

func summarizeAgentToolCall(tc agent.ToolCallEvent) string {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		name = "tool"
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
		return name
	}
	switch name {
	case "bash":
		if command, ok := input["command"].(string); ok && strings.TrimSpace(command) != "" {
			return name + ": " + strings.TrimSpace(command)
		}
	case "read", "write", "edit", "replace_lines":
		if path, ok := input["path"].(string); ok && strings.TrimSpace(path) != "" {
			return name + ": " + shortenPath(strings.TrimSpace(path), 80)
		}
	case "grep", "glob", "find_files":
		if pattern, ok := input["pattern"].(string); ok && strings.TrimSpace(pattern) != "" {
			return name + ": " + strings.TrimSpace(pattern)
		}
	case "parallel":
		if rawSteps, ok := input["steps"].([]interface{}); ok {
			return fmt.Sprintf("%s: %d steps", name, len(rawSteps))
		}
	}
	return name
}

func summarizeAgentToolProgress(progress *agent.ToolProgressEvent) string {
	name := strings.TrimSpace(progress.ToolName)
	status := strings.TrimSpace(progress.Status)
	content := strings.Join(strings.Fields(progress.Content), " ")
	switch {
	case name != "" && content != "" && status != "":
		return fmt.Sprintf("%s: %s - %s", name, status, content)
	case name != "" && content != "":
		return fmt.Sprintf("%s: %s", name, content)
	case name != "" && status != "":
		return fmt.Sprintf("%s: %s", name, status)
	case content != "":
		return content
	default:
		return status
	}
}

func summarizeProviderTrace(trace *agent.ProviderTraceEvent) (string, string) {
	phase := strings.TrimSpace(trace.Phase)
	status := "Provider update"
	switch phase {
	case "attempt_started":
		status = "Calling provider"
	case "attempt_failed", "attempt_failed_partial", "retry_layer_failed":
		status = "Provider retry"
	case "switching_provider":
		status = "Switching provider"
	case "attempt_succeeded":
		status = "Provider responded"
	}

	target := strings.TrimSpace(trace.Provider)
	if strings.TrimSpace(trace.Model) != "" {
		target = strings.TrimSpace(target + "/" + trace.Model)
	}
	detailParts := []string{}
	if target != "" {
		detailParts = append(detailParts, target)
	}
	if trace.Attempt > 0 && trace.MaxAttempts > 0 {
		detailParts = append(detailParts, fmt.Sprintf("attempt %d/%d", trace.Attempt, trace.MaxAttempts))
	}
	if trace.NodeIndex > 0 && trace.TotalNodes > 0 {
		detailParts = append(detailParts, fmt.Sprintf("node %d/%d", trace.NodeIndex, trace.TotalNodes))
	}
	if strings.TrimSpace(trace.Reason) != "" {
		detailParts = append(detailParts, strings.Join(strings.Fields(trace.Reason), " "))
	}
	if strings.TrimSpace(trace.FallbackTo) != "" {
		fallbackTarget := strings.TrimSpace(trace.FallbackTo)
		if strings.TrimSpace(trace.FallbackModel) != "" {
			fallbackTarget += "/" + strings.TrimSpace(trace.FallbackModel)
		}
		detailParts = append(detailParts, "next "+fallbackTarget)
	}
	return status, strings.Join(detailParts, " - ")
}

func externalToolCallsToAgentEvents(toolCalls []httpserver.StreamToolCallEvent) []agent.ToolCallEvent {
	out := make([]agent.ToolCallEvent, 0, len(toolCalls))
	for _, tc := range toolCalls {
		out = append(out, agent.ToolCallEvent{
			ID:               tc.ID,
			Name:             tc.Name,
			Input:            string(tc.Input),
			ThoughtSignature: tc.ThoughtSignature,
		})
	}
	return out
}

func externalToolProgressToAgentEvent(progress *httpserver.StreamToolProgressEvent) agent.ToolProgressEvent {
	if progress == nil {
		return agent.ToolProgressEvent{}
	}
	return agent.ToolProgressEvent{
		ToolCallID: progress.ToolCallID,
		ToolName:   progress.ToolName,
		Status:     progress.Status,
		Content:    progress.Content,
		Metadata:   progress.Metadata,
	}
}

func externalMessagesToTUI(messages []httpserver.MessageResponse) []message {
	out := make([]message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, externalMessageToTUI(msg))
	}
	return out
}

func externalMessageToTUI(msg httpserver.MessageResponse) message {
	return message{
		id:          msg.ID,
		role:        msg.Role,
		content:     msg.Content,
		timestamp:   msg.Timestamp,
		toolCalls:   externalToolCallResponsesToSession(msg.ToolCalls),
		toolResults: externalToolResultResponsesToSession(msg.ToolResults),
		metadata:    msg.Metadata,
	}
}

func externalToolCallResponsesToSession(toolCalls []httpserver.ToolCallResponse) []session.ToolCall {
	out := make([]session.ToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		out = append(out, session.ToolCall{
			ID:               tc.ID,
			Name:             tc.Name,
			Input:            tc.Input,
			ThoughtSignature: tc.ThoughtSignature,
		})
	}
	return out
}

func externalToolResultResponsesToSession(toolResults []httpserver.ToolResultResponse) []session.ToolResult {
	out := make([]session.ToolResult, 0, len(toolResults))
	for _, tr := range toolResults {
		out = append(out, session.ToolResult{
			ToolCallID: tr.ToolCallID,
			Content:    tr.Content,
			IsError:    tr.IsError,
			Metadata:   tr.Metadata,
			Name:       tr.Name,
			DurationMs: tr.DurationMs,
		})
	}
	return out
}

func (m *Model) mergeExternalMessage(msg httpserver.MessageResponse) {
	next := externalMessageToTUI(msg)
	if strings.TrimSpace(next.id) != "" {
		for i := range m.messages {
			if m.messages[i].id == next.id {
				m.messages[i] = next
				return
			}
		}
	}
	if next.role == "assistant" {
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].role != "assistant" {
				continue
			}
			if messageMetadataBool(m.messages[i].metadata, tuiLiveStreamMetadataKey) {
				m.messages[i] = next
				return
			}
			break
		}
	}
	m.messages = append(m.messages, next)
}

func (m *Model) clearLiveAssistantMetadata() {
	for i := range m.messages {
		if len(m.messages[i].metadata) == 0 {
			continue
		}
		delete(m.messages[i].metadata, tuiLiveStreamMetadataKey)
	}
}

func (m Model) updateTokens(msg tokenUpdateMsg) Model {
	m.totalInputTokens += msg.inputTokens
	m.totalOutputTokens += msg.outputTokens
	return m
}
