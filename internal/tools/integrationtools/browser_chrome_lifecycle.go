package integrationtools

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// isChromeRunning reports whether any Google Chrome process is currently running.
// Integration tests use this to decide whether they can safely launch a throwaway instance.
func isChromeRunning() bool {
	cmd := exec.Command("pgrep", "-x", "Google Chrome")
	err := cmd.Run()
	return err == nil
}

func (t *BrowserChromeTool) ensureBrowser() error {
	// Check if existing browser connection is still valid
	if t.browser != nil {
		// Try to verify the connection is still alive
		_, err := t.browser.Version()
		if err == nil {
			return nil
		}
		// Connection is dead, reset it
		logging.Info("Browser connection is stale, reconnecting...")
		t.browser = nil
	}

	logging.Info("Connecting to Chrome on port %s...", t.debugPort)

	// Try to connect to existing Chrome first
	wsURL := fmt.Sprintf("ws://localhost:%s", t.debugPort)
	browser := rod.New().ControlURL(wsURL)
	if err := browser.Connect(); err == nil {
		logging.Info("Connected to existing Chrome on port %s", t.debugPort)
		t.browser = browser
		return nil
	}

	// Try with resolved URL
	resolvedURL, resolveErr := launcher.ResolveURL(":" + t.debugPort)
	if resolveErr == nil {
		browser = rod.New().ControlURL(resolvedURL)
		if err := browser.Connect(); err == nil {
			logging.Info("Connected to Chrome via resolved URL: %s", resolvedURL)
			t.browser = browser
			return nil
		}
	}

	logging.Info("No debuggable Chrome instance found on port %s, launching agent Chrome...", t.debugPort)

	// Prepare launch layout:
	// - persistent profile in default Chrome directory (switchable in regular Chrome)
	// - separate debug user-data-dir to allow remote debugging.
	if err := PrepareAgentChromeLaunchLayout(t.userDataDir, t.profileDir, t.profileDirectory); err != nil {
		return err
	}

	// Launch Chrome with dedicated debug user-data-dir and explicit profile directory.
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	logging.Info("Launching Chrome with user-data-dir: %s", t.userDataDir)
	logging.Info("Using Chrome profile directory: %s (path: %s)", t.profileDirectory, t.profileDir)
	logging.Info("Headless mode: %v", t.headless)

	args := []string{
		"--user-data-dir=" + t.userDataDir,
		"--profile-directory=" + t.profileDirectory,
		"--remote-debugging-port=" + t.debugPort,
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
	}

	if t.headless {
		args = append(args, "--headless")
		logging.Info("Running in headless mode")
	}

	cmd := exec.Command(chromePath, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Chrome: %w", err)
	}

	logging.Info("Chrome launched, PID: %d", cmd.Process.Pid)

	// Wait for Chrome to start
	time.Sleep(3 * time.Second)

	// Connect to Chrome
	resolvedURL, resolveErr = launcher.ResolveURL(":" + t.debugPort)
	if resolveErr != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to resolve Chrome URL: %w", resolveErr)
	}
	logging.Info("Resolved WebSocket URL: %s", resolvedURL)

	browser = rod.New().ControlURL(resolvedURL)
	if err := browser.Connect(); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to connect to Chrome: %w", err)
	}

	logging.Info("Connected to Chrome successfully")
	t.browser = browser
	return nil
}

// ensureBrowserAndPage ensures both browser connection and a persistent page exist
func (t *BrowserChromeTool) ensureBrowserAndPage() error {
	// First ensure browser is connected
	if err := t.ensureBrowser(); err != nil {
		return err
	}

	// Check if we have a valid page
	if t.page != nil {
		// Verify page is still valid by trying to get info
		_, err := t.page.Info()
		if err == nil {
			return nil
		}
		// Page is stale, need a new one
		logging.Info("Page connection is stale, creating new page...")
		t.page = nil
	}

	// Create a new persistent page using MustPage which properly initializes everything
	logging.Info("Creating new browser page...")

	// Get list of existing pages first
	pages, err := t.browser.Pages()
	if err == nil && len(pages) > 0 {
		// Use the first existing page if available
		t.page = pages[0]
		logging.Info("Using existing browser page")
		return nil
	}

	// Create new page - MustPage handles all initialization
	t.page = t.browser.MustPage("")
	logging.Info("Browser page created successfully")
	return nil
}
