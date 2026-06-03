package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4"))

	taskStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	statsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0"))

	tokenStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00"))

	contextWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFF00"))

	compactionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD166"))

	contextDangerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF0000"))

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00AAFF"))

	userContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1a1a3e")).
				Padding(0, 1)

	assistantContentStyle = lipgloss.NewStyle()

	assistantStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00FF00"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500"))

	toolResultStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0"))

	// Tool-specific styles
	toolBashStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379")) // Green for shell commands

	toolReadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#61AFEF")) // Blue for reading

	toolWriteStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5C07B")) // Yellow for writing

	toolEditStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C678DD")) // Purple for editing

	toolGlobStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#56B6C2")) // Cyan for file search

	toolGrepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75")) // Red for content search

	toolTaskStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D19A66")) // Orange for sub-agents

	// Diff styles
	diffAddStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379")) // Green for additions

	diffRemoveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75")) // Red for deletions

	diffContextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ABB2BF")) // Gray for context

	diffHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#61AFEF")).
			Bold(true) // Blue bold for file headers

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000"))

	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	statusRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00FF00"))

	statusPausedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFF00"))

	statusCompletedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#00AAFF"))

	statusFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF0000"))

	statusInputRequiredStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#9C27B0")) // Purple

	loadingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500"))

	sentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00")).
			Bold(true)

	receivedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00AAFF"))

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#444444"))

	queuedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true)

	queuedContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2a2a2a")).
				Foreground(lipgloss.Color("#888888")).
				Padding(0, 1)

	// Command menu styles
	commandMenuStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7D56F4")).
				Padding(0, 1)

	commandItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF"))

	commandSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#7D56F4")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)

	commandDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#888888"))

	// Textarea border style
	textareaBorderStyle = lipgloss.NewStyle().
				BorderLeft(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("#00AAFF")). // Light blue
				PaddingLeft(1)

	// Model indicator style
	modelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	// Path style
	pathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)
