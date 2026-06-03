package tui

import (
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateTick() []tea.Cmd {
	if m.processing {
		m.loadingIndex = (m.loadingIndex + 1) % len(m.loadingFrames)
	}
	if m.showLogsView {
		m.refreshLogsView()
	}
	return []tea.Cmd{tickCmd(), updateMemoryCmd()}
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

		if len(msg.session.Messages) > m.lastSyncedMessageCount {
			m.session = msg.session
			m.messages = make([]message, 0, len(msg.session.Messages))
			for _, sessionMsg := range msg.session.Messages {
				m.messages = append(m.messages, message{
					role:        sessionMsg.Role,
					content:     sessionMsg.Content,
					timestamp:   sessionMsg.Timestamp,
					toolCalls:   sessionMsg.ToolCalls,
					toolResults: sessionMsg.ToolResults,
					metadata:    sessionMsg.Metadata,
				})
			}
			m.lastSyncedMessageCount = len(msg.session.Messages)
			m.taskSummary = msg.session.Title
			m.applySessionTokenMetadata(msg.session)
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
		} else {
			m.session = msg.session
		}
		if workflow := workflowFromSessionMetadata(msg.session); workflow.ID != "" {
			m.selectedWorkflow = workflow
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
	logging.Debug("TUI: Agent done, processing=%v queuedMessages=%d", m.processing, len(m.queuedMessages))

	if freshSess, err := m.sessionManager.Get(m.session.ID); err == nil {
		m.session = freshSess
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

	if msg.content != "" {
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
		m.lastUserInputTime = time.Now()
		m.processing = true
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		cmd, cancel := m.runAgent(nextInput)
		m.cancelFunc = cancel
		m.cancelPending = false
		cmds = append(cmds, cmd)
	}

	return m, cmds
}

func (m Model) updateTitle(msg titleUpdateMsg) Model {
	m.session.SetTitle(msg.title)
	m.taskSummary = msg.title
	m.saveSessionIfNotEmpty()
	m.totalInputTokens += msg.inputTokens
	m.totalOutputTokens += msg.outputTokens
	return m
}

func (m Model) updateTokens(msg tokenUpdateMsg) Model {
	m.totalInputTokens += msg.inputTokens
	m.totalOutputTokens += msg.outputTokens
	return m
}
