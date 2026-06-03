// browser_chrome_handlers.go keeps browser_chrome HTTP management endpoints focused after the split.
package http

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// handleBrowserChromeProfileStatus returns the status of the browser_chrome agent profile
func (s *Server) handleBrowserChromeProfileStatus(w http.ResponseWriter, r *http.Request) {
	chromeAgentProfilePath := integrationtools.AgentChromeProfileDir()
	info, err := os.Stat(chromeAgentProfilePath)
	exists := err == nil && info.IsDir()

	var lastUsed string
	if exists {
		lastUsed = info.ModTime().Format(time.RFC3339)
	}

	response := map[string]interface{}{
		"exists": exists,
		"path":   chromeAgentProfilePath,
	}
	if exists {
		response["lastUsed"] = lastUsed
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, `{"error": "failed to encode response"}`, http.StatusInternalServerError)
	}
}

// handleBrowserChromeCreateProfile ensures the agent profile layout exists.
func (s *Server) handleBrowserChromeCreateProfile(w http.ResponseWriter, r *http.Request) {
	agentProfile := integrationtools.AgentChromeProfileDir()
	debugUserDataDir := integrationtools.AgentChromeDebugUserDataDir()
	profileExists := false
	if info, err := os.Stat(agentProfile); err == nil && info.IsDir() {
		profileExists = true
	}

	if err := integrationtools.PrepareAgentChromeLaunchLayout(
		debugUserDataDir,
		agentProfile,
		integrationtools.AgentChromeProfileDirectoryName,
	); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to prepare browser chrome profile layout: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"message":     "Agent profile is ready.",
		"path":        agentProfile,
		"filesCopied": 0,
		"failedFiles": []string{},
		"created":     !profileExists,
	})
}

// handleBrowserChromeLaunch launches Chrome with a dedicated user-data-dir for the agent.
// This directory is SEPARATE from the default Chrome directory, which:
// 1. Allows remote debugging (Chrome blocks it on default directory)
// 2. Preserves encrypted credentials (logins persist between sessions)
// 3. Both UI button and agent tool use the SAME directory
func (s *Server) handleBrowserChromeLaunch(w http.ResponseWriter, r *http.Request) {
	agentProfileDir := integrationtools.AgentChromeProfileDir()
	debugUserDataDir := integrationtools.AgentChromeDebugUserDataDir()

	logging.Info("Using Chrome agent profile directory: %s", agentProfileDir)
	logging.Info("Using Chrome debug user-data-dir: %s", debugUserDataDir)

	profileExists := false
	if _, err := os.Stat(agentProfileDir); err == nil {
		profileExists = true
		logging.Info("Chrome agent profile directory already exists")
	}
	if err := integrationtools.PrepareAgentChromeLaunchLayout(
		debugUserDataDir,
		agentProfileDir,
		integrationtools.AgentChromeProfileDirectoryName,
	); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to prepare browser chrome profile layout: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	debugPort := strings.TrimSpace(os.Getenv("CHROME_DEBUG_PORT"))
	if debugPort == "" {
		debugPort = "9223"
	}

	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	args := []string{
		"--user-data-dir=" + debugUserDataDir,
		"--profile-directory=" + integrationtools.AgentChromeProfileDirectoryName,
		"--remote-debugging-port=" + debugPort,
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
	}

	logging.Info("Launching Chrome with user-data-dir: %s", debugUserDataDir)

	cmd := exec.Command(chromePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "failed to launch Chrome: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	logging.Info("Chrome launched with ChromeAgent profile, PID: %d", cmd.Process.Pid)

	message := "Chrome opened with agent profile. Log in to websites here - the agent will use these sessions."
	if !profileExists {
		message = "Chrome opened with NEW agent profile. Please log in to websites you want the agent to access and keep using this profile for agent automation."
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"message":       message,
		"pid":           cmd.Process.Pid,
		"profile":       agentProfileDir,
		"profileExists": profileExists,
	})
}

// isChromeRunning checks if Chrome is currently running
func isChromeRunning() bool {
	cmd := exec.Command("pgrep", "-x", "Google Chrome")
	err := cmd.Run()
	return err == nil
}
