// Package tui provides a terminal user interface for the aagent application
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/session"
	"github.com/gratheon/aagent/internal/tools"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4"))

	taskStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	statsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0"))

	tokenStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00"))

	contextWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFF00"))

	contextDangerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF0000"))

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00AAFF"))

	assistantStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00FF00"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500"))

	toolResultStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000"))

	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00FF00"))

	statusPausedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFF00"))

	statusCompletedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00AAFF"))

	statusFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF0000"))

	loadingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500"))

	sentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00")).
			Bold(true)

	receivedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00AAFF"))
)

// Message types for the tea program
type (
	tickMsg time.Time

	agentResponseMsg struct {
		content      string
		done         bool
		err          error
		inputTokens  int
		outputTokens int
	}

	tokenUpdateMsg struct {
		inputTokens  int
		outputTokens int
	}
)

// Model represents the TUI state
type Model struct {
	// Components
	viewport viewport.Model
	textarea textarea.Model

	// Session state
	session        *session.Session
	sessionManager *session.Manager
	agent          *agent.Agent
	toolManager    *tools.Manager
	llmClient      llm.Client

	// Display state
	messages    []message
	taskSummary string
	width       int
	height      int
	ready       bool

	// Token tracking
	totalInputTokens  int
	totalOutputTokens int
	contextWindow     int // in tokens (default 128k for kimi-k2.5)

	// Timing
	lastUserInputTime time.Time
	processing        bool
	loadingFrames     []string
	loadingIndex      int

	// Error state
	err error
}

type message struct {
	role        string
	content     string
	timestamp   time.Time
	toolCalls   []session.ToolCall
	toolResults []session.ToolResult
}

// New creates a new TUI model
func New(
	sess *session.Session,
	sessionManager *session.Manager,
	agentConfig agent.Config,
	llmClient llm.Client,
	toolManager *tools.Manager,
	initialTask string,
) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter your task or message..."
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	ta.CharLimit = 0 // Unlimited
	ta.ShowLineNumbers = false

	m := Model{
		textarea:          ta,
		session:           sess,
		sessionManager:    sessionManager,
		agent:             agent.New(agentConfig, llmClient, toolManager, sessionManager),
		toolManager:       toolManager,
		llmClient:         llmClient,
		messages:          make([]message, 0),
		taskSummary:       initialTask,
		lastUserInputTime: time.Now(),
		loadingFrames:     []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		loadingIndex:      0,
		contextWindow:     128000, // kimi-k2.5 context window
	}

	// Load existing messages from session
	for _, msg := range sess.Messages {
		m.messages = append(m.messages, message{
			role:        msg.Role,
			content:     msg.Content,
			timestamp:   msg.Timestamp,
			toolCalls:   msg.ToolCalls,
			toolResults: msg.ToolResults,
		})
	}

	return m
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tickCmd(),
	)
}

// tickCmd creates a command that sends a tick message every second
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages and updates the model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		taCmd tea.Cmd
		vpCmd tea.Cmd
		cmds  []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-6)
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - 6
		}

		m.textarea.SetWidth(msg.Width)
		m.viewport.SetContent(m.renderMessages())

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			// Save session before quitting
			if m.session != nil {
				m.sessionManager.Save(m.session)
			}
			return m, tea.Quit

		case tea.KeyEnter:
			// Alt+Enter or Ctrl+Enter for new line, Enter to submit
			if msg.Alt {
				// Let the textarea handle it (insert new line)
				break
			}
			input := m.textarea.Value()
			if strings.TrimSpace(input) != "" {
				m = m.handleUserInput(input)
				m.textarea.Reset()
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				// Start the agent in background
				return m, m.runAgent(input)
			}
			return m, nil
		}

	case tickMsg:
		if m.processing {
			m.loadingIndex = (m.loadingIndex + 1) % len(m.loadingFrames)
		}
		cmds = append(cmds, tickCmd())

	case agentResponseMsg:
		// Update token counts
		m.totalInputTokens += msg.inputTokens
		m.totalOutputTokens += msg.outputTokens

		if msg.err != nil {
			m.processing = false
			m.messages = append(m.messages, message{
				role:      "error",
				content:   msg.err.Error(),
				timestamp: time.Now(),
			})
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
		} else if msg.done {
			m.processing = false
			// Add assistant response message
			if msg.content != "" {
				m.messages = append(m.messages, message{
					role:      "assistant",
					content:   msg.content,
					timestamp: time.Now(),
				})
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
			}
		}

	case tokenUpdateMsg:
		m.totalInputTokens += msg.inputTokens
		m.totalOutputTokens += msg.outputTokens
	}

	// Update components
	m.textarea, taCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	cmds = append(cmds, taCmd, vpCmd)

	return m, tea.Batch(cmds...)
}

// View renders the TUI
func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// Top bar with task summary and stats
	topBar := m.renderTopBar()

	// Messages viewport
	messagesView := m.viewport.View()

	// Status line (loading indicator, timer)
	statusLine := m.renderStatusLine()

	// Input area
	inputView := m.textarea.View()

	// Help text
	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Render("esc: quit • enter: send • shift+enter: new line")

	return lipgloss.JoinVertical(
		lipgloss.Left,
		topBar,
		messagesView,
		statusLine,
		inputView,
		helpText,
	)
}

// renderTopBar renders the top bar with task summary and token stats
func (m Model) renderTopBar() string {
	// Task summary (truncated if too long)
	summary := m.taskSummary
	maxSummaryLen := m.width / 2
	if len(summary) > maxSummaryLen {
		summary = summary[:maxSummaryLen-3] + "..."
	}
	taskText := taskStyle.Render(summary)

	// Token stats
	totalTokens := m.totalInputTokens + m.totalOutputTokens
	contextPercent := float64(totalTokens) / float64(m.contextWindow) * 100

	var percentStyle lipgloss.Style
	switch {
	case contextPercent >= 90:
		percentStyle = contextDangerStyle
	case contextPercent >= 70:
		percentStyle = contextWarningStyle
	default:
		percentStyle = tokenStyle
	}

	stats := fmt.Sprintf("Tokens: %s | %s",
		tokenStyle.Render(fmt.Sprintf("%d↓ %d↑", m.totalInputTokens, m.totalOutputTokens)),
		percentStyle.Render(fmt.Sprintf("%.1f%%", contextPercent)),
	)
	statsText := statsStyle.Render(stats)

	// Status indicator
	var statusText string
	switch m.session.Status {
	case session.StatusRunning:
		statusText = statusRunningStyle.Render("●")
	case session.StatusPaused:
		statusText = statusPausedStyle.Render("⏸")
	case session.StatusCompleted:
		statusText = statusCompletedStyle.Render("✓")
	case session.StatusFailed:
		statusText = statusFailedStyle.Render("✗")
	}

	// Combine right side
	rightSide := lipgloss.JoinHorizontal(lipgloss.Right, statsText, " ", statusText)

	// Calculate space between
	usedWidth := lipgloss.Width(taskText) + lipgloss.Width(rightSide)
	space := m.width - usedWidth
	if space < 1 {
		space = 1
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		taskText,
		strings.Repeat(" ", space),
		rightSide,
	)
}

// renderStatusLine renders the status line with loading indicator and timer
func (m Model) renderStatusLine() string {
	var parts []string

	// Loading indicator
	if m.processing {
		loading := loadingStyle.Render(m.loadingFrames[m.loadingIndex] + " Processing...")
		parts = append(parts, loading)
		parts = append(parts, "  ") // Add spacing
	}

	// Timer showing time since last user input
	elapsed := time.Since(m.lastUserInputTime)
	timer := m.formatDuration(elapsed)
	timerText := statsStyle.Render("⏱ " + timer)
	parts = append(parts, timerText)

	// Add separator between time and session
	parts = append(parts, statsStyle.Render("  │  "))

	// Session ID
	sessionInfo := statsStyle.Render(fmt.Sprintf("Session: %s", m.session.ID[:8]))
	parts = append(parts, sessionInfo)

	return lipgloss.JoinHorizontal(lipgloss.Left, parts...)
}

// formatDuration formats a duration in a human-readable way
func (m Model) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	} else {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// renderMessages renders all messages as a string
func (m Model) renderMessages() string {
	var sb strings.Builder

	for _, msg := range m.messages {
		sb.WriteString(m.renderMessage(msg))
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// renderMessage renders a single message
func (m Model) renderMessage(msg message) string {
	var sb strings.Builder

	// Timestamp
	timestamp := timestampStyle.Render(msg.timestamp.Format("15:04:05"))

	switch msg.role {
	case "user":
		header := userStyle.Render("You")
		indicator := sentStyle.Render(" ✓")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		sb.WriteString(msg.content)

	case "assistant":
		header := assistantStyle.Render("Assistant")
		indicator := receivedStyle.Render(" ⬇")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		sb.WriteString(msg.content)

		// Render tool calls if any
		for _, tc := range msg.toolCalls {
			toolLine := toolStyle.Render(fmt.Sprintf("  → %s", tc.Name))
			sb.WriteString("\n" + toolLine)
		}

	case "tool":
		header := toolResultStyle.Render("Tool Results")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))
		for _, tr := range msg.toolResults {
			status := "✓"
			if tr.IsError {
				status = "✗"
			}
			resultLine := fmt.Sprintf("  %s %s", status, tr.Content)
			if len(resultLine) > m.width-4 {
				resultLine = resultLine[:m.width-7] + "..."
			}
			sb.WriteString(resultLine + "\n")
		}

	case "error":
		header := errorStyle.Render("Error")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))
		sb.WriteString(errorStyle.Render(msg.content))
	}

	return sb.String()
}

// handleUserInput processes user input and starts the agent
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

	// Start agent in background
	return m
}

// runAgent starts the agent loop and returns a command
func (m Model) runAgent(input string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		result, usage, err := m.agent.Run(ctx, m.session, input)
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
}

// SetSize sets the terminal size
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.textarea.SetWidth(width)
	if m.ready {
		m.viewport.Width = width
		m.viewport.Height = height - 6
	}
}
