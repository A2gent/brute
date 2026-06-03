package tui

import tea "github.com/charmbracelet/bubbletea"

func (m Model) updateLogsKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showLogsView {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.showLogsView = false
		m.viewport.SetContent(m.renderMessages())
		return m, nil, true
	case tea.KeyUp:
		if m.logTop > 0 {
			m.logTop--
		}
		m.logFollow = false
		return m, nil, true
	case tea.KeyDown:
		maxTop := m.maxLogsTop()
		if m.logTop < maxTop {
			m.logTop++
		}
		if m.logTop >= maxTop {
			m.logFollow = true
		}
		return m, nil, true
	case tea.KeyPgUp:
		m.logTop -= m.logsPageStep()
		if m.logTop < 0 {
			m.logTop = 0
		}
		m.logFollow = false
		return m, nil, true
	case tea.KeyPgDown:
		m.logTop += m.logsPageStep()
		maxTop := m.maxLogsTop()
		if m.logTop > maxTop {
			m.logTop = maxTop
		}
		if m.logTop >= maxTop {
			m.logFollow = true
		}
		return m, nil, true
	case tea.KeyHome:
		m.logTop = 0
		m.logFollow = false
		return m, nil, true
	case tea.KeyEnd:
		m.logTop = m.maxLogsTop()
		m.logFollow = true
		return m, nil, true
	default:
		return m, nil, true
	}
}

func (m Model) updateSessionsKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showSessionsList {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.showSessionsList = false
		m.sessionsListOffset = 0
		m.viewport.SetContent(m.renderMessages())
		return m, nil, true
	case tea.KeyUp:
		if m.sessionsListIndex > 0 {
			m.sessionsListIndex--
		}
		return m, nil, true
	case tea.KeyDown:
		if m.sessionsListIndex < len(m.availableSessions)-1 {
			m.sessionsListIndex++
		}
		return m, nil, true
	case tea.KeyPgUp:
		pageSize := m.height - 8
		if pageSize < 3 {
			pageSize = 3
		}
		m.sessionsListIndex -= pageSize
		if m.sessionsListIndex < 0 {
			m.sessionsListIndex = 0
		}
		return m, nil, true
	case tea.KeyPgDown:
		pageSize := m.height - 8
		if pageSize < 3 {
			pageSize = 3
		}
		m.sessionsListIndex += pageSize
		if m.sessionsListIndex >= len(m.availableSessions) {
			m.sessionsListIndex = len(m.availableSessions) - 1
		}
		return m, nil, true
	case tea.KeyHome:
		m.sessionsListIndex = 0
		return m, nil, true
	case tea.KeyEnd:
		m.sessionsListIndex = len(m.availableSessions) - 1
		return m, nil, true
	case tea.KeyEnter:
		if len(m.availableSessions) > 0 {
			selectedSession := m.availableSessions[m.sessionsListIndex]
			m = m.switchToSession(selectedSession.ID)
			m.showSessionsList = false
			m.sessionsListOffset = 0
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
		}
		return m, nil, true
	default:
		return m, nil, true
	}
}

func (m Model) updateProjectsMenuKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showProjectsMenu {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.showProjectsMenu = false
		m.viewport.SetContent(m.renderMessages())
		return m, nil, true
	case tea.KeyUp:
		if m.projectsMenuIndex > 0 {
			m.projectsMenuIndex--
		}
		return m, nil, true
	case tea.KeyDown:
		if m.projectsMenuIndex < len(m.availableProjects) {
			m.projectsMenuIndex++
		}
		return m, nil, true
	case tea.KeyEnter:
		model, cmd := m.selectProject()
		return model.(Model), cmd, true
	default:
		return m, nil, true
	}
}
