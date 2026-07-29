package tui

import (
	"fmt"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) createNewSession() (tea.Model, tea.Cmd) {
	if m.session != nil {
		m.saveSessionIfNotEmpty()
	}

	newSess, err := m.sessionManager.Create(m.agentConfig.Name)
	if err != nil {
		m.messages = append(m.messages, message{role: "error", content: fmt.Sprintf("Failed to create new session: %v", err), timestamp: time.Now()})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}
	if m.selectedProjectID != nil {
		newSess.ProjectID = m.selectedProjectID
	}
	if err := m.sessionManager.Save(newSess); err != nil {
		m.messages = append(m.messages, message{role: "error", content: fmt.Sprintf("Failed to save session: %v", err), timestamp: time.Now()})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.session = newSess
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)
	m.messages = []message{{role: "system", content: fmt.Sprintf("Started new session: %s", newSess.ID[:8]), timestamp: time.Now()}}
	m.taskSummary = ""
	m.totalInputTokens = 0
	m.totalOutputTokens = 0
	m.queuedMessages = nil
	m.lastUserInputTime = time.Now()
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
