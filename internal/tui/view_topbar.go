package tui

import (
	"fmt"
	"github.com/A2gent/brute/internal/session"
	"github.com/charmbracelet/lipgloss"
	"strconv"
	"strings"
	"time"
)

func (m Model) renderTopBar() string {
	// Use session title if available, otherwise task summary or default
	summary := m.session.Title
	if summary == "" {
		summary = m.taskSummary
	}
	if summary == "" {
		summary = "New Session"
	}
	maxSummaryLen := m.width / 3
	summary = truncateLine(summary, maxSummaryLen)

	// Show project name instead of session ID in the header
	var contextInfo string
	if m.selectedProjectName != "" {
		contextInfo = m.selectedProjectName
	} else if m.session.ProjectID != nil {
		// Try to get project name from session's project
		if project, err := m.sessionManager.GetProject(*m.session.ProjectID); err == nil {
			contextInfo = project.Name
			// Cache it for future renders
			m.selectedProjectID = m.session.ProjectID
			m.selectedProjectName = project.Name
		}
	}

	var taskText string
	if contextInfo != "" {
		taskText = statsStyle.Render(contextInfo+" / ") + taskStyle.Render(summary)
	} else {
		taskText = taskStyle.Render(summary)
	}

	// Status indicator
	var statusIcon string
	switch m.session.Status {
	case session.StatusRunning:
		statusIcon = statusRunningStyle.Render("●")
	case session.StatusPaused:
		statusIcon = statusPausedStyle.Render("⏸")
	case session.StatusWaitingExternal:
		statusIcon = statusPausedStyle.Render("…")
	case session.StatusCompleted:
		statusIcon = statusCompletedStyle.Render("✓")
	case session.StatusFailed:
		statusIcon = statusFailedStyle.Render("✗")
	case session.StatusInputRequired:
		statusIcon = statusInputRequiredStyle.Render("?")
	}

	// Token stats
	currentContextTokens := m.currentContextTokenCount()
	contextPercent := 0.0
	if m.contextWindow > 0 {
		contextPercent = float64(currentContextTokens) / float64(m.contextWindow) * 100
	}

	var percentStyle lipgloss.Style
	switch {
	case contextPercent >= 90:
		percentStyle = contextDangerStyle
	case contextPercent >= 70:
		percentStyle = contextWarningStyle
	default:
		percentStyle = tokenStyle
	}

	tokenStats := fmt.Sprintf("%d↓ %d↑",
		m.totalInputTokens, m.totalOutputTokens)
	percentText := fmt.Sprintf("%.1f%%", contextPercent)

	// Memory usage
	memoryText := fmt.Sprintf("%.1fMB", m.memoryMB)

	// Timer showing time since last user input
	elapsed := time.Since(m.lastUserInputTime)
	timer := m.formatDuration(elapsed)

	// Build right side: tokens | percent | memory | time | status
	rightSide := statsStyle.Render(fmt.Sprintf("%s │ %s │ %s │ ⏱ %s ",
		tokenStyle.Render(tokenStats),
		percentStyle.Render(percentText),
		statsStyle.Render(memoryText),
		timer,
	)) + statusIcon

	// Model name in the center
	modelName := m.agentConfig.Model
	if modelName == "" {
		modelName = "default"
	}
	modelText := modelStyle.Render("⚡ " + modelName)

	// Calculate spacing to center the model
	leftWidth := lipgloss.Width(taskText)
	rightWidth := lipgloss.Width(rightSide)
	centerWidth := lipgloss.Width(modelText)

	totalUsed := leftWidth + centerWidth + rightWidth
	if totalUsed >= m.width {
		// Not enough space for centering, just show left and right
		space := m.width - leftWidth - rightWidth
		if space < 1 {
			space = 1
		}
		return lipgloss.JoinHorizontal(
			lipgloss.Left,
			taskText,
			strings.Repeat(" ", space),
			rightSide,
		)
	}

	// Center the model text
	remainingSpace := m.width - totalUsed
	leftSpace := (remainingSpace / 2) + 1
	rightSpace := remainingSpace - leftSpace + 2

	if leftSpace < 1 {
		leftSpace = 1
	}
	if rightSpace < 1 {
		rightSpace = 1
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		taskText,
		strings.Repeat(" ", leftSpace),
		modelText,
		strings.Repeat(" ", rightSpace),
		rightSide,
	)
}

// wrapText wraps text to fit within the given width
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}

	var result strings.Builder
	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}

		// Wrap each line
		for len(line) > width {
			// Find a good break point
			breakPoint := width
			for breakPoint > 0 && line[breakPoint] != ' ' {
				breakPoint--
			}
			if breakPoint == 0 {
				breakPoint = width // No space found, force break
			}

			result.WriteString(line[:breakPoint])
			result.WriteString("\n")
			line = strings.TrimLeft(line[breakPoint:], " ")
		}
		result.WriteString(line)
	}

	return result.String()
}

// formatDuration formats a duration in a human-readable way
func (m Model) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	} else {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func sessionMetadataFloat(sess *session.Session, key string) float64 {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	raw, ok := sess.Metadata[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

func (m *Model) applySessionTokenMetadata(sess *session.Session) {
	if sess == nil {
		return
	}
	totalIn := int(sessionMetadataFloat(sess, "total_input_tokens"))
	totalOut := int(sessionMetadataFloat(sess, "total_output_tokens"))
	if totalIn > 0 || totalOut > 0 {
		m.totalInputTokens = totalIn
		m.totalOutputTokens = totalOut
	}
}

func (m Model) currentContextTokenCount() int {
	current := int(sessionMetadataFloat(m.session, "current_context_tokens"))
	if current > 0 {
		return current
	}
	return m.totalInputTokens + m.totalOutputTokens
}

// renderMessages renders all messages as a string
