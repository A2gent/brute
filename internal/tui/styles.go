package tui

import "github.com/charmbracelet/lipgloss"

var (
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

	toolBashStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379"))

	toolReadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#61AFEF"))

	toolWriteStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5C07B"))

	toolEditStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C678DD"))

	toolGlobStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#56B6C2"))

	toolGrepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75"))

	toolTaskStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D19A66"))

	diffAddStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#98C379"))

	diffRemoveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E06C75"))

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
					Foreground(lipgloss.Color("#9C27B0"))

	sentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00FF00")).
			Bold(true)

	receivedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00AAFF"))

	queuedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true)

	queuedContentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2a2a2a")).
				Foreground(lipgloss.Color("#888888")).
				Padding(0, 1)

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

	modelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	pathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)
