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
		if updated, cmd, handled := m.updateGlobalKey(msg); handled {
			return updated, cmd
		} else {
			m = updated
		}

	case tickMsg:
		var tickCmds []tea.Cmd
		m, tickCmds = m.updateTick()
		cmds = append(cmds, tickCmds...)

	case memoryUpdateMsg:
		m = m.updateMemory(msg)

	case serverPortMsg:
		m = m.updateServerPort(msg)

	case sessionSyncMsg:
		var asyncCmds []tea.Cmd
		m, asyncCmds = m.updateSessionSync(msg)
		cmds = append(cmds, asyncCmds...)

	case startInitialRunMsg:
		var runCmd tea.Cmd
		m, runCmd = m.startExistingSessionRun(msg.task)
		cmds = append(cmds, runCmd)

	case externalSessionEventMsg:
		m = m.updateExternalSessionEvent(msg.event)
		cmds = append(cmds, sessionEventCmd(m.sessionEvents))

	case agentResponseMsg:
		var asyncCmds []tea.Cmd
		m, asyncCmds = m.updateAgentResponse(msg)
		cmds = append(cmds, asyncCmds...)

	case agentStreamMsg:
		if msg.hasEvent {
			m = m.updateAgentEvent(msg.event)
			cmds = append(cmds, agentStreamCmd(msg.stream))
		}
		if msg.hasResponse {
			var asyncCmds []tea.Cmd
			m, asyncCmds = m.updateAgentResponse(msg.response)
			cmds = append(cmds, asyncCmds...)
		}

	case agentStreamClosedMsg:
		// The response message owns terminal state. A closed stream without a
		// response can happen after cancellation and does not need another update.

	case tokenUpdateMsg:
		m = m.updateTokens(msg)
	}

	m.updateViewportHeight()

	m.textarea, taCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, taCmd, vpCmd)

	return m, tea.Batch(cmds...)
}
