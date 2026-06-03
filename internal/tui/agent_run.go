package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleUserInput(input string) Model {
	// Add user message to display
	m.messages = append(m.messages, message{
		role:      "user",
		content:   input,
		timestamp: time.Now(),
	})

	// Update session
	m.session.AddUserMessage(input)
	m.lastUserInputTime = time.Now()
	m.processing = true

	// Update sync counter to prevent duplicate messages
	m.lastSyncedMessageCount = len(m.session.Messages)

	// Start agent in background
	return m
}

// runAgent starts the agent loop and returns a command along with the cancel function
func (m Model) runAgent(input string) (tea.Cmd, context.CancelFunc) {
	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Capture necessary fields for the goroutine
	agent := m.agent
	sess := m.session

	cmd := func() tea.Msg {
		if err := m.validateActiveProviderConfig(); err != nil {
			sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = m.sessionManager.Save(sess)
			return agentResponseMsg{err: err}
		}

		result, usage, err := agent.Run(ctx, sess, input)
		if err != nil {
			return agentResponseMsg{err: err}
		}
		return agentResponseMsg{
			content:      result,
			done:         true,
			inputTokens:  usage.InputTokens,
			outputTokens: usage.OutputTokens,
		}
	}

	return cmd, cancel
}

// runAgentResume continues agent execution after answering a question
func (m Model) runAgentResume() (tea.Cmd, context.CancelFunc) {
	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Capture necessary fields for the goroutine
	agent := m.agent
	sess := m.session

	cmd := func() tea.Msg {
		// Agent continues from where it left off
		// The answer was already added as a user message by AnswerQuestion
		result, usage, err := agent.Run(ctx, sess, "")
		if err != nil {
			return agentResponseMsg{err: err}
		}
		return agentResponseMsg{
			content:      result,
			done:         true,
			inputTokens:  usage.InputTokens,
			outputTokens: usage.OutputTokens,
		}
	}

	return cmd, cancel
}

// generateTitle generates a session title from the conversation
func (m Model) generateTitle() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Build a summary of the conversation for title generation
		var conversationSummary string
		for _, msg := range m.messages {
			if msg.role == "user" || msg.role == "assistant" {
				content := msg.content
				if len(content) > 200 {
					content = content[:200] + "..."
				}
				conversationSummary += fmt.Sprintf("%s: %s\n", msg.role, content)
			}
		}

		// Create a simple request to generate title
		request := &llm.ChatRequest{
			Messages: []llm.Message{
				{
					Role:    "user",
					Content: fmt.Sprintf("Summarize this conversation in a short title (max 50 chars, no quotes):\n\n%s", conversationSummary),
				},
			},
			MaxTokens:   50,
			Temperature: 0.3,
		}

		response, err := m.llmClient.Chat(ctx, request)
		if err != nil {
			// Silently fail - title generation is not critical
			return titleUpdateMsg{title: "", inputTokens: 0, outputTokens: 0}
		}

		title := strings.TrimSpace(response.Content)
		// Remove quotes if present
		title = strings.Trim(title, "\"'")
		// Limit length
		if len(title) > 60 {
			title = title[:57] + "..."
		}

		return titleUpdateMsg{
			title:        title,
			inputTokens:  response.Usage.InputTokens,
			outputTokens: response.Usage.OutputTokens,
		}
	}
}
