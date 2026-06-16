package tui

import tea "github.com/charmbracelet/bubbletea"

// Update stays intentionally small: it only dispatches by message type and
// keeps the original priority order between overlays/modes. The actual state
// transitions live in focused update_*.go files so behavior can stay identical
// while the main Bubble Tea entrypoint remains maintainable.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		taCmd tea.Cmd
		vpCmd tea.Cmd
		cmds  []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m = m.updateWindowSize(msg)

	case tea.KeyMsg:
		if updated, cmd, handled := m.updateCommandMenuKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateQuestionKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateLogsKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateSessionsKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateProviderMenuKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateModelsMenuKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateProjectsMenuKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateWorkflowMenuKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}
		if updated, cmd, handled := m.updateGlobalKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}

	case tickMsg:
		cmds = append(cmds, m.updateTick()...)

	case memoryUpdateMsg:
		m = m.updateMemory(msg)

	case serverPortMsg:
		m = m.updateServerPort(msg)

	case sessionSyncMsg:
		var asyncCmds []tea.Cmd
		m, asyncCmds = m.updateSessionSync(msg)
		cmds = append(cmds, asyncCmds...)

	case agentResponseMsg:
		var asyncCmds []tea.Cmd
		m, asyncCmds = m.updateAgentResponse(msg)
		cmds = append(cmds, asyncCmds...)

	case tokenUpdateMsg:
		m = m.updateTokens(msg)
	}

	m.updateViewportHeight()

	m.textarea, taCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, taCmd, vpCmd)

	return m, tea.Batch(cmds...)
}
