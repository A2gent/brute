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
	if m.devReloadStatus != "" {
		devText := contextWarningStyle.Render(m.devReloadStatus)
		if portText != "" {
			portText += "  " + devText
		} else {
			portText = devText
		}
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
	if m.hasActiveRunIndicator() {
		sections = append(sections, m.renderRunStatus(m.width))
	}
	if m.hasDiscussion() || m.showQuestionPrompt {
		sections = append(sections, questionPrompt+commandMenu+inputView+"\n"+agentLine)
	}
	sections = append(sections, bottomBar)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m Model) renderRunStatus(width int) string {
	if width < 1 {
		width = 1
	}
	frame := ""
	if len(m.loadingFrames) > 0 {
		frame = m.loadingFrames[m.loadingIndex%len(m.loadingFrames)]
	}
	status := strings.TrimSpace(m.activeRunStatus)
	if status == "" {
		if m.processing {
			status = "Working"
		} else {
			status = "Session running"
		}
	}
	detail := strings.TrimSpace(m.activeRunDetail)
	text := strings.TrimSpace(frame + " " + status)
	if detail != "" {
		text += " - " + detail
	}
	if queued := len(m.queuedMessages); queued > 0 {
		text += fmt.Sprintf(" (%d queued)", queued)
	}
	text = truncateLine(text, width)
	return lipgloss.NewStyle().
		Background(lipgloss.Color("#111111")).
		Foreground(lipgloss.Color("#A0A0A0")).
		Width(width).
		Render(text)
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
	label := strings.TrimSpace(m.agentConfig.Name)
	if label == "" {
		label = "agent"
	}
	text := fmt.Sprintf("  Agent: %s", label)
	if m.selectedProjectName != "" {
		text += fmt.Sprintf("  •  Project: %s", m.selectedProjectName)
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#1a1a1a")).Foreground(lipgloss.Color("#888888")).Width(width).Render(truncateLine(text, width))
}

func (m Model) renderEmptySessionView() string {
	inputWidth := m.width
	if inputWidth > 90 {
		inputWidth = 90
	}
	if inputWidth < 20 {
		inputWidth = m.width
	}
	if inputWidth < 1 {
		inputWidth = 1
	}

	centerStyle := lipgloss.NewStyle().
		Width(inputWidth).
		Align(lipgloss.Center)

	// Hide the cube in short terminals so the input stays on screen.
	var logoCells [][]string
	if m.viewport.Height >= logoCanvasHeight+8 {
		logoCells = renderLogoCells(m.logoFrame)
	}
	var parts []string
	if logoCells != nil {
		parts = append(parts, centerStyle.Render(renderLogo(m.logoFrame)))
	}
	parts = append(parts,
		centerStyle.Render(m.renderInputView(inputWidth)),
		centerStyle.Render(m.renderActiveAgentLine(inputWidth)),
	)
	if m.showCommandMenu {
		parts = append(parts, centerStyle.Render(m.renderCommandMenu()))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	contentRows := strings.Split(content, "\n")
	for len(contentRows) > 0 && contentRows[len(contentRows)-1] == "" {
		contentRows = contentRows[:len(contentRows)-1]
	}
	height := m.viewport.Height
	if height < 1 {
		height = 1
	}
	width := m.width
	if width < 1 {
		width = 1
	}
	topPad := (height - len(contentRows)) / 2
	if topPad < 0 {
		topPad = 0
	}

	leftPad := (width - inputWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	contentWidth := inputWidth
	if contentWidth+leftPad > width {
		contentWidth = width - leftPad
	}
	rightPad := width - leftPad - contentWidth
	background := renderMatrixBackground(width, height, m.logoFrame)
	logoLeft := leftPad + max(0, (inputWidth-logoCanvasWidth)/2)

	var out strings.Builder
	for row := 0; row < height; row++ {
		if row > 0 {
			out.WriteByte('\n')
		}
		contentRow := row - topPad
		if contentRow < 0 || contentRow >= len(contentRows) {
			for _, cell := range background[row] {
				out.WriteString(cell)
			}
			continue
		}
		if contentRow < len(logoCells) && logoLeft+logoCanvasWidth <= width {
			// Composite the cube over the rain so empty canvas cells keep the
			// same background columns as the rest of the screen.
			for col, cell := range background[row] {
				if col >= logoLeft && col < logoLeft+logoCanvasWidth {
					if logoCell := logoCells[contentRow][col-logoLeft]; logoCell != "" {
						cell = logoCell
					}
				}
				out.WriteString(cell)
			}
			continue
		}
		for _, cell := range background[row][:leftPad] {
			out.WriteString(cell)
		}
		out.WriteString(contentRows[contentRow])
		for _, cell := range background[row][leftPad+contentWidth : leftPad+contentWidth+rightPad] {
			out.WriteString(cell)
		}
	}
	return out.String()
}
