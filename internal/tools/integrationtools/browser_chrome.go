package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/gratheon/aagent/internal/logging"
	"github.com/gratheon/aagent/internal/tools"
)

// BrowserChromeTool allows controlling a Chrome browser instance.
type BrowserChromeTool struct {
	browser      *rod.Browser
	page         *rod.Page // Persistent page across calls
	workDir      string
	debugPort    string
	userDataDir  string
	headless     bool
	capabilities []string
}

// NewBrowserChromeTool creates a new instance of the browser tool.
func NewBrowserChromeTool(workDir string) *BrowserChromeTool {
	debugPort := os.Getenv("CHROME_DEBUG_PORT")
	if debugPort == "" {
		debugPort = "9223"
	}

	userDataDir := os.Getenv("CHROME_USER_DATA_DIR")
	if userDataDir == "" {
		userDataDir = getAgentChromeUserDataDir()
	}

	return &BrowserChromeTool{
		workDir:      workDir,
		debugPort:    debugPort,
		userDataDir:  userDataDir,
		headless:     strings.ToLower(os.Getenv("CHROME_HEADLESS")) == "true",
		capabilities: []string{"navigate", "click", "type", "scroll", "screenshot", "read_content", "eval"},
	}
}

func getDefaultChromeUserDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

func getAgentChromeUserDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Use a SEPARATE Chrome user-data directory (not inside default Chrome)
	// This allows remote debugging AND preserves encrypted credentials
	return filepath.Join(home, "Library", "Application Support", "Google", "ChromeAgent")
}

func (t *BrowserChromeTool) Name() string {
	return "browser_chrome"
}

func (t *BrowserChromeTool) Description() string {
	return "Control a Chrome browser instance with a dedicated agent profile. Can navigate, click, type, scroll, take screenshots, and read content. Use 'navigate' action with a URL to start browsing. The agent profile is separate from your main Chrome profile and can run simultaneously."
}

func (t *BrowserChromeTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"navigate", "click", "type", "scroll", "screenshot", "read_content", "eval"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (required for 'navigate')",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector for the element",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to type (required for 'type')",
			},
			"script": map[string]interface{}{
				"type":        "string",
				"description": "JavaScript to evaluate (required for 'eval')",
			},
		},
		"required": []string{"action"},
	}
}

func (t *BrowserChromeTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var input map[string]interface{}
	if err := json.Unmarshal(params, &input); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to parse input: %v", err)}, nil
	}

	action, ok := input["action"].(string)
	if !ok || action == "" {
		return &tools.Result{Success: false, Error: "action is required"}, nil
	}

	// Ensure browser and page are ready
	if err := t.ensureBrowserAndPage(); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to ensure browser: %v", err)}, nil
	}

	page := t.page

	switch action {
	case "navigate":
		url, ok := input["url"].(string)
		if !ok || url == "" {
			return &tools.Result{Success: false, Error: "url is required for navigate"}, nil
		}
		if err := page.Navigate(url); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to navigate: %v", err)}, nil
		}
		page.WaitLoad()
		return &tools.Result{Success: true, Output: fmt.Sprintf("Navigated to %s", page.MustInfo().URL)}, nil

	case "click":
		selector, ok := input["selector"].(string)
		if !ok || selector == "" {
			return &tools.Result{Success: false, Error: "selector is required for click"}, nil
		}
		el, err := page.Element(selector)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to find element: %v", err)}, nil
		}
		if err := el.Click("left", 1); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to click: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: "Clicked element"}, nil

	case "type":
		selector, _ := input["selector"].(string)
		text, _ := input["text"].(string)
		if selector == "" || text == "" {
			return &tools.Result{Success: false, Error: "selector and text are required for type"}, nil
		}
		el, err := page.Element(selector)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to find element: %v", err)}, nil
		}
		if err := el.Input(text); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to type: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: "Typed text"}, nil

	case "screenshot":
		screenshotPath := filepath.Join(t.workDir, fmt.Sprintf("screenshot_%d.png", time.Now().Unix()))
		data, err := page.Screenshot(false, nil)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to take screenshot: %v", err)}, nil
		}
		if err := os.WriteFile(screenshotPath, data, 0644); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to save screenshot: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("Screenshot saved to %s", screenshotPath)}, nil

	case "read_content":
		content, err := page.HTML()
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read content: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: content}, nil

	case "eval":
		script, _ := input["script"].(string)
		if script == "" {
			return &tools.Result{Success: false, Error: "script is required for eval"}, nil
		}
		result, err := page.Eval(script)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to eval: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("%v", result)}, nil

	default:
		return &tools.Result{Success: false, Error: fmt.Sprintf("unknown action: %s", action)}, nil
	}
}

// isChromeRunning checks if Chrome is currently running
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

	// Chrome not running with debugging, check if any Chrome is open
	if isChromeRunning() {
		return fmt.Errorf(`Chrome is already running without remote debugging.

On macOS, you cannot have multiple Chrome instances open simultaneously.

To use browser_chrome:
1. Close Chrome completely (Cmd+Q)
2. Click "Open Agent Profile" in the UI, OR run the agent again

The agent will launch Chrome with remote debugging enabled.`)
	}

	logging.Info("No Chrome running, launching new instance...")

	// Ensure the ChromeAgent directory exists
	if err := os.MkdirAll(t.userDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create ChromeAgent directory: %w", err)
	}

	// Launch Chrome with the dedicated agent user-data-dir
	// This is NOT inside the default Chrome directory, so remote debugging works
	// AND credentials are encrypted with keys specific to this directory (they persist!)
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

	logging.Info("Launching Chrome with user-data-dir: %s", t.userDataDir)
	logging.Info("Headless mode: %v", t.headless)

	args := []string{
		"--user-data-dir=" + t.userDataDir,
		"--remote-debugging-port=" + t.debugPort,
		"--no-first-run",
		"--no-default-browser-check",
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
