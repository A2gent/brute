package tui

import (
	"context"
	"fmt"
	"time"

	agentpkg "github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleUserInput(input string) Model {
	// Update session first so the displayed message carries the persisted ID.
	m.session.AddUserMessage(input)
	userMsg := m.session.GetLastMessage()

	// Add user message to display
	displayMsg := message{
		role:      "user",
		content:   input,
		timestamp: time.Now(),
	}
	if userMsg != nil {
		displayMsg.id = userMsg.ID
		displayMsg.timestamp = userMsg.Timestamp
	}
	m.messages = append(m.messages, displayMsg)

	// Update session
	m.session.SetStatus(session.StatusRunning)
	_ = m.sessionManager.Save(m.session)
	m.lastUserInputTime = time.Now()
	m.processing = true
	m.activeRunStatus = "Sending request"
	m.activeRunDetail = ""

	// Update sync counter to prevent duplicate messages
	m.lastSyncedMessageCount = len(m.session.Messages)
	m.lastSyncedSessionUpdatedAt = m.session.UpdatedAt

	// Start agent in background
	return m
}

func (m Model) startExistingSessionRun(input string) (Model, tea.Cmd) {
	if m.processing || m.session == nil {
		return m, nil
	}
	m.processing = true
	m.activeRunStatus = "Sending request"
	m.activeRunDetail = ""
	m.lastUserInputTime = time.Now()
	m.session.SetStatus(session.StatusRunning)
	_ = m.sessionManager.Save(m.session)
	m.lastSyncedMessageCount = len(m.session.Messages)
	m.lastSyncedSessionUpdatedAt = m.session.UpdatedAt
	cmd, cancel := m.runAgent(input)
	m.cancelFunc = cancel
	m.cancelPending = false
	return m, cmd
}

// runAgent starts the agent loop and returns a command along with the cancel function
func (m Model) runAgent(input string) (tea.Cmd, context.CancelFunc) {
	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Capture necessary fields for the goroutine
	ag := m.agent
	sess := m.session
	events := make(chan agentStreamMsg, 128)

	go func() {
		defer close(events)
		if err := m.validateActiveProviderConfig(); err != nil {
			sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = m.sessionManager.Save(sess)
			events <- agentStreamMsg{response: agentResponseMsg{err: err}, hasResponse: true}
			return
		}

		result, usage, err := ag.RunWithEvents(ctx, sess, input, func(ev agentpkg.Event) {
			select {
			case events <- agentStreamMsg{event: ev, hasEvent: true}:
			default:
			}
		})
		if err != nil {
			events <- agentStreamMsg{response: agentResponseMsg{err: err}, hasResponse: true}
			return
		}
		events <- agentStreamMsg{response: agentResponseMsg{
			content:      result,
			done:         true,
			inputTokens:  usage.InputTokens,
			outputTokens: usage.OutputTokens,
		}, hasResponse: true}
	}()

	return agentStreamCmd(events), cancel
}

// runAgentResume continues agent execution after answering a question
func (m Model) runAgentResume() (tea.Cmd, context.CancelFunc) {
	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Capture necessary fields for the goroutine
	ag := m.agent
	sess := m.session
	events := make(chan agentStreamMsg, 128)

	go func() {
		defer close(events)
		// Agent continues from where it left off
		// The answer was already added as a user message by AnswerQuestion
		result, usage, err := ag.RunWithEvents(ctx, sess, "", func(ev agentpkg.Event) {
			select {
			case events <- agentStreamMsg{event: ev, hasEvent: true}:
			default:
			}
		})
		if err != nil {
			events <- agentStreamMsg{response: agentResponseMsg{err: err}, hasResponse: true}
			return
		}
		events <- agentStreamMsg{response: agentResponseMsg{
			content:      result,
			done:         true,
			inputTokens:  usage.InputTokens,
			outputTokens: usage.OutputTokens,
		}, hasResponse: true}
	}()

	return agentStreamCmd(events), cancel
}

func agentStreamCmd(stream <-chan agentStreamMsg) tea.Cmd {
	if stream == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-stream
		if !ok {
			return agentStreamClosedMsg{stream: stream}
		}
		msg.stream = stream
		return msg
	}
}
