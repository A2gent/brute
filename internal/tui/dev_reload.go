package tui

import (
	"os"
	"strings"
)

// devReloadStatusPath is set by cmd/devreload (`just dev`). Its one-line status
// tells the user this TUI runs under the hot reloader, which restarts it after
// code changes once no sessions are running.
var devReloadStatusPath = os.Getenv("AAGENT_DEV_RELOAD_STATUS")

func readDevReloadStatus() string {
	if devReloadStatusPath == "" {
		return ""
	}
	data, err := os.ReadFile(devReloadStatusPath)
	if err != nil {
		return "DEV"
	}
	return strings.TrimSpace(string(data))
}
