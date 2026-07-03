package tui

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/commands"
	"github.com/A2gent/brute/internal/config"
	httpserver "github.com/A2gent/brute/internal/http"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
	messages       []message
	taskSummary    string
	initialRunTask string
	serverPort     int
	width          int
	height         int
	ready          bool

	// Token tracking
	totalInputTokens  int
	totalOutputTokens int
	contextWindow     int // in tokens

	// Message queue for when processing
	queuedMessages []string

	// Timing
	lastUserInputTime time.Time
	processing        bool
	loadingFrames     []string
	loadingIndex      int
	activeRunStatus   string
	activeRunDetail   string

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

	// Workflow/agent selection state
	showAgentsMenu     bool
	agentsMenuIndex    int
	availableWorkflows []tuiWorkflow
	selectedWorkflow   tuiWorkflow

	// Config reference for persistence
	appConfig *config.Config

	// HTTP server port updates
	serverPortUpdates <-chan int
	sessionEvents     <-chan httpserver.ChatStreamEvent

	// Memory tracking
	memoryMB float64

	// Session sync tracking
	lastSyncedMessageCount     int
	lastSyncedSessionUpdatedAt time.Time

	// Question prompt state
	showQuestionPrompt  bool
	pendingQuestion     *session.QuestionData
	questionOptionIndex int // Selected option index (-1 = custom answer)
}

func New(
	sess *session.Session,
	sessionManager *session.Manager,
	agentConfig agent.Config,
	llmClient llm.Client,
	toolManager *tools.Manager,
	initialTask string,
	appConfig *config.Config,
	serverPort int,
	serverPortUpdates <-chan int,
	sessionEvents <-chan httpserver.ChatStreamEvent,
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
	selectedWorkflow := workflowFromSessionMetadata(sess)
	if selectedWorkflow.ID == "" {
		selectedWorkflow = builtinUserMainWorkflow()
	}

	// Determine context window from the configured provider/model so token
	// accounting stays consistent with HTTP session execution.
	contextWindow := 0
	if appConfig != nil {
		providerType := config.ProviderType(config.NormalizeProviderRef(appConfig.ActiveProvider))
		provider := appConfig.Providers[string(providerType)]
		model := agentConfig.Model
		if model == "" {
			model = provider.Model
		}
		if model == "" {
			model = appConfig.DefaultModel
		}
		contextWindow = config.ResolveContextWindow(providerType, provider, model)
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
		initialRunTask:    strings.TrimSpace(initialTask),
		serverPort:        serverPort,
		lastUserInputTime: time.Now(),
		loadingFrames:     []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		loadingIndex:      0,
		contextWindow:     contextWindow,
		commandRegistry:   cmdRegistry,
		filteredCommands:  cmdRegistry.GetCommands(),
		appConfig:         appConfig,
		serverPortUpdates: serverPortUpdates,
		sessionEvents:     sessionEvents,
		selectedWorkflow:  selectedWorkflow,
	}

	// Load existing messages from session
	for _, msg := range sess.Messages {
		m.messages = append(m.messages, message{
			id:          msg.ID,
			role:        msg.Role,
			content:     msg.Content,
			timestamp:   msg.Timestamp,
			toolCalls:   msg.ToolCalls,
			toolResults: msg.ToolResults,
			metadata:    msg.Metadata,
		})
	}
	m.lastSyncedMessageCount = len(sess.Messages)
	m.lastSyncedSessionUpdatedAt = sess.UpdatedAt
	m.applySessionTokenMetadata(sess)

	return m
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		tickCmd(),
		updateMemoryCmd(),
		serverPortCmd(m.serverPortUpdates),
		sessionEventCmd(m.sessionEvents),
		sessionSyncCmd(m.sessionManager, m.session.ID),
	}
	if strings.TrimSpace(m.initialRunTask) != "" {
		task := m.initialRunTask
		cmds = append(cmds, func() tea.Msg {
			return startInitialRunMsg{task: task}
		})
	}
	return tea.Batch(cmds...)
}

func serverPortCmd(portUpdates <-chan int) tea.Cmd {
	if portUpdates == nil {
		return nil
	}
	return func() tea.Msg {
		port, ok := <-portUpdates
		if !ok {
			return nil
		}
		return serverPortMsg{port: port}
	}
}

func sessionEventCmd(events <-chan httpserver.ChatStreamEvent) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return externalSessionEventMsg{event: event}
	}
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
// SetSize sets the terminal size
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.textarea.SetWidth(width)
	if m.ready {
		m.viewport.Width = width
		m.updateViewportHeight()
	}
}

func (m Model) hasDiscussion() bool {
	return len(m.messages) > 0
}

func (m Model) hasActiveRunIndicator() bool {
	if m.processing {
		return true
	}
	return m.session != nil && m.hasDiscussion() && m.session.Status == session.StatusRunning
}

func (m Model) calculateViewportHeight() int {
	fixedHeight := 2 // top bar + bottom bar
	if m.hasDiscussion() || m.showQuestionPrompt {
		fixedHeight += 4 // bottom textarea + active workflow/agent line
	}
	if m.hasActiveRunIndicator() {
		fixedHeight++
	}
	viewportHeight := m.height - fixedHeight - m.calculateQuestionPromptHeight()
	if viewportHeight < 1 {
		viewportHeight = 1
	}
	return viewportHeight
}

func (m *Model) updateViewportHeight() {
	if !m.ready {
		return
	}
	m.viewport.Height = m.calculateViewportHeight()
}

// executeCommand executes a slash command and returns the updated model
