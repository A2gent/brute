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
	"github.com/gratheon/aagent/internal/commands"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/logging"
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

	userContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1a1a3e")).
				Padding(0, 1)

	assistantContentStyle = lipgloss.NewStyle()

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

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#444444"))

	queuedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true)

	queuedContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2a2a2a")).
				Foreground(lipgloss.Color("#888888")).
				Padding(0, 1)

	// Command menu styles
	commandMenuStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7D56F4")).
				Padding(0, 1)

	commandItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF"))

	commandSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#7D56F4")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)

	commandDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#888888"))
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

	titleUpdateMsg struct {
		title        string
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
	agentConfig    agent.Config

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

	// Interaction tracking for auto-summarization
	interactionCount int
	titleGenerated   bool

	// Message queue for when processing
	queuedMessages []string

	// Timing
	lastUserInputTime time.Time
	processing        bool
	loadingFrames     []string
	loadingIndex      int

	// Cancel support
	cancelFunc    context.CancelFunc
	cancelPending bool // true if user pressed Ctrl+C once while processing

	// Command menu state
	commandRegistry  *commands.Registry
	showCommandMenu  bool
	commandMenuIndex int
	filteredCommands []commands.Command

	// Sessions list view state
	showSessionsList  bool
	sessionsListIndex int
	availableSessions []*session.Session

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
	ta.Placeholder = "Enter your task or message (type / for commands)..."
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	ta.CharLimit = 0 // Unlimited
	ta.ShowLineNumbers = false

	cmdRegistry := commands.NewRegistry()

	m := Model{
		textarea:          ta,
		session:           sess,
		sessionManager:    sessionManager,
		agent:             agent.New(agentConfig, llmClient, toolManager, sessionManager),
		toolManager:       toolManager,
		llmClient:         llmClient,
		agentConfig:       agentConfig,
		messages:          make([]message, 0),
		taskSummary:       initialTask,
		lastUserInputTime: time.Now(),
		loadingFrames:     []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		loadingIndex:      0,
		contextWindow:     128000, // kimi-k2.5 context window
		commandRegistry:   cmdRegistry,
		filteredCommands:  cmdRegistry.GetCommands(),
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

		// Height calculation: total - topBar(1) - separator(1) - textarea(3) - helpText(1) = total - 6
		viewportHeight := msg.Height - 6
		if viewportHeight < 1 {
			viewportHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(msg.Width, viewportHeight)
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = viewportHeight
		}

		m.textarea.SetWidth(msg.Width)
		m.viewport.SetContent(m.renderMessages())

	case tea.KeyMsg:
		// Handle sessions list view first
		if m.showSessionsList {
			switch msg.Type {
			case tea.KeyEsc:
				m.showSessionsList = false
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case tea.KeyUp:
				if m.sessionsListIndex > 0 {
					m.sessionsListIndex--
				}
				return m, nil
			case tea.KeyDown:
				if m.sessionsListIndex < len(m.availableSessions)-1 {
					m.sessionsListIndex++
				}
				return m, nil
			case tea.KeyEnter:
				if len(m.availableSessions) > 0 {
					selectedSession := m.availableSessions[m.sessionsListIndex]
					m = m.switchToSession(selectedSession.ID)
					m.showSessionsList = false
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
				}
				return m, nil
			}
			return m, nil
		}

		// Handle command menu
		if m.showCommandMenu {
			switch msg.Type {
			case tea.KeyEsc:
				m.showCommandMenu = false
				m.textarea.Reset()
				return m, nil
			case tea.KeyUp:
				if m.commandMenuIndex > 0 {
					m.commandMenuIndex--
				}
				return m, nil
			case tea.KeyDown:
				if m.commandMenuIndex < len(m.filteredCommands)-1 {
					m.commandMenuIndex++
				}
				return m, nil
			case tea.KeyEnter, tea.KeyTab:
				if len(m.filteredCommands) > 0 {
					selectedCmd := m.filteredCommands[m.commandMenuIndex]
					m.showCommandMenu = false
					m.textarea.Reset()
					return m.executeCommand(selectedCmd.Name)
				}
				return m, nil
			case tea.KeyBackspace:
				input := m.textarea.Value()
				if input == "/" || input == "" {
					m.showCommandMenu = false
					m.textarea.Reset()
					return m, nil
				}
				// Let textarea handle backspace, then update filter
				m.textarea, taCmd = m.textarea.Update(msg)
				newInput := m.textarea.Value()
				if strings.HasPrefix(newInput, "/") {
					m.filteredCommands = m.commandRegistry.FilterCommands(newInput[1:])
					m.commandMenuIndex = 0
				} else {
					m.showCommandMenu = false
				}
				return m, taCmd
			default:
				// Update textarea and filter commands
				m.textarea, taCmd = m.textarea.Update(msg)
				input := m.textarea.Value()
				if strings.HasPrefix(input, "/") {
					m.filteredCommands = m.commandRegistry.FilterCommands(input[1:])
					m.commandMenuIndex = 0
				} else {
					m.showCommandMenu = false
				}
				return m, taCmd
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			if m.processing {
				if m.cancelPending {
					// Second Ctrl+C - force quit
					if m.cancelFunc != nil {
						m.cancelFunc()
					}
					if m.session != nil {
						m.sessionManager.Save(m.session)
					}
					return m, tea.Quit
				}
				// First Ctrl+C while processing - cancel the agent
				m.cancelPending = true
				if m.cancelFunc != nil {
					m.cancelFunc()
					logging.Info("Agent cancelled by user")
				}
				// Show cancellation message
				m.messages = append(m.messages, message{
					role:      "error",
					content:   "Cancelling... (press Ctrl+C again to force quit)",
					timestamp: time.Now(),
				})
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				return m, nil
			}
			// Not processing - quit immediately
			if m.session != nil {
				m.sessionManager.Save(m.session)
			}
			return m, tea.Quit

		case tea.KeyEsc:
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
				// Check if it's a command
				if strings.HasPrefix(input, "/") {
					cmdName := strings.TrimPrefix(input, "/")
					cmdName = strings.TrimSpace(cmdName)
					if cmd := m.commandRegistry.FindCommand(cmdName); cmd != nil {
						m.textarea.Reset()
						return m.executeCommand(cmd.Name)
					}
				}

				m.textarea.Reset()

				if m.processing {
					// Queue the message if we're still processing
					m.queuedMessages = append(m.queuedMessages, input)
					// Show queued message in UI with pending indicator
					m.messages = append(m.messages, message{
						role:      "queued",
						content:   input,
						timestamp: time.Now(),
					})
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
					return m, nil
				}

				m = m.handleUserInput(input)
				m.viewport.SetContent(m.renderMessages())
				m.viewport.GotoBottom()
				// Start the agent in background
				cmd, cancel := m.runAgent(input)
				m.cancelFunc = cancel
				m.cancelPending = false
				return m, cmd
			}
			return m, nil

		case tea.KeyRunes:
			// Check if user is typing a slash to show command menu
			if len(msg.Runes) > 0 && msg.Runes[0] == '/' && m.textarea.Value() == "" {
				m.showCommandMenu = true
				m.commandMenuIndex = 0
				m.filteredCommands = m.commandRegistry.GetCommands()
			}
		}

	case tickMsg:
		if m.processing {
			m.loadingIndex = (m.loadingIndex + 1) % len(m.loadingFrames)
		}
		cmds = append(cmds, tickCmd())

	case agentResponseMsg:
		logging.Debug("TUI received agentResponseMsg: done=%v err=%v tokens=%d/%d", msg.done, msg.err != nil, msg.inputTokens, msg.outputTokens)

		// Update token counts
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
		} else if msg.done {
			m.processing = false
			m.cancelFunc = nil
			m.cancelPending = false
			logging.Debug("TUI: Agent done, processing=%v queuedMessages=%d", m.processing, len(m.queuedMessages))
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

			// Track interaction and trigger title generation after 2 interactions
			m.interactionCount++
			if m.interactionCount >= 2 && !m.titleGenerated && len(m.queuedMessages) == 0 {
				// Only generate title if no queued messages to avoid interference
				m.titleGenerated = true
				cmds = append(cmds, m.generateTitle())
			}

			// Process queued messages
			if len(m.queuedMessages) > 0 {
				// Get the first queued message
				nextInput := m.queuedMessages[0]
				m.queuedMessages = m.queuedMessages[1:]

				// Convert queued message to sent message in display
				for i := range m.messages {
					if m.messages[i].role == "queued" && m.messages[i].content == nextInput {
						m.messages[i].role = "user"
						m.messages[i].timestamp = time.Now()
						break
					}
				}

				// Process the queued message
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
		}

	case titleUpdateMsg:
		// Update session title
		m.session.SetTitle(msg.title)
		m.taskSummary = msg.title
		m.sessionManager.Save(m.session)
		// Update token counts from title generation
		m.totalInputTokens += msg.inputTokens
		m.totalOutputTokens += msg.outputTokens

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

	// Top bar with task summary, stats, session, and time
	topBar := m.renderTopBar()

	// Messages viewport
	messagesView := m.viewport.View()

	// Check if we should show sessions list overlay
	if m.showSessionsList {
		sessionsView := m.renderSessionsList()
		// Center the sessions list
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			sessionsView,
		)
	}

	// Separator line above input
	separator := m.renderSeparator()

	// Command menu (rendered above input if active)
	var commandMenu string
	if m.showCommandMenu {
		commandMenu = m.renderCommandMenu() + "\n"
	}

	// Input area
	inputView := m.textarea.View()

	// Help text
	var helpStr string
	if m.showCommandMenu {
		helpStr = "↑↓: navigate • enter/tab: select • esc: cancel"
	} else if m.processing {
		helpStr = "ctrl+c: cancel • esc: quit • enter: queue message • /: commands"
	} else {
		helpStr = "esc: quit • enter: send • alt+enter: new line • /: commands"
	}
	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Render(helpStr)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		topBar,
		messagesView,
		separator,
		commandMenu+inputView,
		helpText,
	)
}

// renderSeparator renders a horizontal line with optional processing indicator
func (m Model) renderSeparator() string {
	var leftPart string
	if m.processing {
		leftPart = loadingStyle.Render(m.loadingFrames[m.loadingIndex] + " Processing")
		if len(m.queuedMessages) > 0 {
			leftPart += queuedStyle.Render(fmt.Sprintf(" (%d queued)", len(m.queuedMessages)))
		}
	}

	leftWidth := lipgloss.Width(leftPart)
	lineWidth := m.width - leftWidth
	if lineWidth < 0 {
		lineWidth = 0
	}

	line := separatorStyle.Render(strings.Repeat("─", lineWidth))

	if leftPart != "" {
		return leftPart + " " + line
	}
	return line
}

// renderTopBar renders the top bar with task summary, token stats, session, and time
func (m Model) renderTopBar() string {
	// Use session title if available, otherwise task summary or default
	summary := m.session.Title
	if summary == "" {
		summary = m.taskSummary
	}
	if summary == "" {
		summary = "New Session"
	}
	maxSummaryLen := m.width / 3
	if len(summary) > maxSummaryLen {
		summary = summary[:maxSummaryLen-3] + "..."
	}
	taskText := taskStyle.Render(summary)

	// Status indicator
	var statusIcon string
	switch m.session.Status {
	case session.StatusRunning:
		statusIcon = statusRunningStyle.Render("●")
	case session.StatusPaused:
		statusIcon = statusPausedStyle.Render("⏸")
	case session.StatusCompleted:
		statusIcon = statusCompletedStyle.Render("✓")
	case session.StatusFailed:
		statusIcon = statusFailedStyle.Render("✗")
	}

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

	tokenStats := fmt.Sprintf("%d↓ %d↑",
		m.totalInputTokens, m.totalOutputTokens)
	percentText := fmt.Sprintf("%.1f%%", contextPercent)

	// Timer showing time since last user input
	elapsed := time.Since(m.lastUserInputTime)
	timer := m.formatDuration(elapsed)

	// Session ID (truncated)
	sessionID := m.session.ID[:8]

	// Build right side: tokens | percent | time | session | status
	rightSide := statsStyle.Render(fmt.Sprintf("%s │ %s │ ⏱%s │ %s ",
		tokenStyle.Render(tokenStats),
		percentStyle.Render(percentText),
		timer,
		sessionID,
	)) + statusIcon

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

// wrapText wraps text to fit within the given width
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}

	var result strings.Builder
	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}

		// Wrap each line
		for len(line) > width {
			// Find a good break point
			breakPoint := width
			for breakPoint > 0 && line[breakPoint] != ' ' {
				breakPoint--
			}
			if breakPoint == 0 {
				breakPoint = width // No space found, force break
			}

			result.WriteString(line[:breakPoint])
			result.WriteString("\n")
			line = strings.TrimLeft(line[breakPoint:], " ")
		}
		result.WriteString(line)
	}

	return result.String()
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

	// Calculate wrap width (leave some margin)
	wrapWidth := m.width - 4
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	switch msg.role {
	case "user":
		header := userStyle.Render("You")
		indicator := sentStyle.Render(" ✓")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap and render user content with navy background
		wrapped := wrapText(msg.content, wrapWidth-2) // -2 for padding
		content := userContentStyle.Width(wrapWidth).Render(wrapped)
		sb.WriteString(content)

	case "assistant":
		header := assistantStyle.Render("Assistant")
		indicator := receivedStyle.Render(" ⬇")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap assistant content
		wrapped := wrapText(msg.content, wrapWidth)
		sb.WriteString(wrapped)

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

	case "queued":
		header := queuedStyle.Render("You (queued)")
		indicator := queuedStyle.Render(" ⏳")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap and render queued content with gray background
		wrapped := wrapText(msg.content, wrapWidth-2)
		content := queuedContentStyle.Width(wrapWidth).Render(wrapped)
		sb.WriteString(content)

	case "system":
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			Render("System")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))
		wrapped := wrapText(msg.content, wrapWidth)
		sb.WriteString(wrapped)
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

// runAgent starts the agent loop and returns a command along with the cancel function
func (m Model) runAgent(input string) (tea.Cmd, context.CancelFunc) {
	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Capture necessary fields for the goroutine
	agent := m.agent
	sess := m.session

	cmd := func() tea.Msg {
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

// SetSize sets the terminal size
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.textarea.SetWidth(width)
	if m.ready {
		m.viewport.Width = width
		viewportHeight := height - 6
		if viewportHeight < 1 {
			viewportHeight = 1
		}
		m.viewport.Height = viewportHeight
	}
}

// executeCommand executes a slash command and returns the updated model
func (m Model) executeCommand(cmdName string) (tea.Model, tea.Cmd) {
	switch cmdName {
	case "new":
		return m.createNewSession()
	case "sessions":
		return m.showSessions()
	case "clear":
		return m.clearConversation()
	case "help":
		return m.showHelp()
	default:
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Unknown command: /%s", cmdName),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}
}

// createNewSession creates a new session
func (m Model) createNewSession() (tea.Model, tea.Cmd) {
	// Save current session
	if m.session != nil {
		m.sessionManager.Save(m.session)
	}

	// Create new session
	newSess, err := m.sessionManager.Create(m.agentConfig.Name)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to create new session: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	// Reset model state for new session
	m.session = newSess
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)
	m.messages = make([]message, 0)
	m.taskSummary = ""
	m.totalInputTokens = 0
	m.totalOutputTokens = 0
	m.interactionCount = 0
	m.titleGenerated = false
	m.queuedMessages = nil
	m.lastUserInputTime = time.Now()

	// Show confirmation
	m.messages = append(m.messages, message{
		role:      "system",
		content:   fmt.Sprintf("Started new session: %s", newSess.ID[:8]),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	logging.Info("Created new session: %s", newSess.ID)
	return m, nil
}

// showSessions shows the sessions list
func (m Model) showSessions() (tea.Model, tea.Cmd) {
	sessions, err := m.sessionManager.List()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to list sessions: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.availableSessions = sessions
	m.sessionsListIndex = 0
	m.showSessionsList = true

	// Find current session in list
	for i, s := range sessions {
		if s.ID == m.session.ID {
			m.sessionsListIndex = i
			break
		}
	}

	return m, nil
}

// switchToSession switches to a different session
func (m Model) switchToSession(sessionID string) Model {
	// Save current session
	if m.session != nil {
		m.sessionManager.Save(m.session)
	}

	// Load the new session
	newSess, err := m.sessionManager.Get(sessionID)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to load session: %v", err),
			timestamp: time.Now(),
		})
		return m
	}

	// Update model with new session
	m.session = newSess
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)
	m.taskSummary = newSess.Title
	m.totalInputTokens = 0
	m.totalOutputTokens = 0
	m.interactionCount = len(newSess.Messages) / 2 // Rough estimate
	m.titleGenerated = newSess.Title != ""
	m.queuedMessages = nil
	m.lastUserInputTime = time.Now()

	// Load messages from session
	m.messages = make([]message, 0, len(newSess.Messages))
	for _, msg := range newSess.Messages {
		m.messages = append(m.messages, message{
			role:        msg.Role,
			content:     msg.Content,
			timestamp:   msg.Timestamp,
			toolCalls:   msg.ToolCalls,
			toolResults: msg.ToolResults,
		})
	}

	logging.Info("Switched to session: %s", sessionID)
	return m
}

// clearConversation clears the current conversation
func (m Model) clearConversation() (tea.Model, tea.Cmd) {
	m.messages = make([]message, 0)
	m.session.Messages = nil
	m.totalInputTokens = 0
	m.totalOutputTokens = 0
	m.interactionCount = 0
	m.titleGenerated = false
	m.queuedMessages = nil
	m.sessionManager.Save(m.session)

	m.messages = append(m.messages, message{
		role:      "system",
		content:   "Conversation cleared",
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	return m, nil
}

// showHelp shows available commands
func (m Model) showHelp() (tea.Model, tea.Cmd) {
	var helpText strings.Builder
	helpText.WriteString("Available commands:\n")
	for _, cmd := range m.commandRegistry.GetCommands() {
		aliases := ""
		if len(cmd.Aliases) > 0 {
			aliases = fmt.Sprintf(" (aliases: /%s)", strings.Join(cmd.Aliases, ", /"))
		}
		helpText.WriteString(fmt.Sprintf("  /%s - %s%s\n", cmd.Name, cmd.Description, aliases))
	}

	m.messages = append(m.messages, message{
		role:      "system",
		content:   helpText.String(),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	return m, nil
}

// renderCommandMenu renders the command menu popup
func (m Model) renderCommandMenu() string {
	if !m.showCommandMenu || len(m.filteredCommands) == 0 {
		return ""
	}

	var items []string
	for i, cmd := range m.filteredCommands {
		item := fmt.Sprintf("/%s", cmd.Name)
		desc := commandDescStyle.Render(fmt.Sprintf(" - %s", cmd.Description))

		if i == m.commandMenuIndex {
			item = commandSelectedStyle.Render(item)
		} else {
			item = commandItemStyle.Render(item)
		}
		items = append(items, item+desc)
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Render(content)
}

// renderSessionsList renders the sessions list popup
func (m Model) renderSessionsList() string {
	if !m.showSessionsList || len(m.availableSessions) == 0 {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Sessions (Enter to switch, Esc to cancel):"))
	items = append(items, "")

	for i, sess := range m.availableSessions {
		title := sess.Title
		if title == "" {
			title = "(no title)"
		}
		if len(title) > 40 {
			title = title[:37] + "..."
		}

		current := ""
		if sess.ID == m.session.ID {
			current = " (current)"
		}

		item := fmt.Sprintf("%s  %s  %s%s",
			sess.ID[:8],
			sess.CreatedAt.Format("01/02 15:04"),
			title,
			current,
		)

		if i == m.sessionsListIndex {
			item = commandSelectedStyle.Render(item)
		} else {
			item = commandItemStyle.Render(item)
		}
		items = append(items, item)
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}
