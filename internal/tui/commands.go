package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// executeCommand executes a slash command and returns the updated model
func (m Model) executeCommand(cmdName string, args []string) (tea.Model, tea.Cmd) {
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
	case "a2a":
		return m.handleA2ACommand(args)
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
