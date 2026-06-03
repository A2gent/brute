package tui

import (
	"fmt"
	"github.com/A2gent/brute/internal/logging"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

func (m Model) renderLogsView() string {
	lines := m.logLines
	if len(lines) == 0 {
		lines = []string{"No logs captured in this process yet."}
	}

	visible := m.logsVisibleLines()
	start := m.logTop
	if start < 0 {
		start = 0
	}
	if start > len(lines)-1 {
		start = len(lines) - 1
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}

	width := m.width - 6
	if width < 20 {
		width = 20
	}

	var out []string
	header := fmt.Sprintf(
		"Logs (live)  file: %s",
		logging.GetLogPath(),
	)
	out = append(out, lipgloss.NewStyle().Bold(true).Render(truncateLine(header, width)))
	out = append(out, "")

	for _, line := range lines[start:end] {
		out = append(out, truncateLine(line, width))
	}

	help := "↑/↓: scroll  pgup/pgdn: page  home/end: start/end  esc: close"
	follow := "paused"
	if m.logFollow {
		follow = "following"
	}
	position := fmt.Sprintf("lines %d-%d/%d (%s)", start+1, end, len(lines), follow)
	out = append(out, "")
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render(truncateLine(position, width)))
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Render(truncateLine(help, width)))

	content := strings.Join(out, "\n")
	return commandMenuStyle.Width(m.width - 4).Render(content)
}

// calculateQuestionPromptHeight calculates how many lines the question prompt will take
