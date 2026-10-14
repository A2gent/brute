package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// logoRows is a terminal take on the a2gent.net mark: a split square frame
// (blue left, white right), an "A" chevron and a node lattice inside.
// Every row has the same rune width so the mark stays aligned when centered.
var logoRows = []string{
	"┏━━━━━━━━━━╱╲━━━━━━━━━━┓",
	"┃        ╱╱  ╲╲        ┃",
	"┃      ╱╱  ●●  ╲╲      ┃",
	"┃    ╱╱ ●──┼┼──● ╲╲    ┃",
	"┃  ╱╱●  ●  ●●  ●  ●╲╲  ┃",
	"┃╱╱  ●  ●──┼┼──●  ●  ╲╲┃",
	"┃    ●  ●  ●●  ●  ●    ┃",
	"┗━━━━━━━●━━━━━━●━━━━━━━┛",
}

const (
	logoTickInterval = 120 * time.Millisecond
	logoWordmark     = "A 2   B R U T E"
	logoPulsePeriod  = 16
)

type logoTickMsg struct{}

func logoTickCmd() tea.Cmd {
	return tea.Tick(logoTickInterval, func(time.Time) tea.Msg { return logoTickMsg{} })
}

var (
	logoBlue  = lipgloss.NewStyle().Foreground(lipgloss.Color("#0A6BE8")).Bold(true)
	logoWhite = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5F7FA")).Bold(true)
	logoLine  = lipgloss.NewStyle().Foreground(lipgloss.Color("#1F4F8F"))
	logoNodes = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#4CC9FF")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#1E90FF")),
	}
	logoNodeDim = lipgloss.NewStyle().Foreground(lipgloss.Color("#2F6FB0"))
)

// renderLogo draws the mark for an animation frame. A pulse radiates from the
// center node outwards through the lattice, then a highlight sweeps the wordmark.
func renderLogo(frame int) string {
	var b strings.Builder
	for row, line := range logoRows {
		runes := []rune(line)
		mid := len(runes) / 2
		for col, r := range runes {
			switch r {
			case '●':
				b.WriteString(logoNodeStyle(frame, row, col, mid).Render(string(r)))
			case '─', '│', '┼':
				b.WriteString(logoLine.Render(string(r)))
			case '╱', '╲':
				b.WriteString(logoBlue.Render(string(r)))
			case ' ':
				b.WriteRune(r)
			default:
				if col < mid {
					b.WriteString(logoBlue.Render(string(r)))
				} else {
					b.WriteString(logoWhite.Render(string(r)))
				}
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString(renderWordmark(frame))
	return b.String()
}

func logoNodeStyle(frame, row, col, mid int) lipgloss.Style {
	dist := abs(row-4) + abs(col-mid)/3
	phase := ((frame-dist)%logoPulsePeriod + logoPulsePeriod) % logoPulsePeriod
	if phase < len(logoNodes) {
		return logoNodes[phase]
	}
	return logoNodeDim
}

func renderWordmark(frame int) string {
	letters := []rune(logoWordmark)
	sweep := (frame / 2) % (len(letters) + 8)
	var b strings.Builder
	for i, r := range letters {
		style := logoBlue
		if d := abs(i - sweep); d == 0 {
			style = logoNodes[0]
		} else if d == 1 {
			style = logoNodes[1]
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
