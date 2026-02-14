// Package tui provides a terminal user interface for the aagent application
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/commands"
	"github.com/gratheon/aagent/internal/config"
	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/llm/anthropic"
	"github.com/gratheon/aagent/internal/llm/fallback"
	"github.com/gratheon/aagent/internal/llm/lmstudio"
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

	compactionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD166"))

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

	// Tool-specific styles
	toolBashStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379")) // Green for shell commands

	toolReadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#61AFEF")) // Blue for reading

	toolWriteStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5C07B")) // Yellow for writing

	toolEditStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C678DD")) // Purple for editing

	toolGlobStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#56B6C2")) // Cyan for file search

	toolGrepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75")) // Red for content search

	toolTaskStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D19A66")) // Orange for sub-agents

	// Diff styles
	diffAddStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379")) // Green for additions

	diffRemoveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75")) // Red for deletions

	diffContextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ABB2BF")) // Gray for context

	diffHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#61AFEF")).
			Bold(true) // Blue bold for file headers

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

// Tool icons for visual distinction in the TUI
var toolIcons = map[string]string{
	"bash":  "", // Terminal icon
	"read":  "", // File read icon
	"write": "", // File write icon
	"edit":  "", // Edit icon
	"glob":  "", // Search files icon
	"grep":  "", // Search content icon
	"task":  "", // Sub-agent icon
}

// getToolIcon returns the icon for a tool, or a default arrow
func getToolIcon(toolName string) string {
	if icon, ok := toolIcons[toolName]; ok {
		return icon
	}
	return "" // Default icon
}

// getToolStyle returns the style for a tool
func getToolStyle(toolName string) lipgloss.Style {
	switch toolName {
	case "bash":
		return toolBashStyle
	case "read":
		return toolReadStyle
	case "write":
		return toolWriteStyle
	case "edit":
		return toolEditStyle
	case "glob":
		return toolGlobStyle
	case "grep":
		return toolGrepStyle
	case "task":
		return toolTaskStyle
	default:
		return toolStyle
	}
}

// ToolCallDisplay holds parsed tool call info for display
type ToolCallDisplay struct {
	Name    string
	Icon    string
	Summary string
	Details []string
}

// parseToolCall extracts display info from a tool call
func parseToolCall(tc session.ToolCall, maxWidth int) ToolCallDisplay {
	display := ToolCallDisplay{
		Name: tc.Name,
		Icon: getToolIcon(tc.Name),
	}

	// Parse the input JSON to extract relevant info
	var input map[string]interface{}
	if err := json.Unmarshal(tc.Input, &input); err != nil {
		display.Summary = tc.Name
		return display
	}

	switch tc.Name {
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			// Truncate command if too long
			if len(cmd) > maxWidth-10 {
				cmd = cmd[:maxWidth-13] + "..."
			}
			display.Summary = cmd
		}
		if workdir, ok := input["workdir"].(string); ok && workdir != "" {
			display.Details = append(display.Details, fmt.Sprintf("workdir: %s", workdir))
		}

	case "read":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}

	case "write":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		if content, ok := input["content"].(string); ok {
			lines := strings.Count(content, "\n") + 1
			display.Details = append(display.Details, fmt.Sprintf("%d lines", lines))
		}

	case "edit":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		// Add diff preview
		if oldStr, ok := input["old_string"].(string); ok {
			if newStr, ok := input["new_string"].(string); ok {
				display.Details = append(display.Details, formatDiff(oldStr, newStr, maxWidth-4))
			}
		}

	case "glob":
		if pattern, ok := input["pattern"].(string); ok {
			display.Summary = pattern
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "grep":
		if pattern, ok := input["pattern"].(string); ok {
			display.Summary = pattern
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "task":
		if desc, ok := input["description"].(string); ok {
			display.Summary = desc
		}
		if agentType, ok := input["subagent_type"].(string); ok {
			display.Details = append(display.Details, fmt.Sprintf("agent: %s", agentType))
		}

	default:
		display.Summary = tc.Name
	}

	return display
}

// shortenPath shortens a path to fit within maxLen
func shortenPath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	// Try to show the filename and as much of the path as possible
	base := filepath.Base(path)
	if len(base) >= maxLen-3 {
		return base[:maxLen-3] + "..."
	}
	remaining := maxLen - len(base) - 4 // for ".../"
	if remaining <= 0 {
		return base
	}
	dir := filepath.Dir(path)
	if len(dir) > remaining {
		dir = "..." + dir[len(dir)-remaining:]
	}
	return dir + "/" + base
}

// findToolNameByCallID finds the tool name for a given tool call ID
func findToolNameByCallID(toolCalls []session.ToolCall, callID string) string {
	for _, tc := range toolCalls {
		if tc.ID == callID {
			return tc.Name
		}
	}
	return "tool"
}

// formatDiff creates a simple diff display
func formatDiff(oldStr, newStr string, maxWidth int) string {
	var sb strings.Builder

	// Split into lines for comparison
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	// Show at most 5 lines of diff
	maxLines := 5

	// Show removed lines (up to maxLines/2)
	showCount := 0
	for i, line := range oldLines {
		if showCount >= (maxLines+1)/2 {
			if i < len(oldLines)-1 {
				sb.WriteString(diffRemoveStyle.Render(fmt.Sprintf("    ... (%d more removed)", len(oldLines)-i)))
				sb.WriteString("\n")
			}
			break
		}
		line = strings.TrimRight(line, " \t")
		if len(line) > maxWidth-6 {
			line = line[:maxWidth-9] + "..."
		}
		sb.WriteString(diffRemoveStyle.Render(fmt.Sprintf("    - %s", line)))
		sb.WriteString("\n")
		showCount++
	}

	// Show added lines (up to maxLines/2)
	showCount = 0
	for i, line := range newLines {
		if showCount >= maxLines/2 {
			if i < len(newLines)-1 {
				sb.WriteString(diffAddStyle.Render(fmt.Sprintf("    ... (%d more added)", len(newLines)-i)))
				sb.WriteString("\n")
			}
			break
		}
		line = strings.TrimRight(line, " \t")
		if len(line) > maxWidth-6 {
			line = line[:maxWidth-9] + "..."
		}
		sb.WriteString(diffAddStyle.Render(fmt.Sprintf("    + %s", line)))
		sb.WriteString("\n")
		showCount++
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

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

	memoryUpdateMsg struct {
		memoryMB float64
	}

	sessionSyncMsg struct {
		session *session.Session
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

	// Provider selection state
	showProviderMenu     bool
	providerMenuIndex    int
	providerMenuStep     int    // 0=select provider, 1=enter API key, 2=enter URL
	providerInput        string // For API key or URL input
	selectedProviderType string

	// Models selection state
	showModelsMenu  bool
	modelsMenuIndex int
	availableModels []string

	// Config reference for persistence
	appConfig *config.Config

	// Memory tracking
	memoryMB float64

	// Session sync tracking
	lastSyncedMessageCount int

	// Error state
	err error
}

type message struct {
	role        string
	content     string
	timestamp   time.Time
	toolCalls   []session.ToolCall
	toolResults []session.ToolResult
	metadata    map[string]interface{}
}

// New creates a new TUI model
func New(
	sess *session.Session,
	sessionManager *session.Manager,
	agentConfig agent.Config,
	llmClient llm.Client,
	toolManager *tools.Manager,
	initialTask string,
	appConfig *config.Config,
) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter your task or message (type / for commands)..."
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	ta.CharLimit = 0 // Unlimited
	ta.ShowLineNumbers = false

	cmdRegistry := commands.NewRegistry()

	// Determine context window from config
	contextWindow := 128000 // default
	if appConfig != nil {
		if def := config.GetProviderDefinition(config.ProviderType(appConfig.ActiveProvider)); def != nil {
			contextWindow = def.ContextWindow
		}
	}
	if agentConfig.ContextWindow <= 0 {
		agentConfig.ContextWindow = contextWindow
	}

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
		contextWindow:     contextWindow,
		commandRegistry:   cmdRegistry,
		filteredCommands:  cmdRegistry.GetCommands(),
		appConfig:         appConfig,
	}

	// Load existing messages from session
	for _, msg := range sess.Messages {
		m.messages = append(m.messages, message{
			role:        msg.Role,
			content:     msg.Content,
			timestamp:   msg.Timestamp,
			toolCalls:   msg.ToolCalls,
			toolResults: msg.ToolResults,
			metadata:    msg.Metadata,
		})
	}
	m.applySessionTokenMetadata(sess)

	return m
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tickCmd(),
		updateMemoryCmd(),
		sessionSyncCmd(m.sessionManager, m.session.ID),
	)
}

// tickCmd creates a command that sends a tick message every second
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// updateMemoryCmd returns a command that reads current memory usage
func updateMemoryCmd() tea.Cmd {
	return func() tea.Msg {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		// Alloc is bytes of allocated heap objects
		memoryMB := float64(memStats.Alloc) / 1024 / 1024
		return memoryUpdateMsg{memoryMB: memoryMB}
	}
}

// sessionSyncCmd returns a command that syncs the session from storage
func sessionSyncCmd(sessionManager *session.Manager, sessionID string) tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		sess, err := sessionManager.Get(sessionID)
		if err != nil {
			return nil
		}
		return sessionSyncMsg{session: sess}
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

		// Handle provider menu
		if m.showProviderMenu {
			switch m.providerMenuStep {
			case 0:
				// Provider selection
				switch msg.Type {
				case tea.KeyEsc:
					m.showProviderMenu = false
					m.viewport.SetContent(m.renderMessages())
					return m, nil
				case tea.KeyUp:
					if m.providerMenuIndex > 0 {
						m.providerMenuIndex--
					}
					return m, nil
				case tea.KeyDown:
					providers := config.SupportedProviders()
					if m.providerMenuIndex < len(providers)-1 {
						m.providerMenuIndex++
					}
					return m, nil
				case tea.KeyEnter:
					providers := config.SupportedProviders()
					if m.providerMenuIndex < len(providers) {
						selectedProvider := providers[m.providerMenuIndex]
						return m.selectProvider(selectedProvider.Type)
					}
					return m, nil
				}
			case 1, 2:
				// API key or URL input
				switch msg.Type {
				case tea.KeyEsc:
					m.showProviderMenu = false
					m.providerMenuStep = 0
					m.providerInput = ""
					m.viewport.SetContent(m.renderMessages())
					return m, nil
				case tea.KeyEnter:
					if m.providerInput != "" {
						return m.saveProviderCredentials()
					}
					return m, nil
				case tea.KeyBackspace:
					if len(m.providerInput) > 0 {
						m.providerInput = m.providerInput[:len(m.providerInput)-1]
					}
					return m, nil
				case tea.KeyRunes:
					// Handle character input
					m.providerInput += string(msg.Runes)
					return m, nil
				case tea.KeySpace:
					// Handle space character
					m.providerInput += " "
					return m, nil
				}
				return m, nil
			}
			return m, nil
		}

		// Handle models menu
		if m.showModelsMenu {
			switch msg.Type {
			case tea.KeyEsc:
				m.showModelsMenu = false
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case tea.KeyUp:
				if m.modelsMenuIndex > 0 {
					m.modelsMenuIndex--
				}
				return m, nil
			case tea.KeyDown:
				if m.modelsMenuIndex < len(m.availableModels)-1 {
					m.modelsMenuIndex++
				}
				return m, nil
			case tea.KeyEnter:
				if len(m.availableModels) > 0 && m.modelsMenuIndex < len(m.availableModels) {
					return m.selectModel(m.availableModels[m.modelsMenuIndex])
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
		cmds = append(cmds, tickCmd(), updateMemoryCmd())

	case memoryUpdateMsg:
		m.memoryMB = msg.memoryMB

	case sessionSyncMsg:
		if msg.session != nil && !m.processing {
			// Check if there are new messages from external sources (e.g., web app)
			if len(msg.session.Messages) > m.lastSyncedMessageCount {
				// Reload messages from the synced session
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
			}
		}
		// Schedule next sync
		cmds = append(cmds, sessionSyncCmd(m.sessionManager, m.session.ID))

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
			// Update sync counter after agent completes
			m.lastSyncedMessageCount = len(m.session.Messages)

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

	// Check if we should show provider menu overlay
	if m.showProviderMenu {
		providerView := m.renderProviderMenu()
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			providerView,
		)
	}

	// Check if we should show models menu overlay
	if m.showModelsMenu {
		modelsView := m.renderModelsMenu()
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			modelsView,
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
	// Session ID (truncated) shown next to session name
	sessionID := m.session.ID[:8]
	taskText := taskStyle.Render(summary) + statsStyle.Render(" ["+sessionID+"]")

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
	currentContextTokens := m.currentContextTokenCount()
	contextPercent := float64(currentContextTokens) / float64(m.contextWindow) * 100

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

	// Memory usage
	memoryText := fmt.Sprintf("%.1fMB", m.memoryMB)

	// Timer showing time since last user input
	elapsed := time.Since(m.lastUserInputTime)
	timer := m.formatDuration(elapsed)

	// Build right side: tokens | percent | memory | time | status
	rightSide := statsStyle.Render(fmt.Sprintf("%s │ %s │ %s │ ⏱ %s ",
		tokenStyle.Render(tokenStats),
		percentStyle.Render(percentText),
		statsStyle.Render(memoryText),
		timer,
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

func sessionMetadataFloat(sess *session.Session, key string) float64 {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	raw, ok := sess.Metadata[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func (m *Model) applySessionTokenMetadata(sess *session.Session) {
	if sess == nil {
		return
	}
	totalIn := int(sessionMetadataFloat(sess, "total_input_tokens"))
	totalOut := int(sessionMetadataFloat(sess, "total_output_tokens"))
	if totalIn > 0 || totalOut > 0 {
		m.totalInputTokens = totalIn
		m.totalOutputTokens = totalOut
	}
}

func (m Model) currentContextTokenCount() int {
	current := int(sessionMetadataFloat(m.session, "current_context_tokens"))
	if current > 0 {
		return current
	}
	return m.totalInputTokens + m.totalOutputTokens
}

// renderMessages renders all messages as a string
func (m Model) renderMessages() string {
	var sb strings.Builder

	for i, msg := range m.messages {
		var prevMsg *message
		if i > 0 {
			prevMsg = &m.messages[i-1]
		}
		sb.WriteString(m.renderMessageWithContext(msg, prevMsg))
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// renderMessage renders a single message with optional previous message context
func (m Model) renderMessage(msg message) string {
	return m.renderMessageWithContext(msg, nil)
}

func isCompactionMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata["context_compaction"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		normalized := strings.TrimSpace(strings.ToLower(v))
		return normalized == "true" || normalized == "1" || normalized == "yes"
	default:
		return false
	}
}

// renderMessageWithContext renders a message with context from previous message
func (m Model) renderMessageWithContext(msg message, prevMsg *message) string {
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
		contentStyle := assistantContentStyle
		if isCompactionMetadata(msg.metadata) {
			header = compactionStyle.Render("Compaction")
			indicator = ""
			contentStyle = compactionStyle
		}
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap assistant content
		wrapped := wrapText(msg.content, wrapWidth)
		sb.WriteString(contentStyle.Render(wrapped))

		// Render tool calls with icons and details
		for _, tc := range msg.toolCalls {
			display := parseToolCall(tc, wrapWidth)
			style := getToolStyle(tc.Name)

			// Tool header with icon and name
			toolHeader := style.Render(fmt.Sprintf("  %s %s", display.Icon, tc.Name))
			sb.WriteString("\n" + toolHeader)

			// Tool summary (command, path, pattern, etc.)
			if display.Summary != "" {
				summaryLine := toolResultStyle.Render(fmt.Sprintf("    %s", display.Summary))
				sb.WriteString("\n" + summaryLine)
			}

			// Additional details (diff, workdir, etc.)
			for _, detail := range display.Details {
				sb.WriteString("\n" + detail)
			}
		}

	case "tool":
		header := toolResultStyle.Render("Tool Results")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))

		// Get tool calls from previous assistant message (if available)
		var prevToolCalls []session.ToolCall
		if prevMsg != nil && prevMsg.role == "assistant" {
			prevToolCalls = prevMsg.toolCalls
		}

		for _, tr := range msg.toolResults {
			// Find the matching tool call to get the tool name
			toolName := findToolNameByCallID(prevToolCalls, tr.ToolCallID)
			icon := getToolIcon(toolName)
			style := getToolStyle(toolName)

			var statusIcon string
			var statusStyle lipgloss.Style
			if tr.IsError {
				statusIcon = ""
				statusStyle = errorStyle
			} else {
				statusIcon = ""
				statusStyle = style
			}

			// Format the result with icon and status
			resultHeader := statusStyle.Render(fmt.Sprintf("  %s %s %s", icon, toolName, statusIcon))
			sb.WriteString(resultHeader + "\n")

			// Show content preview (truncated)
			content := tr.Content
			if len(content) > 0 {
				// Limit to first few lines
				lines := strings.SplitN(content, "\n", 6)
				for i, line := range lines {
					if i >= 5 {
						remaining := len(strings.Split(content, "\n")) - 5
						if remaining > 0 {
							sb.WriteString(toolResultStyle.Render(fmt.Sprintf("    ... (%d more lines)", remaining)) + "\n")
						}
						break
					}
					line = strings.TrimRight(line, " \t\r")
					if len(line) > m.width-8 {
						line = line[:m.width-11] + "..."
					}
					sb.WriteString(toolResultStyle.Render(fmt.Sprintf("    %s", line)) + "\n")
				}
			}
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
	case "provider":
		return m.showProviderSelection()
	case "models":
		return m.showModelsSelection()
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
			metadata:    msg.Metadata,
		})
	}
	m.applySessionTokenMetadata(newSess)

	logging.Info("Switched to session: %s", sessionID)
	return m
}

// clearConversation clears the current conversation
func (m Model) clearConversation() (tea.Model, tea.Cmd) {
	m.messages = make([]message, 0)
	m.session.Messages = nil
	m.totalInputTokens = 0
	m.totalOutputTokens = 0
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

// showProviderSelection shows the provider selection menu
func (m Model) showProviderSelection() (tea.Model, tea.Cmd) {
	m.showProviderMenu = true
	m.providerMenuIndex = 0
	m.providerMenuStep = 0
	m.providerInput = ""

	// Find current provider in list
	providers := config.SupportedProviders()
	for i, p := range providers {
		if string(p.Type) == m.appConfig.ActiveProvider {
			m.providerMenuIndex = i
			break
		}
	}

	return m, nil
}

// showModelsSelection shows the models selection menu
func (m Model) showModelsSelection() (tea.Model, tea.Cmd) {
	// Check if we have a valid provider configured
	if m.appConfig == nil || m.appConfig.ActiveProvider == "" {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "No provider configured. Use /provider first.",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	// For LM Studio, fetch models from the API
	if m.appConfig.ActiveProvider == string(config.ProviderLMStudio) {
		return m.fetchLMStudioModels()
	}

	// For other providers, show known models
	return m.showStaticModels()
}

// fetchLMStudioModels fetches models from LM Studio API
func (m Model) fetchLMStudioModels() (tea.Model, tea.Cmd) {
	provider := m.appConfig.GetActiveProvider()
	baseURL := "http://localhost:1234/v1"
	if provider != nil && provider.BaseURL != "" {
		baseURL = provider.BaseURL
	}

	client := lmstudio.NewClient("", "", baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to fetch models from LM Studio: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.availableModels = make([]string, len(models))
	for i, model := range models {
		m.availableModels[i] = model.ID
	}

	if len(m.availableModels) == 0 {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "No models loaded in LM Studio. Please load a model first.",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.showModelsMenu = true
	m.modelsMenuIndex = 0

	// Find current model in list
	currentModel := m.appConfig.DefaultModel
	for i, model := range m.availableModels {
		if model == currentModel {
			m.modelsMenuIndex = i
			break
		}
	}

	return m, nil
}

// showStaticModels shows models for providers with known model lists
func (m Model) showStaticModels() (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(config.ProviderType(m.appConfig.ActiveProvider))
	if providerDef == nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "Unknown provider",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	// Define known models for each provider
	switch config.ProviderType(m.appConfig.ActiveProvider) {
	case config.ProviderKimi:
		m.availableModels = []string{"kimi-k2.5", "kimi-k2", "kimi-for-coding"}
	case config.ProviderOpenRouter:
		m.availableModels = []string{"openrouter/auto", "anthropic/claude-sonnet-4", "openai/gpt-4.1-mini"}
	case config.ProviderAnthropic:
		m.availableModels = []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514", "claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022"}
	case config.ProviderGoogle:
		m.availableModels = []string{"gemini-2.0-flash", "gemini-2.0-flash-lite", "gemini-2.0-pro"}
	default:
		m.availableModels = []string{providerDef.DefaultModel}
	}

	m.showModelsMenu = true
	m.modelsMenuIndex = 0

	// Find current model
	for i, model := range m.availableModels {
		if model == m.appConfig.DefaultModel {
			m.modelsMenuIndex = i
			break
		}
	}

	return m, nil
}

// selectProvider handles provider selection and triggers credential prompts if needed
func (m Model) selectProvider(providerType config.ProviderType) (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(providerType)
	if providerDef == nil {
		return m, nil
	}

	m.selectedProviderType = string(providerType)

	// Check if provider is already configured with credentials
	existingProvider := m.appConfig.Providers[string(providerType)]

	// For LM Studio, prompt for URL
	if providerType == config.ProviderLMStudio {
		if existingProvider.BaseURL == "" {
			m.providerMenuStep = 2 // Go to URL input
			m.providerInput = providerDef.DefaultURL
			return m, nil
		}
	}

	// For providers requiring API key
	if providerDef.RequiresKey && existingProvider.APIKey == "" {
		m.providerMenuStep = 1 // Go to API key input
		return m, nil
	}

	// Provider is ready, activate it
	return m.activateProvider(providerType)
}

// activateProvider activates the selected provider
func (m Model) activateProvider(providerType config.ProviderType) (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(providerType)
	if providerDef == nil {
		return m, nil
	}

	m.appConfig.ActiveProvider = string(providerType)
	m.appConfig.DefaultModel = providerDef.DefaultModel
	m.contextWindow = providerDef.ContextWindow
	m.agentConfig.Model = providerDef.DefaultModel
	m.agentConfig.ContextWindow = providerDef.ContextWindow

	// Save config
	if err := m.appConfig.Save(config.GetConfigPath()); err != nil {
		logging.Error("Failed to save config: %v", err)
	}

	// Create new LLM client for this provider
	m.llmClient = m.createLLMClient(providerType)

	// Update agent with new client
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)

	m.showProviderMenu = false
	m.providerMenuStep = 0

	m.messages = append(m.messages, message{
		role:      "system",
		content:   fmt.Sprintf("Switched to %s (model: %s)", providerDef.DisplayName, m.appConfig.DefaultModel),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())

	return m, nil
}

// createLLMClient creates an LLM client for the given provider type
func (m Model) createLLMClient(providerType config.ProviderType) llm.Client {
	if providerType == config.ProviderFallback {
		fallbackProvider := m.appConfig.Providers[string(config.ProviderFallback)]
		seen := map[string]struct{}{}
		nodes := make([]fallback.Node, 0, len(fallbackProvider.FallbackChain))
		for _, raw := range fallbackProvider.FallbackChain {
			nodeType := config.ProviderType(strings.ToLower(strings.TrimSpace(raw)))
			if nodeType == "" || nodeType == config.ProviderFallback {
				continue
			}
			if _, exists := seen[string(nodeType)]; exists {
				continue
			}
			seen[string(nodeType)] = struct{}{}
			nodeProvider := m.appConfig.Providers[string(nodeType)]
			nodeDef := config.GetProviderDefinition(nodeType)
			if nodeDef == nil {
				continue
			}
			apiKey := strings.TrimSpace(nodeProvider.APIKey)
			if apiKey == "" {
				apiKey = providerAPIKeyFromEnv(nodeType)
			}
			baseURL := strings.TrimSpace(nodeProvider.BaseURL)
			if baseURL == "" {
				baseURL = strings.TrimSpace(nodeDef.DefaultURL)
			}
			model := strings.TrimSpace(nodeProvider.Model)
			if model == "" {
				model = strings.TrimSpace(nodeDef.DefaultModel)
			}
			var client llm.Client
			switch nodeType {
			case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderGoogle:
				client = lmstudio.NewClient(apiKey, model, baseURL)
			default:
				client = anthropic.NewClientWithBaseURL(apiKey, model, baseURL)
			}
			nodes = append(nodes, fallback.Node{
				Name:   string(nodeType),
				Model:  model,
				Client: client,
			})
		}
		if len(nodes) >= 2 {
			return fallback.NewClient(nodes)
		}
		// Fall back to default provider if chain is invalid.
		return anthropic.NewClientWithBaseURL("", "kimi-k2.5", "https://api.kimi.com/coding/v1")
	}

	provider := m.appConfig.Providers[string(providerType)]
	providerDef := config.GetProviderDefinition(providerType)
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = providerAPIKeyFromEnv(providerType)
	}

	switch providerType {
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderGoogle:
		baseURL := provider.BaseURL
		if baseURL == "" {
			baseURL = providerDef.DefaultURL
		}
		return lmstudio.NewClient(apiKey, provider.Model, baseURL)
	default:
		// For Anthropic-compatible providers (Kimi, Anthropic)
		baseURL := provider.BaseURL
		if baseURL == "" {
			baseURL = providerDef.DefaultURL
		}
		// Import and use anthropic client
		return anthropic.NewClientWithBaseURL(apiKey, provider.Model, baseURL)
	}
}

func (m Model) validateActiveProviderConfig() error {
	if m.appConfig == nil {
		return nil
	}
	providerType := config.ProviderType(strings.TrimSpace(m.appConfig.ActiveProvider))
	def := config.GetProviderDefinition(providerType)
	if def == nil {
		return fmt.Errorf("unknown active provider: %s", providerType)
	}
	if providerType == config.ProviderFallback {
		fallbackProvider := m.appConfig.Providers[string(config.ProviderFallback)]
		if len(fallbackProvider.FallbackChain) < 2 {
			return fmt.Errorf("fallback chain requires at least two providers")
		}
		return nil
	}
	if !def.RequiresKey {
		return nil
	}

	provider := m.appConfig.Providers[string(providerType)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = providerAPIKeyFromEnv(providerType)
	}
	if apiKey == "" {
		envName := providerAPIKeyEnvName(providerType)
		if envName != "" {
			return fmt.Errorf("%s API key is missing. Configure provider settings (/provider) or set %s", def.DisplayName, envName)
		}
		return fmt.Errorf("%s API key is missing. Configure provider settings with /provider", def.DisplayName)
	}
	return nil
}

func providerAPIKeyFromEnv(providerType config.ProviderType) string {
	envName := providerAPIKeyEnvName(providerType)
	if envName == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func providerAPIKeyEnvName(providerType config.ProviderType) string {
	switch providerType {
	case config.ProviderKimi:
		return "KIMI_API_KEY"
	case config.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case config.ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case config.ProviderGoogle:
		return "GOOGLE_API_KEY"
	default:
		return ""
	}
}

// saveProviderCredentials saves the API key or URL for a provider
func (m Model) saveProviderCredentials() (tea.Model, tea.Cmd) {
	providerType := config.ProviderType(m.selectedProviderType)
	providerDef := config.GetProviderDefinition(providerType)

	provider := m.appConfig.Providers[m.selectedProviderType]
	if provider.Name == "" {
		provider.Name = m.selectedProviderType
	}

	if m.providerMenuStep == 1 {
		// Saving API key
		provider.APIKey = m.providerInput
	} else if m.providerMenuStep == 2 {
		// Saving URL
		provider.BaseURL = m.providerInput
	}

	if provider.Model == "" && providerDef != nil {
		provider.Model = providerDef.DefaultModel
	}

	m.appConfig.SetProvider(providerType, provider)

	// Check if we need more credentials
	if providerType == config.ProviderLMStudio {
		// LM Studio doesn't require API key, URL is enough
		return m.activateProvider(providerType)
	}

	if providerDef.RequiresKey && provider.APIKey == "" {
		m.providerMenuStep = 1
		m.providerInput = ""
		return m, nil
	}

	// All credentials gathered, activate provider
	return m.activateProvider(providerType)
}

// selectModel selects a model for the current provider
func (m Model) selectModel(modelName string) (tea.Model, tea.Cmd) {
	m.appConfig.DefaultModel = modelName

	// Update provider config
	provider := m.appConfig.Providers[m.appConfig.ActiveProvider]
	provider.Model = modelName
	m.appConfig.SetProvider(config.ProviderType(m.appConfig.ActiveProvider), provider)

	// Update context window based on model (rough estimates)
	switch {
	case strings.Contains(modelName, "kimi"):
		m.contextWindow = 131072
	case strings.Contains(modelName, "claude"):
		m.contextWindow = 200000
	case strings.Contains(modelName, "gemini"):
		m.contextWindow = 1048576
	default:
		m.contextWindow = 32768
	}
	m.agentConfig.Model = modelName
	m.agentConfig.ContextWindow = m.contextWindow

	// Save config
	if err := m.appConfig.Save(config.GetConfigPath()); err != nil {
		logging.Error("Failed to save config: %v", err)
	}

	// Recreate LLM client with new model
	m.llmClient = m.createLLMClient(config.ProviderType(m.appConfig.ActiveProvider))
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)

	m.showModelsMenu = false

	m.messages = append(m.messages, message{
		role:      "system",
		content:   fmt.Sprintf("Model switched to: %s", modelName),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())

	return m, nil
}

// renderProviderMenu renders the provider selection menu
func (m Model) renderProviderMenu() string {
	if !m.showProviderMenu {
		return ""
	}

	var items []string

	switch m.providerMenuStep {
	case 0:
		// Provider selection
		items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Provider (Enter to select, Esc to cancel):"))
		items = append(items, "")

		providers := config.SupportedProviders()
		for i, p := range providers {
			current := ""
			if string(p.Type) == m.appConfig.ActiveProvider {
				current = " (active)"
			}

			item := fmt.Sprintf("%s%s", p.DisplayName, current)

			if i == m.providerMenuIndex {
				item = commandSelectedStyle.Render(item)
			} else {
				item = commandItemStyle.Render(item)
			}
			items = append(items, item)
		}

	case 1:
		// API key input
		providerDef := config.GetProviderDefinition(config.ProviderType(m.selectedProviderType))
		name := m.selectedProviderType
		if providerDef != nil {
			name = providerDef.DisplayName
		}
		items = append(items, lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Enter API key for %s:", name)))
		items = append(items, "")
		// Show input with cursor (mask API key with asterisks for security)
		maskedInput := strings.Repeat("*", len(m.providerInput))
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Blink(true).Render("█")
		items = append(items, fmt.Sprintf("> %s%s", maskedInput, cursor))
		items = append(items, "")
		items = append(items, statsStyle.Render("(Press Enter to confirm, Esc to cancel)"))

	case 2:
		// URL input
		providerDef := config.GetProviderDefinition(config.ProviderType(m.selectedProviderType))
		name := m.selectedProviderType
		if providerDef != nil {
			name = providerDef.DisplayName
		}
		items = append(items, lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Enter URL for %s:", name)))
		items = append(items, "")
		// Show input with cursor
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Blink(true).Render("█")
		items = append(items, fmt.Sprintf("> %s%s", m.providerInput, cursor))
		items = append(items, "")
		items = append(items, statsStyle.Render("(Press Enter to confirm, Esc to cancel)"))
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// renderModelsMenu renders the model selection menu
func (m Model) renderModelsMenu() string {
	if !m.showModelsMenu || len(m.availableModels) == 0 {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Model (Enter to select, Esc to cancel):"))
	items = append(items, "")

	for i, model := range m.availableModels {
		current := ""
		if model == m.appConfig.DefaultModel {
			current = " (current)"
		}

		item := fmt.Sprintf("%s%s", model, current)

		if i == m.modelsMenuIndex {
			item = commandSelectedStyle.Render(item)
		} else {
			item = commandItemStyle.Render(item)
		}
		items = append(items, item)
	}

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}
