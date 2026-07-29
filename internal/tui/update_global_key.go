package tui

import (
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateGlobalKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.processing {
			if m.cancelPending {
				if m.cancelFunc != nil {
					m.cancelFunc()
				}
				if m.session != nil {
					m.saveSessionIfNotEmpty()
				}
				return m, tea.Quit, true
			}

			m.cancelPending = true
			if m.cancelFunc != nil {
				m.cancelFunc()
				logging.Info("Agent cancelled by user")
			}
			m.messages = append(m.messages, message{
				role:      "error",
				content:   "Cancelling... (press Ctrl+C again to force quit)",
				timestamp: time.Now(),
			})
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, nil, true
		}
		if m.session != nil {
			m.saveSessionIfNotEmpty()
		}
		return m, tea.Quit, true

	case tea.KeyEsc:
		if m.session != nil {
			m.saveSessionIfNotEmpty()
		}
		return m, tea.Quit, true

	case tea.KeyEnter:
		if msg.Alt {
			return m, nil, false
		}

		input := m.textarea.Value()
		if strings.TrimSpace(input) == "" {
			return m, nil, true
		}

		if strings.HasPrefix(input, "/") {
			raw := strings.TrimSpace(strings.TrimPrefix(input, "/"))
			parts := strings.Fields(raw)
			if len(parts) > 0 {
				cmdName := parts[0]
				args := parts[1:]
				if cmd := m.commandRegistry.FindCommand(cmdName); cmd != nil {
					m.textarea.Reset()
					model, cmdResult := m.executeCommand(cmd.Name, args)
					return model.(Model), cmdResult, true
				}
			}
			if cmd := m.commandRegistry.FindCommand(raw); cmd != nil {
				m.textarea.Reset()
				model, cmdResult := m.executeCommand(cmd.Name, nil)
				return model.(Model), cmdResult, true
			}
		}

		m.textarea.Reset()
		if m.processing {
			m.queuedMessages = append(m.queuedMessages, input)
			m.messages = append(m.messages, message{
				role:      "queued",
				content:   input,
				timestamp: time.Now(),
			})
			m.viewport.SetContent(m.renderMessages())
			m.viewport.GotoBottom()
			return m, nil, true
		}

		m = m.handleUserInput(input)
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		cmd, cancel := m.runAgent(input)
		m.cancelFunc = cancel
		m.cancelPending = false
		return m, cmd, true

	case tea.KeyRunes:
		if len(msg.Runes) > 0 && msg.Runes[0] == '/' && m.textarea.Value() == "" {
			m.showCommandMenu = true
			m.commandMenuIndex = 0
			m.filteredCommands = m.commandRegistry.GetCommands()
		}
		return m, nil, false

	default:
		return m, nil, false
	}
}
