package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the TUI
func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// Top bar with task summary, stats, session, and time
	topBar := m.renderTopBar()

	// Messages viewport - show ASCII art and centered input if no messages
	var messagesView string
	if len(m.messages) == 0 {
		messagesView = m.renderEmptySessionView()
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

	// Check if we should show workflow/agent menu overlay
	if m.showAgentsMenu {
		agentsView := m.renderAgentsMenu()
		return lipgloss.JoinVertical(
			lipgloss.Left,
			topBar,
			agentsView,
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

	inputView := m.renderInputView(m.width)
	agentLine := m.renderActiveAgentLine(m.width)

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
	portText := ""
	if m.serverPort > 0 {
		portText = statsStyle.Render(fmt.Sprintf("API :%d", m.serverPort))
	}
	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Render(helpStr)

	// Calculate space between path and help
	bottomUsedWidth := lipgloss.Width(pathText) + lipgloss.Width(helpText)
	if portText != "" {
		bottomUsedWidth += lipgloss.Width(portText) + 2
	}
	bottomSpace := m.width - bottomUsedWidth
	if bottomSpace < 1 {
		bottomSpace = 1
	}

	bottomLeft := pathText
	if portText != "" {
		bottomLeft = lipgloss.JoinHorizontal(lipgloss.Left, pathText, "  ", portText)
	}

	bottomBar := lipgloss.JoinHorizontal(lipgloss.Left, bottomLeft, strings.Repeat(" ", bottomSpace), helpText)

	sections := []string{topBar, messagesView}
	if m.hasDiscussion() || m.showQuestionPrompt {
		sections = append(sections, questionPrompt+commandMenu+inputView+"\n"+agentLine)
	}
	sections = append(sections, bottomBar)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m Model) renderInputView(width int) string {
	if width < 1 {
		width = 1
	}
	if m.showQuestionPrompt && m.questionOptionIndex >= 0 {
		disabledStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1a1a")).
			Foreground(lipgloss.Color("#666666")).
			Width(width)

		selectedOption := ""
		if m.questionOptionIndex < len(m.pendingQuestion.Options) {
			selectedOption = m.pendingQuestion.Options[m.questionOptionIndex].Label
		}
		return disabledStyle.Render("│ Selected: " + selectedOption + " (press Enter to submit, ↓ for custom)")
	}

	m.textarea.SetWidth(width)
	textareaContent := m.textarea.View()
	lines := strings.Split(textareaContent, "\n")
	paddedLines := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		line := "│ "
		if i < len(lines) {
			line = lines[i]
		}
		paddedLine := lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1a1a")).
			Width(width).
			Render(line)
		paddedLines = append(paddedLines, paddedLine)
	}
	return strings.Join(paddedLines, "\n")
}

func (m Model) renderActiveAgentLine(width int) string {
	if width < 1 {
		width = 1
	}
	workflow := m.selectedWorkflow
	if sessionWorkflow := workflowFromSessionMetadata(m.session); sessionWorkflow.ID != "" {
		workflow = sessionWorkflow
	}
	if workflow.ID == "" {
		workflow = builtinUserMainWorkflow()
	}

	label := workflowLaunchLabel(workflow)
	if label == "" {
		label = m.agentConfig.Name
	}
	if label == "" {
		label = "agent"
	}
	text := fmt.Sprintf("  Agent: %s", label)
	if workflow.Name != "" && workflow.Name != label {
		text += fmt.Sprintf("  •  Workflow: %s", workflow.Name)
	}
	if m.selectedProjectName != "" {
		text += fmt.Sprintf("  •  Project: %s", m.selectedProjectName)
	}
	text = truncateLine(text, width)
	return lipgloss.NewStyle().
		Background(lipgloss.Color("#1a1a1a")).
		Foreground(lipgloss.Color("#888888")).
		Width(width).
		Render(text)
}

func (m Model) renderEmptySessionView() string {
	inputWidth := m.width
	if inputWidth > 90 {
		inputWidth = 90
	}
	if inputWidth < 20 {
		inputWidth = m.width
	}

	artStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7D56F4")).
		Bold(true).
		Width(m.width).
		Align(lipgloss.Center)
	inputStyle := lipgloss.NewStyle().
		Width(m.width).
		Align(lipgloss.Center)

	parts := []string{
		artStyle.Render(asciiArt),
		inputStyle.Render(m.renderInputView(inputWidth)),
		inputStyle.Render(m.renderActiveAgentLine(inputWidth)),
	}
	if m.showCommandMenu {
		parts = append(parts, inputStyle.Render(m.renderCommandMenu()))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	contentHeight := lipgloss.Height(content)
	topPad := (m.viewport.Height - contentHeight) / 2
	if topPad < 0 {
		topPad = 0
	}

	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.viewport.Height).
		Render(strings.Repeat("\n", topPad) + content)
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
