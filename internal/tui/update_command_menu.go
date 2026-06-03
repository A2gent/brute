package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) updateWindowSize(msg tea.WindowSizeMsg) Model {
	m.width = msg.Width
	m.height = msg.Height

	viewportHeight := m.calculateViewportHeight()
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
	return m
}

func (m Model) updateCommandMenuKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	if !m.showCommandMenu {
		return m, nil, false
	}

	var taCmd tea.Cmd

	switch msg.Type {
	case tea.KeyEsc:
		m.showCommandMenu = false
		m.textarea.Reset()
		return m, nil, true
	case tea.KeyUp:
		if m.commandMenuIndex > 0 {
			m.commandMenuIndex--
		}
		return m, nil, true
	case tea.KeyDown:
		if m.commandMenuIndex < len(m.filteredCommands)-1 {
			m.commandMenuIndex++
		}
		return m, nil, true
	case tea.KeyEnter:
		input := strings.TrimSpace(m.textarea.Value())
		if strings.HasPrefix(input, "/") {
			raw := strings.TrimSpace(strings.TrimPrefix(input, "/"))
			parts := strings.Fields(raw)
			if len(parts) > 1 {
				if cmd := m.commandRegistry.FindCommand(parts[0]); cmd != nil {
					m.showCommandMenu = false
					m.textarea.Reset()
					model, cmdResult := m.executeCommand(cmd.Name, parts[1:])
					return model.(Model), cmdResult, true
				}
			}
		}
		if len(m.filteredCommands) > 0 {
			selectedCmd := m.filteredCommands[m.commandMenuIndex]
			m.showCommandMenu = false
			m.textarea.Reset()
			model, cmdResult := m.executeCommand(selectedCmd.Name, nil)
			return model.(Model), cmdResult, true
		}
		return m, nil, true
	case tea.KeyTab:
		if len(m.filteredCommands) > 0 {
			selectedCmd := m.filteredCommands[m.commandMenuIndex]
			m.showCommandMenu = false
			m.textarea.Reset()
			model, cmdResult := m.executeCommand(selectedCmd.Name, nil)
			return model.(Model), cmdResult, true
		}
		return m, nil, true
	case tea.KeyBackspace:
		input := m.textarea.Value()
		if input == "/" || input == "" {
			m.showCommandMenu = false
			m.textarea.Reset()
			return m, nil, true
		}

		m.textarea, taCmd = m.textarea.Update(msg)
		m = m.updateCommandMenuFilter(m.textarea.Value())
		return m, taCmd, true
	default:
		m.textarea, taCmd = m.textarea.Update(msg)
		m = m.updateCommandMenuFilter(m.textarea.Value())
		return m, taCmd, true
	}
}

func (m Model) updateCommandMenuFilter(input string) Model {
	if strings.HasPrefix(input, "/") {
		raw := strings.TrimPrefix(input, "/")
		if strings.ContainsAny(raw, " \t") {
			m.showCommandMenu = false
		} else {
			m.filteredCommands = m.commandRegistry.FilterCommands(raw)
			m.commandMenuIndex = 0
		}
	} else {
		m.showCommandMenu = false
	}
	return m
}
