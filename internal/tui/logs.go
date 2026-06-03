package tui

import (
	"github.com/A2gent/brute/internal/logging"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"strings"
)

func (m Model) showLogs() (tea.Model, tea.Cmd) {
	m.showLogsView = true
	m.logFollow = true
	m.refreshLogsView()
	return m, nil
}

func (m *Model) refreshLogsView() {
	lines := logging.RecentLines(600)
	if len(lines) == 0 {
		filePath := strings.TrimSpace(logging.GetLogPath())
		if filePath != "" {
			if tailed := tailLogFile(filePath, 600); len(tailed) > 0 {
				lines = tailed
			}
		}
	}
	if len(lines) == 0 {
		lines = []string{"No logs available yet. Start activity, then reopen /logs."}
	}
	m.logLines = lines
	maxTop := m.maxLogsTop()
	if m.logFollow {
		m.logTop = maxTop
	} else if m.logTop > maxTop {
		m.logTop = maxTop
	}
	if m.logTop < 0 {
		m.logTop = 0
	}
}

func tailLogFile(path string, maxLines int) []string {
	if strings.TrimSpace(path) == "" || maxLines <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

func (m Model) logsVisibleLines() int {
	visible := m.height - 10
	if visible < 6 {
		visible = 6
	}
	return visible
}

func (m Model) logsPageStep() int {
	step := m.logsVisibleLines() - 2
	if step < 1 {
		step = 1
	}
	return step
}

func (m Model) maxLogsTop() int {
	visible := m.logsVisibleLines()
	if len(m.logLines) <= visible {
		return 0
	}
	return len(m.logLines) - visible
}

func truncateLine(line string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= maxLen {
		return line
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
