package tui

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

func (m Model) calculateQuestionPromptHeight() int {
	if !m.showQuestionPrompt || m.pendingQuestion == nil {
		return 0
	}

	height := 0
	height += 1                              // Top separator
	height += 1                              // Header line
	height += len(m.pendingQuestion.Options) // Options (one per line)
	if m.pendingQuestion.Custom {
		height += 1 // Empty line before custom hint
		height += 1 // Custom hint line
	}
	height += 1 // Bottom separator

	return height
}

// renderQuestionPrompt renders the question prompt overlay
func (m Model) renderQuestionPrompt() string {
	if !m.showQuestionPrompt || m.pendingQuestion == nil {
		return ""
	}

	var sb strings.Builder

	// Header (compact, one line)
	questionHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FF9800"))

	sb.WriteString(questionHeaderStyle.Render("❓ " + m.pendingQuestion.Header + ": " + m.pendingQuestion.Question))
	sb.WriteString("\n")

	// Options (compact, one line each)
	optionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#AAAAAA"))

	selectedOptionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#2196F3")).
		Bold(true)

	for i, opt := range m.pendingQuestion.Options {
		var icon string
		var style lipgloss.Style

		if m.pendingQuestion.Multiple {
			icon = "☐"
			if i == m.questionOptionIndex {
				icon = "☑"
			}
		} else {
			icon = "○"
			if i == m.questionOptionIndex {
				icon = "◉"
			}
		}

		if i == m.questionOptionIndex {
			style = selectedOptionStyle
		} else {
			style = optionStyle
		}

		text := fmt.Sprintf("  %s %s", icon, opt.Label)
		sb.WriteString(style.Render(text))
		sb.WriteString("\n")
	}

	// Custom answer hint if enabled
	if m.pendingQuestion.Custom {
		sb.WriteString("\n")

		// Check if custom field is selected
		isCustomSelected := m.questionOptionIndex == -1

		var hintStyle lipgloss.Style
		var hintText string
		if isCustomSelected {
			hintStyle = selectedOptionStyle
			hintText = "  💡 Custom answer (type below) ▼"
		} else {
			hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
			hintText = "  💡 Custom answer (press ↓ to select)"
		}
		sb.WriteString(hintStyle.Render(hintText))
	}

	// Simple separator line instead of border (more compact)
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF9800")).
		Render(strings.Repeat("─", m.width))

	return separator + "\n" + sb.String() + separator
}

// renderCommandMenu renders the command menu popup
