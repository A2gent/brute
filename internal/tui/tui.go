// Package tui provides a terminal user interface for the aagent application
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/commands"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/autorouter"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
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

	statusInputRequiredStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#9C27B0")) // Purple

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

	// Textarea border style
	textareaBorderStyle = lipgloss.NewStyle().
				BorderLeft(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("#00AAFF")). // Light blue
				PaddingLeft(1)

	// Model indicator style
	modelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	// Path style
	pathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)

// ASCII art for empty state
const asciiArt = `
         █████╗ ██████╗     ██████╗ ██████╗ ██╗   ██╗████████╗███████╗
        ██╔══██╗╚════██╗    ██╔══██╗██╔══██╗██║   ██║   ██║   ██╔════╝
        ███████║ █████╔╝    ██████╔╝██████╔╝██║   ██║   ██║   █████╗  
        ██╔══██║██╔═══╝     ██╔══██╗██╔══██╗██║   ██║   ██║   ██╔══╝  
        ██║  ██║███████╗    ██████╔╝██║  ██║╚██████╔╝   ██║   ███████╗
        ╚═╝  ╚═╝╚══════╝    ╚═════╝ ╚═╝  ╚═╝ ╚═════╝    ╚═╝   ╚══════╝
`

// Tool icons for visual distinction in the TUI
var toolIcons = map[string]string{
	"bash":          "", // Terminal icon
	"read":          "", // File read icon
	"write":         "", // File write icon
	"edit":          "", // Edit icon
	"replace_lines": "",
	"glob":          "", // Search files icon
	"find_files":    "",
	"grep":          "", // Search content icon
	"task":          "", // Sub-agent icon
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
	case "replace_lines":
		return toolEditStyle
	case "glob":
		return toolGlobStyle
	case "find_files":
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

	case "replace_lines":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		start, hasStart := input["start_line"].(float64)
		end, hasEnd := input["end_line"].(float64)
		if hasStart && hasEnd {
			display.Details = append(display.Details, fmt.Sprintf("lines %.0f-%.0f", start, end))
		}

	case "glob":
		if pattern, ok := input["pattern"].(string); ok {
			display.Summary = pattern
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "find_files":
		if pattern, ok := input["pattern"].(string); ok && pattern != "" {
			display.Summary = pattern
		} else {
			display.Summary = "**/*"
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
	showSessionsList   bool
	sessionsListIndex  int
	sessionsListOffset int // Scroll offset for long lists
	availableSessions  []*session.Session

	// Logs view state
	showLogsView bool
	logLines     []string
	logTop       int
	logFollow    bool

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

	// Projects selection state
	showProjectsMenu    bool
	projectsMenuIndex   int
	availableProjects   []*session.Project
	selectedProjectID   *string
	selectedProjectName string

	// Config reference for persistence
	appConfig *config.Config

	// Memory tracking
	memoryMB float64

	// Session sync tracking
	lastSyncedMessageCount int

	// Question prompt state
	showQuestionPrompt  bool
	pendingQuestion     *session.QuestionData
	questionOptionIndex int // Selected option index (-1 = custom answer)

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
	ta.Placeholder = ""
	ta.SetHeight(3)
	ta.Focus()
	ta.CharLimit = 0 // Unlimited
	ta.ShowLineNumbers = false
	ta.Prompt = "│ " // Use light blue vertical line as prompt instead of border

	// Style the textarea with dark gray background and white text
	darkGray := lipgloss.Color("#1a1a1a")
	white := lipgloss.Color("#ffffff")
	lightBlue := lipgloss.Color("#00AAFF")
	placeholderGray := lipgloss.Color("#666666")

	ta.FocusedStyle.Base = lipgloss.NewStyle().
		Background(darkGray)
	ta.BlurredStyle.Base = lipgloss.NewStyle().
		Background(darkGray)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().
		Background(darkGray)
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle().
		Background(darkGray)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().
		Foreground(placeholderGray).
		Background(darkGray)
	ta.BlurredStyle.Placeholder = lipgloss.NewStyle().
		Foreground(placeholderGray).
		Background(darkGray)
	ta.FocusedStyle.Text = lipgloss.NewStyle().
		Foreground(white).
		Background(darkGray)
	ta.BlurredStyle.Text = lipgloss.NewStyle().
		Foreground(white).
		Background(darkGray)
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().
		Foreground(lightBlue).
		Background(darkGray)
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().
		Foreground(lightBlue).
		Background(darkGray)

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

// saveSessionIfNotEmpty persists the active session only after the conversation started.
func (m *Model) saveSessionIfNotEmpty() {
	if m.session == nil {
		return
	}
	if len(m.session.Messages) == 0 {
		return
	}
	_ = m.sessionManager.Save(m.session)
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

		// Height calculation: total - topBar(1) - textarea(3) - bottomBar(1) = total - 5
		// If question prompt is shown, also subtract its height
		fixedHeight := 5 // topBar + textarea + bottomBar
		questionHeight := m.calculateQuestionPromptHeight()
		viewportHeight := msg.Height - fixedHeight - questionHeight
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
		// Handle command menu first (highest priority - works even over question prompt)
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

		// Handle question prompt
		if m.showQuestionPrompt && m.pendingQuestion != nil {
			switch msg.Type {
			case tea.KeyCtrlC:
				// Always allow Ctrl+C to exit
				return m, tea.Quit
			case tea.KeyEsc:
				// Don't allow escaping question prompt - user must answer
				return m, nil
			case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
				// Allow scrolling viewport even when question is shown
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			case tea.KeyUp:
				// Use Alt+Up for viewport scroll, plain Up for option navigation
				if msg.Alt {
					m.viewport, vpCmd = m.viewport.Update(msg)
					return m, vpCmd
				}
				// Navigate up: custom (-1) -> last option -> ... -> first option
				if m.questionOptionIndex == -1 {
					// From custom to last option
					m.questionOptionIndex = len(m.pendingQuestion.Options) - 1
				} else if m.questionOptionIndex > 0 {
					m.questionOptionIndex--
				}
				return m, nil
			case tea.KeyDown:
				// Use Alt+Down for viewport scroll, plain Down for option navigation
				if msg.Alt {
					m.viewport, vpCmd = m.viewport.Update(msg)
					return m, vpCmd
				}
				// Navigate down: first option -> ... -> last option -> custom (-1)
				if m.questionOptionIndex < len(m.pendingQuestion.Options)-1 {
					m.questionOptionIndex++
				} else if m.pendingQuestion.Custom {
					// Go to custom answer position
					m.questionOptionIndex = -1
				}
				return m, nil
			case tea.KeyEnter:
				// Submit answer
				var answer string

				if m.questionOptionIndex == -1 {
					// Custom answer - use textarea value
					answer = strings.TrimSpace(m.textarea.Value())
				} else if m.questionOptionIndex >= 0 && m.questionOptionIndex < len(m.pendingQuestion.Options) {
					// Selected option
					answer = m.pendingQuestion.Options[m.questionOptionIndex].Label
				}

				if answer != "" {
					// Send answer
					if err := m.sessionManager.AnswerQuestion(m.session.ID, answer); err != nil {
						m.messages = append(m.messages, message{
							role:      "error",
							content:   fmt.Sprintf("Failed to answer question: %v", err),
							timestamp: time.Now(),
						})
					} else {
						// Clear question state
						m.showQuestionPrompt = false
						m.pendingQuestion = nil
						m.textarea.Reset() // Clear textarea

						// Recalculate viewport height now that question is hidden
						fixedHeight := 5 // topBar + textarea + bottomBar
						questionHeight := m.calculateQuestionPromptHeight()
						viewportHeight := m.height - fixedHeight - questionHeight
						if viewportHeight < 1 {
							viewportHeight = 1
						}
						m.viewport.Height = viewportHeight

						// Reload session
						if sess, err := m.sessionManager.Get(m.session.ID); err == nil {
							m.session = sess
							// Resume agent if status is running
							if sess.Status == session.StatusRunning {
								m.processing = true
								m.lastUserInputTime = time.Now()
								cmd, cancel := m.runAgentResume()
								m.cancelFunc = cancel
								m.cancelPending = false
								cmds = append(cmds, cmd)
							}
						}
					}
					m.viewport.SetContent(m.renderMessages())
					m.viewport.GotoBottom()
				}
				return m, tea.Batch(cmds...)
			case tea.KeyRunes:
				// Allow slash commands even when question is shown
				if len(msg.Runes) > 0 && msg.Runes[0] == '/' && m.textarea.Value() == "" && m.questionOptionIndex == -1 {
					m.showCommandMenu = true
					m.commandMenuIndex = 0
					m.filteredCommands = m.commandRegistry.GetCommands()
					return m, nil
				}
				// If custom answer is selected, let textarea handle input
				if m.questionOptionIndex == -1 && m.pendingQuestion.Custom {
					m.textarea, taCmd = m.textarea.Update(msg)
					return m, taCmd
				}
				// Otherwise ignore other keys
				return m, nil
			default:
				// If custom answer is selected, let textarea handle input
				if m.questionOptionIndex == -1 && m.pendingQuestion.Custom {
					m.textarea, taCmd = m.textarea.Update(msg)
					return m, taCmd
				}
				// Otherwise ignore other keys
				return m, nil
			}
		}

		// Handle logs view first
		if m.showLogsView {
			switch msg.Type {
			case tea.KeyEsc:
				m.showLogsView = false
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case tea.KeyUp:
				if m.logTop > 0 {
					m.logTop--
				}
				m.logFollow = false
				return m, nil
			case tea.KeyDown:
				maxTop := m.maxLogsTop()
				if m.logTop < maxTop {
					m.logTop++
				}
				if m.logTop >= maxTop {
					m.logFollow = true
				}
				return m, nil
			case tea.KeyPgUp:
				m.logTop -= m.logsPageStep()
				if m.logTop < 0 {
					m.logTop = 0
				}
				m.logFollow = false
				return m, nil
			case tea.KeyPgDown:
				m.logTop += m.logsPageStep()
				maxTop := m.maxLogsTop()
				if m.logTop > maxTop {
					m.logTop = maxTop
				}
				if m.logTop >= maxTop {
					m.logFollow = true
				}
				return m, nil
			case tea.KeyHome:
				m.logTop = 0
				m.logFollow = false
				return m, nil
			case tea.KeyEnd:
				m.logTop = m.maxLogsTop()
				m.logFollow = true
				return m, nil
			}
			return m, nil
		}

		// Handle sessions list view first
		if m.showSessionsList {
			switch msg.Type {
			case tea.KeyEsc:
				m.showSessionsList = false
				m.sessionsListOffset = 0
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
			case tea.KeyPgUp:
				// Scroll up by page
				pageSize := m.height - 8
				if pageSize < 3 {
					pageSize = 3
				}
				m.sessionsListIndex -= pageSize
				if m.sessionsListIndex < 0 {
					m.sessionsListIndex = 0
				}
				return m, nil
			case tea.KeyPgDown:
				// Scroll down by page
				pageSize := m.height - 8
				if pageSize < 3 {
					pageSize = 3
				}
				m.sessionsListIndex += pageSize
				if m.sessionsListIndex >= len(m.availableSessions) {
					m.sessionsListIndex = len(m.availableSessions) - 1
				}
				return m, nil
			case tea.KeyHome:
				m.sessionsListIndex = 0
				return m, nil
			case tea.KeyEnd:
				m.sessionsListIndex = len(m.availableSessions) - 1
				return m, nil
			case tea.KeyEnter:
				if len(m.availableSessions) > 0 {
					selectedSession := m.availableSessions[m.sessionsListIndex]
					m = m.switchToSession(selectedSession.ID)
					m.showSessionsList = false
					m.sessionsListOffset = 0
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

		// Handle projects menu
		if m.showProjectsMenu {
			switch msg.Type {
			case tea.KeyEsc:
				m.showProjectsMenu = false
				m.viewport.SetContent(m.renderMessages())
				return m, nil
			case tea.KeyUp:
				if m.projectsMenuIndex > 0 {
					m.projectsMenuIndex--
				}
				return m, nil
			case tea.KeyDown:
				// +1 for "No project" option at index 0
				if m.projectsMenuIndex < len(m.availableProjects) {
					m.projectsMenuIndex++
				}
				return m, nil
			case tea.KeyEnter:
				return m.selectProject()
			}
			return m, nil
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
						m.saveSessionIfNotEmpty()
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
				m.saveSessionIfNotEmpty()
			}
			return m, tea.Quit

		case tea.KeyEsc:
			// Save session before quitting
			if m.session != nil {
				m.saveSessionIfNotEmpty()
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
		if m.showLogsView {
			m.refreshLogsView()
		}
		cmds = append(cmds, tickCmd(), updateMemoryCmd())

	case memoryUpdateMsg:
		m.memoryMB = msg.memoryMB

	case sessionSyncMsg:
		if msg.session != nil {
			// Check if session status changed to input_required
			if msg.session.Status == session.StatusInputRequired && !m.showQuestionPrompt {
				// Load pending question
				question, err := m.sessionManager.GetPendingQuestion(msg.session.ID)
				if err == nil && question != nil {
					m.pendingQuestion = question
					m.showQuestionPrompt = true
					m.questionOptionIndex = 0
					m.processing = false // Stop processing, wait for answer

					// Recalculate viewport height now that question is shown
					fixedHeight := 5 // topBar + textarea + bottomBar
					questionHeight := m.calculateQuestionPromptHeight()
					viewportHeight := m.height - fixedHeight - questionHeight
					if viewportHeight < 1 {
						viewportHeight = 1
					}
					m.viewport.Height = viewportHeight
				}
			}

			// Check if there are new messages from external sources (e.g., web app)
			// Always sync if session has more messages than we've seen, even during processing.
			// This ensures TUI stays in sync with changes from web-app or other sources.
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
			} else {
				// Update session reference even if no new messages
				m.session = msg.session
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

			// Check if session is waiting for input
			if freshSess, err := m.sessionManager.Get(m.session.ID); err == nil {
				m.session = freshSess
				if freshSess.Status == session.StatusInputRequired {
					// Load pending question immediately
					question, qErr := m.sessionManager.GetPendingQuestion(freshSess.ID)
					if qErr == nil && question != nil {
						m.pendingQuestion = question
						m.showQuestionPrompt = true
						m.questionOptionIndex = 0
						logging.Debug("TUI: Loaded pending question: %s", question.Header)

						// Recalculate viewport height now that question is shown
						fixedHeight := 5 // topBar + textarea + bottomBar
						questionHeight := m.calculateQuestionPromptHeight()
						viewportHeight := m.height - fixedHeight - questionHeight
						if viewportHeight < 1 {
							viewportHeight = 1
						}
						m.viewport.Height = viewportHeight
					}
				}
			}

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
		m.saveSessionIfNotEmpty()
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

	// Messages viewport - show ASCII art if no messages
	var messagesView string
	if len(m.messages) == 0 {
		// Center the ASCII art
		artStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true).
			Width(m.viewport.Width).
			Height(m.viewport.Height).
			Align(lipgloss.Center, lipgloss.Center)
		messagesView = artStyle.Render(asciiArt)
	} else {
		messagesView = m.viewport.View()
	}

	// Check if we should show sessions list overlay
	if m.showLogsView {
		logsView := m.renderLogsView()
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			logsView,
		)
	}

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

	// Check if we should show projects menu overlay
	if m.showProjectsMenu {
		projectsView := m.renderProjectsMenu()
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			projectsView,
		)
	}

	// Question prompt (rendered above input if active)
	var questionPrompt string
	if m.showQuestionPrompt {
		questionPrompt = m.renderQuestionPrompt() + "\n"
	}

	// Command menu (rendered above input if active)
	var commandMenu string
	if m.showCommandMenu {
		commandMenu = m.renderCommandMenu() + "\n"
	}

	// Input area - show textarea for custom answer, placeholder for option selection
	var inputView string
	if m.showQuestionPrompt && m.questionOptionIndex >= 0 {
		// Option is selected - show placeholder
		disabledStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1a1a")).
			Foreground(lipgloss.Color("#666666")).
			Width(m.width)

		selectedOption := ""
		if m.questionOptionIndex < len(m.pendingQuestion.Options) {
			selectedOption = m.pendingQuestion.Options[m.questionOptionIndex].Label
		}
		inputView = disabledStyle.Render("│ Selected: " + selectedOption + " (press Enter to submit, ↓ for custom)")
	} else {
		// Normal textarea (for regular input or custom answer)
		textareaContent := m.textarea.View()

		// Ensure the textarea takes up exactly 3 lines with full width background
		lines := strings.Split(textareaContent, "\n")
		paddedLines := make([]string, 0, 3)
		for i := 0; i < 3; i++ {
			var line string
			if i < len(lines) {
				line = lines[i]
			} else {
				line = "│ " // Empty line with prompt
			}
			// Pad each line to full width with background
			paddedLine := lipgloss.NewStyle().
				Background(lipgloss.Color("#1a1a1a")).
				Width(m.width).
				Render(line)
			paddedLines = append(paddedLines, paddedLine)
		}
		inputView = strings.Join(paddedLines, "\n")
	}

	// Help text (now on the right side)
	var helpStr string
	if m.showQuestionPrompt {
		if m.pendingQuestion != nil && m.pendingQuestion.Custom {
			helpStr = "↑↓: navigate • type: custom answer • enter: submit"
		} else {
			helpStr = "↑↓: navigate • enter: submit"
		}
	} else if m.showCommandMenu {
		helpStr = "↑↓: navigate • enter/tab: select • esc: cancel"
	} else if m.processing {
		helpStr = "ctrl+c: cancel • esc: quit • enter: queue message • /: commands"
	} else {
		helpStr = "esc: quit • enter: send • alt+enter: new line • /: commands"
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	}

	// Bottom bar with path on left and shortcuts on right
	pathText := pathStyle.Render(cwd)
	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Render(helpStr)

	// Calculate space between path and help
	bottomUsedWidth := lipgloss.Width(pathText) + lipgloss.Width(helpText)
	bottomSpace := m.width - bottomUsedWidth
	if bottomSpace < 1 {
		bottomSpace = 1
	}

	bottomBar := lipgloss.JoinHorizontal(
		lipgloss.Left,
		pathText,
		strings.Repeat(" ", bottomSpace),
		helpText,
	)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		topBar,
		messagesView,
		questionPrompt+commandMenu+inputView,
		bottomBar,
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

// renderTopBar renders the top bar with task summary, token stats, project/session, and time
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

	// Show project name instead of session ID in the header
	var contextInfo string
	if m.selectedProjectName != "" {
		contextInfo = m.selectedProjectName
	} else if m.session.ProjectID != nil {
		// Try to get project name from session's project
		if project, err := m.sessionManager.GetProject(*m.session.ProjectID); err == nil {
			contextInfo = project.Name
			// Cache it for future renders
			m.selectedProjectID = m.session.ProjectID
			m.selectedProjectName = project.Name
		}
	}

	var taskText string
	if contextInfo != "" {
		taskText = statsStyle.Render(contextInfo+" / ") + taskStyle.Render(summary)
	} else {
		taskText = taskStyle.Render(summary)
	}

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
	case session.StatusInputRequired:
		statusIcon = statusInputRequiredStyle.Render("?")
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

	// Model name in the center
	modelName := m.agentConfig.Model
	if modelName == "" {
		modelName = "default"
	}
	modelText := modelStyle.Render("⚡ " + modelName)

	// Calculate spacing to center the model
	leftWidth := lipgloss.Width(taskText)
	rightWidth := lipgloss.Width(rightSide)
	centerWidth := lipgloss.Width(modelText)

	totalUsed := leftWidth + centerWidth + rightWidth
	if totalUsed >= m.width {
		// Not enough space for centering, just show left and right
		space := m.width - leftWidth - rightWidth
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

	// Center the model text
	remainingSpace := m.width - totalUsed
	leftSpace := (remainingSpace / 2) + 1
	rightSpace := remainingSpace - leftSpace + 2

	if leftSpace < 1 {
		leftSpace = 1
	}
	if rightSpace < 1 {
		rightSpace = 1
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		taskText,
		strings.Repeat(" ", leftSpace),
		modelText,
		strings.Repeat(" ", rightSpace),
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
	case "projects":
		return m.showProjectsSelection()
	case "provider":
		return m.showProviderSelection()
	case "models":
		return m.showModelsSelection()
	case "clear":
		return m.clearConversation()
	case "help":
		return m.showHelp()
	case "logs":
		return m.showLogs()
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
		m.saveSessionIfNotEmpty()
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

	// Filter sessions by selected project
	var filteredSessions []*session.Session
	for _, s := range sessions {
		if m.selectedProjectID == nil {
			// No project selected - show sessions without project
			if s.ProjectID == nil {
				filteredSessions = append(filteredSessions, s)
			}
		} else {
			// Project selected - show only sessions for this project
			if s.ProjectID != nil && *s.ProjectID == *m.selectedProjectID {
				filteredSessions = append(filteredSessions, s)
			}
		}
	}

	m.availableSessions = filteredSessions
	m.sessionsListIndex = 0
	m.sessionsListOffset = 0
	m.showSessionsList = true

	// Find current session in list
	for i, s := range filteredSessions {
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
		m.saveSessionIfNotEmpty()
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

func (m Model) showLogs() (tea.Model, tea.Cmd) {
	m.showLogsView = true
	m.logFollow = true
	m.refreshLogsView()
	return m, nil
}

func (m *Model) refreshLogsView() {
	lines := logging.RecentLines(600)
	if len(lines) == 0 {
		filePath := strings.TrimSpace(logging.GetLogPath())
		if filePath != "" {
			if tailed := tailLogFile(filePath, 600); len(tailed) > 0 {
				lines = tailed
			}
		}
	}
	if len(lines) == 0 {
		lines = []string{"No logs available yet. Start activity, then reopen /logs."}
	}
	m.logLines = lines
	maxTop := m.maxLogsTop()
	if m.logFollow {
		m.logTop = maxTop
	} else if m.logTop > maxTop {
		m.logTop = maxTop
	}
	if m.logTop < 0 {
		m.logTop = 0
	}
}

func tailLogFile(path string, maxLines int) []string {
	if strings.TrimSpace(path) == "" || maxLines <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

func (m Model) logsVisibleLines() int {
	visible := m.height - 10
	if visible < 6 {
		visible = 6
	}
	return visible
}

func (m Model) logsPageStep() int {
	step := m.logsVisibleLines() - 2
	if step < 1 {
		step = 1
	}
	return step
}

func (m Model) maxLogsTop() int {
	visible := m.logsVisibleLines()
	if len(m.logLines) <= visible {
		return 0
	}
	return len(m.logLines) - visible
}

func truncateLine(line string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= maxLen {
		return line
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func (m Model) renderLogsView() string {
	lines := m.logLines
	if len(lines) == 0 {
		lines = []string{"No logs captured in this process yet."}
	}

	visible := m.logsVisibleLines()
	start := m.logTop
	if start < 0 {
		start = 0
	}
	if start > len(lines)-1 {
		start = len(lines) - 1
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}

	width := m.width - 6
	if width < 20 {
		width = 20
	}

	var out []string
	header := fmt.Sprintf(
		"Logs (live)  file: %s",
		logging.GetLogPath(),
	)
	out = append(out, lipgloss.NewStyle().Bold(true).Render(truncateLine(header, width)))
	out = append(out, "")

	for _, line := range lines[start:end] {
		out = append(out, truncateLine(line, width))
	}

	help := "↑/↓: scroll  pgup/pgdn: page  home/end: start/end  esc: close"
	follow := "paused"
	if m.logFollow {
		follow = "following"
	}
	position := fmt.Sprintf("lines %d-%d/%d (%s)", start+1, end, len(lines), follow)
	out = append(out, "")
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render(truncateLine(position, width)))
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render(truncateLine(help, width)))

	content := strings.Join(out, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// calculateQuestionPromptHeight calculates how many lines the question prompt will take
func (m Model) calculateQuestionPromptHeight() int {
	if !m.showQuestionPrompt || m.pendingQuestion == nil {
		return 0
	}

	height := 0
	height += 1                              // Top separator
	height += 1                              // Header line
	height += len(m.pendingQuestion.Options) // Options (one per line)
	if m.pendingQuestion.Custom {
		height += 1 // Empty line before custom hint
		height += 1 // Custom hint line
	}
	height += 1 // Bottom separator

	return height
}

// renderQuestionPrompt renders the question prompt overlay
func (m Model) renderQuestionPrompt() string {
	if !m.showQuestionPrompt || m.pendingQuestion == nil {
		return ""
	}

	var sb strings.Builder

	// Header (compact, one line)
	questionHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FF9800"))

	sb.WriteString(questionHeaderStyle.Render("❓ " + m.pendingQuestion.Header + ": " + m.pendingQuestion.Question))
	sb.WriteString("\n")

	// Options (compact, one line each)
	optionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#AAAAAA"))

	selectedOptionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#2196F3")).
		Bold(true)

	for i, opt := range m.pendingQuestion.Options {
		var icon string
		var style lipgloss.Style

		if m.pendingQuestion.Multiple {
			icon = "☐"
			if i == m.questionOptionIndex {
				icon = "☑"
			}
		} else {
			icon = "○"
			if i == m.questionOptionIndex {
				icon = "◉"
			}
		}

		if i == m.questionOptionIndex {
			style = selectedOptionStyle
		} else {
			style = optionStyle
		}

		text := fmt.Sprintf("  %s %s", icon, opt.Label)
		sb.WriteString(style.Render(text))
		sb.WriteString("\n")
	}

	// Custom answer hint if enabled
	if m.pendingQuestion.Custom {
		sb.WriteString("\n")

		// Check if custom field is selected
		isCustomSelected := m.questionOptionIndex == -1

		var hintStyle lipgloss.Style
		var hintText string
		if isCustomSelected {
			hintStyle = selectedOptionStyle
			hintText = "  💡 Custom answer (type below) ▼"
		} else {
			hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
			hintText = "  💡 Custom answer (press ↓ to select)"
		}
		sb.WriteString(hintStyle.Render(hintText))
	}

	// Simple separator line instead of border (more compact)
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF9800")).
		Render(strings.Repeat("─", m.width))

	return separator + "\n" + sb.String() + separator
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

	// Group sessions by day
	type sessionWithIndex struct {
		sess  *session.Session
		index int
	}

	grouped := make(map[string][]sessionWithIndex)
	for i, sess := range m.availableSessions {
		dayKey := sess.CreatedAt.Format("2006-01-02")
		grouped[dayKey] = append(grouped[dayKey], sessionWithIndex{sess: sess, index: i})
	}

	// Sort days in reverse chronological order
	var days []string
	for day := range grouped {
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	// Build flat list of items with their types
	type listItem struct {
		isHeader bool
		day      string
		session  sessionWithIndex
	}

	var items []listItem
	for _, day := range days {
		items = append(items, listItem{isHeader: true, day: day})
		sessions := grouped[day]
		for _, s := range sessions {
			items = append(items, listItem{isHeader: false, session: s})
		}
	}

	// Calculate visible range based on scroll offset
	maxVisibleItems := m.height - 6 // Leave room for title, header, and borders
	if maxVisibleItems < 5 {
		maxVisibleItems = 5
	}

	// Ensure selected item is visible
	selectedItemIdx := 0
	for i, item := range items {
		if !item.isHeader && item.session.index == m.sessionsListIndex {
			selectedItemIdx = i
			break
		}
	}

	// Adjust offset to keep selected item in view
	if selectedItemIdx < m.sessionsListOffset {
		m.sessionsListOffset = selectedItemIdx
	} else if selectedItemIdx >= m.sessionsListOffset+maxVisibleItems {
		m.sessionsListOffset = selectedItemIdx - maxVisibleItems + 1
	}

	// Ensure offset doesn't go negative
	if m.sessionsListOffset < 0 {
		m.sessionsListOffset = 0
	}

	// Render visible items
	var rendered []string
	var headerText string
	if m.selectedProjectName != "" {
		headerText = fmt.Sprintf("Sessions for '%s' (Enter to switch, Esc to cancel):", m.selectedProjectName)
	} else {
		headerText = "Sessions (ungrouped) (Enter to switch, Esc to cancel):"
	}
	rendered = append(rendered, lipgloss.NewStyle().Bold(true).Render(headerText))
	rendered = append(rendered, "")

	// Calculate end index
	endIdx := m.sessionsListOffset + maxVisibleItems
	if endIdx > len(items) {
		endIdx = len(items)
	}

	for i := m.sessionsListOffset; i < endIdx; i++ {
		item := items[i]

		if item.isHeader {
			// Format day header
			day, _ := time.Parse("2006-01-02", item.day)
			today := time.Now().Truncate(24 * time.Hour)
			dayStart := day.Truncate(24 * time.Hour)

			var dayLabel string
			daysDiff := int(today.Sub(dayStart).Hours() / 24)
			switch daysDiff {
			case 0:
				dayLabel = "Today"
			case 1:
				dayLabel = "Yesterday"
			default:
				dayLabel = day.Format("Monday, Jan 2")
			}

			header := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7D56F4")).
				Render("  " + dayLabel)
			rendered = append(rendered, header)
		} else {
			// Format session entry
			sess := item.session.sess
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

			entry := fmt.Sprintf("    %s  %s%s",
				sess.CreatedAt.Format("15:04"),
				title,
				current,
			)

			if item.session.index == m.sessionsListIndex {
				entry = commandSelectedStyle.Render("  " + entry)
			} else {
				entry = commandItemStyle.Render("  " + entry)
			}
			rendered = append(rendered, entry)
		}
	}

	// Add scroll indicators if needed
	if m.sessionsListOffset > 0 {
		rendered[1] = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render("  ▲ more above")
	}
	if endIdx < len(items) {
		rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render("  ▼ more below"))
	}

	// Add help text
	help := fmt.Sprintf("↑/↓: navigate  pgup/pgdn: page  home/end: top/bottom  enter: switch  esc: cancel")
	rendered = append(rendered, "")
	rendered = append(rendered, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render("  "+help))

	content := strings.Join(rendered, "\n")
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
		m.availableModels = []string{
			"openrouter/auto",
			"anthropic/claude-opus-4-6",
			"anthropic/claude-sonnet-4-6",
			"anthropic/claude-opus-4-5",
			"anthropic/claude-sonnet-4-5",
			"openai/gpt-4.1",
			"openai/gpt-4.1-mini",
			"google/gemini-3-flash-preview",
			"google/gemini-2.5-pro",
			"meta-llama/llama-4-maverick",
		}
	case config.ProviderAnthropic:
		m.availableModels = []string{
			"claude-opus-4-6",
			"claude-sonnet-4-6",
			"claude-opus-4-5",
			"claude-opus-4-5-20251101",
			"claude-sonnet-4-5",
			"claude-sonnet-4-5-20250929",
			"claude-haiku-4-5",
			"claude-opus-4-1",
			"claude-opus-4-0",
			"claude-sonnet-4-0",
		}
	case config.ProviderGoogle:
		m.availableModels = []string{
			"gemini-3-pro-preview",
			"gemini-3-flash-preview",
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.5-flash-image",
			"gemini-2.5-flash-lite",
			"gemini-2.0-flash",
			"gemini-2.0-flash-lite",
		}
	case config.ProviderOpenAI:
		m.availableModels = []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4o-mini"}
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

// showProjectsSelection shows the projects selection menu
func (m Model) showProjectsSelection() (tea.Model, tea.Cmd) {
	projects, err := m.sessionManager.ListProjects()
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to list projects: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.availableProjects = projects
	m.projectsMenuIndex = 0 // 0 = "No project" option
	m.showProjectsMenu = true

	// Find current project in list (index 0 = no project, real projects start at 1)
	if m.selectedProjectID != nil {
		for i, p := range projects {
			if p.ID == *m.selectedProjectID {
				m.projectsMenuIndex = i + 1 // +1 because 0 is "No project"
				break
			}
		}
	}

	return m, nil
}

// selectProject handles project selection
func (m Model) selectProject() (tea.Model, tea.Cmd) {
	if m.projectsMenuIndex == 0 {
		// "No project" selected
		m.selectedProjectID = nil
		m.selectedProjectName = ""
		m.messages = append(m.messages, message{
			role:      "system",
			content:   "Cleared project selection. New sessions will not be associated with any project.",
			timestamp: time.Now(),
		})
	} else {
		// A project is selected (index - 1 because 0 is "No project")
		projectIdx := m.projectsMenuIndex - 1
		if projectIdx < len(m.availableProjects) {
			project := m.availableProjects[projectIdx]
			m.selectedProjectID = &project.ID
			m.selectedProjectName = project.Name
			m.messages = append(m.messages, message{
				role:      "system",
				content:   fmt.Sprintf("Selected project: %s", project.Name),
				timestamp: time.Now(),
			})

			// Associate current session with the project if it exists
			if m.session != nil {
				m.session.ProjectID = m.selectedProjectID
				m.sessionManager.Save(m.session)
			}
		}
	}

	m.showProjectsMenu = false
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()

	return m, nil
}

// renderProjectsMenu renders the projects selection menu
func (m Model) renderProjectsMenu() string {
	if !m.showProjectsMenu {
		return ""
	}

	var items []string
	items = append(items, lipgloss.NewStyle().Bold(true).Render("Select Project (Enter to confirm, Esc to cancel):"))
	items = append(items, "")

	// "No project" option
	noProjectLabel := "  (No project)"
	if m.projectsMenuIndex == 0 {
		noProjectLabel = commandSelectedStyle.Render(noProjectLabel)
	} else {
		noProjectLabel = commandItemStyle.Render(noProjectLabel)
	}
	items = append(items, noProjectLabel)

	// Project options
	for i, project := range m.availableProjects {
		label := fmt.Sprintf("  %s", project.Name)
		if project.Folder != nil && *project.Folder != "" {
			// Show folder path shortened
			folder := *project.Folder
			if len(folder) > 30 {
				folder = "..." + folder[len(folder)-27:]
			}
			label += commandDescStyle.Render(fmt.Sprintf(" (%s)", folder))
		}
		if project.IsSystem {
			label += commandDescStyle.Render(" [system]")
		}

		// +1 because index 0 is "No project"
		if m.projectsMenuIndex == i+1 {
			// Re-render with selection style
			labelBase := fmt.Sprintf("  %s", project.Name)
			label = commandSelectedStyle.Render(labelBase)
			if project.Folder != nil && *project.Folder != "" {
				folder := *project.Folder
				if len(folder) > 30 {
					folder = "..." + folder[len(folder)-27:]
				}
				label += commandDescStyle.Render(fmt.Sprintf(" (%s)", folder))
			}
			if project.IsSystem {
				label += commandDescStyle.Render(" [system]")
			}
		} else {
			label = commandItemStyle.Render(label)
		}
		items = append(items, label)
	}

	items = append(items, "")
	items = append(items, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render("  ↑/↓: navigate  enter: select  esc: cancel"))

	content := strings.Join(items, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
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

	// For providers requiring API key (except if OAuth is configured for Anthropic)
	if providerDef.RequiresKey && existingProvider.APIKey == "" {
		// Check if Anthropic has OAuth configured
		hasOAuth := providerType == config.ProviderAnthropic &&
			existingProvider.OAuth != nil &&
			existingProvider.OAuth.AccessToken != ""
		if !hasOAuth {
			m.providerMenuStep = 1 // Go to API key input
			return m, nil
		}
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
	defaultClient := anthropic.NewClientWithBaseURL("", "kimi-k2.5", "https://api.kimi.com/coding/v1")
	if m.appConfig == nil {
		return defaultClient
	}

	createDirectClient := func(targetType config.ProviderType, modelOverride string) (llm.Client, string, error) {
		providerDef := config.GetProviderDefinition(targetType)
		if providerDef == nil || targetType == config.ProviderFallback || targetType == config.ProviderAutoRouter {
			return nil, "", fmt.Errorf("unsupported provider: %s", targetType)
		}

		provider := m.appConfig.Providers[string(targetType)]
		apiKey := strings.TrimSpace(provider.APIKey)
		if apiKey == "" {
			apiKey = providerAPIKeyFromEnv(targetType)
		}

		baseURL := strings.TrimSpace(provider.BaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(providerDef.DefaultURL)
		}
		if envURL := strings.TrimSpace(os.Getenv(strings.ToUpper(string(targetType)) + "_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else if envURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); envURL != "" && (targetType == config.ProviderKimi || targetType == config.ProviderAnthropic) {
			baseURL = envURL
		}

		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(provider.Model)
		}
		if model == "" {
			model = strings.TrimSpace(providerDef.DefaultModel)
		}
		if model == "" {
			model = strings.TrimSpace(m.appConfig.DefaultModel)
		}

		switch targetType {
		case config.ProviderGoogle:
			// Google Gemini uses a dedicated client with OpenAI-compatible API + Gemini extensions
			return gemini.NewClient(apiKey, model, baseURL), model, nil
		case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI:
			// Other OpenAI-compatible providers
			return lmstudio.NewClient(apiKey, model, baseURL), model, nil
		case config.ProviderAnthropic:
			// Anthropic supports OAuth or API key
			if provider.OAuth != nil && provider.OAuth.AccessToken != "" {
				tokens := &anthropic.OAuthTokens{
					AccessToken:  provider.OAuth.AccessToken,
					RefreshToken: provider.OAuth.RefreshToken,
					ExpiresIn:    int(provider.OAuth.ExpiresAt - time.Now().Unix()),
				}
				return anthropic.NewOAuthClient(tokens, model, nil), model, nil
			}
			return anthropic.NewClientWithBaseURL(apiKey, model, baseURL), model, nil
		default:
			return anthropic.NewClientWithBaseURL(apiKey, model, baseURL), model, nil
		}
	}

	createClientForProvider := func(providerRef string, modelOverride string) (llm.Client, string, error) {
		normalizedRef := config.NormalizeProviderRef(providerRef)
		if normalizedRef == "" {
			return nil, "", fmt.Errorf("provider reference is empty")
		}
		targetType := config.ProviderType(normalizedRef)
		if targetType == config.ProviderAutoRouter {
			return nil, "", fmt.Errorf("automatic_router cannot be used as nested target")
		}
		if normalizedRef == string(config.ProviderFallback) || config.IsFallbackAggregateRef(normalizedRef) {
			var chain []config.FallbackChainNode
			if normalizedRef == string(config.ProviderFallback) {
				fallbackProvider := m.appConfig.Providers[string(config.ProviderFallback)]
				chain = fallbackProvider.FallbackChainNodes
				if len(chain) == 0 {
					for _, raw := range fallbackProvider.FallbackChain {
						nodeType := config.ProviderType(config.NormalizeProviderRef(raw))
						if nodeType == "" || nodeType == config.ProviderFallback {
							continue
						}
						model := strings.TrimSpace(m.appConfig.Providers[string(nodeType)].Model)
						if model == "" {
							if nodeDef := config.GetProviderDefinition(nodeType); nodeDef != nil {
								model = strings.TrimSpace(nodeDef.DefaultModel)
							}
						}
						if model == "" {
							model = strings.TrimSpace(m.appConfig.DefaultModel)
						}
						if model == "" {
							continue
						}
						chain = append(chain, config.FallbackChainNode{Provider: string(nodeType), Model: model})
					}
				}
			} else {
				id := config.FallbackAggregateIDFromRef(normalizedRef)
				for _, aggregate := range m.appConfig.FallbackAggregates {
					if config.NormalizeToken(aggregate.ID) == id {
						chain = aggregate.Chain
						break
					}
				}
			}

			nodes := make([]fallback.Node, 0, len(chain))
			seen := make(map[string]struct{}, len(chain))
			for _, rawNode := range chain {
				nodeType := config.ProviderType(config.NormalizeProviderRef(rawNode.Provider))
				model := strings.TrimSpace(rawNode.Model)
				if nodeType == "" || nodeType == config.ProviderFallback || model == "" {
					continue
				}
				seenKey := string(nodeType) + "::" + model
				if _, exists := seen[seenKey]; exists {
					continue
				}
				seen[seenKey] = struct{}{}
				client, _, err := createDirectClient(nodeType, model)
				if err != nil {
					return nil, "", fmt.Errorf("fallback node %s/%s is unavailable: %w", nodeType, model, err)
				}
				nodes = append(nodes, fallback.Node{
					Name:   string(nodeType),
					Model:  model,
					Client: client,
				})
			}
			if len(nodes) < 2 {
				return nil, "", fmt.Errorf("%s requires at least two valid fallback model nodes", normalizedRef)
			}
			retries := m.appConfig.LLMRetries
			if retries <= 0 {
				retries = fallback.DefaultMaxRetries
			}
			return fallback.NewClient(nodes, fallback.WithMaxRetries(retries)), "", nil
		}

		client, model, err := createDirectClient(targetType, modelOverride)
		if err != nil {
			return nil, model, err
		}
		retries := m.appConfig.LLMRetries
		if retries <= 0 {
			retries = retry.DefaultMaxRetries
		}
		return retry.Wrap(client, retry.WithMaxRetries(retries)), model, nil
	}

	providerRef := config.NormalizeProviderRef(string(providerType))
	if providerRef == string(config.ProviderAutoRouter) {
		return autorouter.New(m.appConfig, createClientForProvider)
	}
	client, _, err := createClientForProvider(providerRef, "")
	if err != nil {
		logging.Warn("Failed to create LLM client for %s: %v", providerType, err)
		return defaultClient
	}
	return client
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
	if providerType == config.ProviderAutoRouter {
		provider := m.appConfig.Providers[string(config.ProviderAutoRouter)]
		routerProvider := config.NormalizeProviderRef(provider.RouterProvider)
		if routerProvider == "" {
			return fmt.Errorf("automatic router requires router_provider")
		}
		if routerProvider == string(config.ProviderAutoRouter) {
			return fmt.Errorf("automatic router cannot use automatic_router as router provider")
		}
		hasRule := false
		for _, rule := range provider.RouterRules {
			if strings.TrimSpace(rule.Match) == "" || strings.TrimSpace(rule.Provider) == "" {
				continue
			}
			hasRule = true
			if config.NormalizeProviderRef(rule.Provider) == string(config.ProviderAutoRouter) {
				return fmt.Errorf("routing rule %q cannot target automatic_router", strings.TrimSpace(rule.Match))
			}
		}
		if !hasRule {
			return fmt.Errorf("automatic router requires at least one routing rule")
		}
		return nil
	}
	if !def.RequiresKey {
		return nil
	}

	provider := m.appConfig.Providers[string(providerType)]

	// Check if Anthropic has OAuth configured
	if providerType == config.ProviderAnthropic &&
		provider.OAuth != nil &&
		provider.OAuth.AccessToken != "" {
		return nil
	}

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
	if envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	if providerType == config.ProviderGoogle {
		return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	return ""
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
	case config.ProviderOpenAI:
		return "OPENAI_API_KEY"
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
