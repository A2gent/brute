package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// selfUpdateScriptURL is the same installer documented for first-time setup;
// it downloads the repo and rebuilds the binary into the install dir (needs Go).
const selfUpdateScriptURL = "https://raw.githubusercontent.com/A2gent/brute/main/install-from-github.sh"

// RestartRequested is set after a successful /update; main re-execs the new
// binary once the TUI has shut down.
var RestartRequested bool

type selfUpdateDoneMsg struct {
	output string
	err    error
}

func runSelfUpdate() tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("bash", "-c", "curl -fsSL "+selfUpdateScriptURL+" | bash").CombinedOutput()
		return selfUpdateDoneMsg{output: strings.TrimSpace(string(out)), err: err}
	}
}

func (m Model) startSelfUpdate() (tea.Model, tea.Cmd) {
	m.messages = append(m.messages, message{role: "system", content: "Updating brute from GitHub (this builds from source)...", timestamp: time.Now()})
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
	return m, runSelfUpdate()
}

func (m Model) handleSelfUpdateDone(msg selfUpdateDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.messages = append(m.messages, message{role: "error", content: fmt.Sprintf("Update failed: %v\n%s", msg.err, msg.output), timestamp: time.Now()})
		m.viewport.SetContent(m.renderMessages())
		m.viewport.GotoBottom()
		return m, nil
	}
	RestartRequested = true
	return m, tea.Quit
}
