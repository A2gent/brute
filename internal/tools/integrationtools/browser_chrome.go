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
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/tools"
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
	return `Control a Chrome browser instance with a dedicated agent profile.

Actions:
- navigate: Go to a URL (requires 'url')
- get_interactive_elements: List all clickable/typeable elements with CSS selectors - USE THIS FIRST to find selectors
- get_text: Get page text content (simplified, no HTML)
- click: Click an element (requires 'selector')
- click_at: Click at specific pixel coordinates (requires 'x' and 'y')
- type: Type text into an input (requires 'selector' and 'text')
- press_key: Press a keyboard key (requires 'key', e.g. 'Enter', 'Escape', 'Tab')
- scroll: Scroll page or element (optional 'x', 'y' in pixels, optional 'selector' for element)
- screenshot: Take a screenshot
- read_content: Get full HTML (verbose)
- eval: Run JavaScript (requires 'script')

Workflow: navigate -> get_interactive_elements -> click/type -> press_key Enter to submit`
}

func (t *BrowserChromeTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform",
				"enum":        []string{"navigate", "click", "click_at", "type", "press_key", "scroll", "screenshot", "read_content", "get_interactive_elements", "get_text", "eval"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (required for 'navigate')",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector for the element",
			},
			"page": map[string]interface{}{
				"type":        "integer",
				"description": "Page number for get_interactive_elements (default 1, 20 items per page)",
			},
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to type (required for 'type')",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Key to press (required for 'press_key'). Examples: 'Enter', 'Escape', 'Tab', 'Backspace', 'ArrowDown'",
			},
			"x": map[string]interface{}{
				"type":        "number",
				"description": "X coordinate in pixels (required for 'click_at')",
			},
			"y": map[string]interface{}{
				"type":        "number",
				"description": "Y coordinate in pixels (required for 'click_at')",
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
		// Wait for page to be stable before interacting
		page.MustWaitStable()
		el, err := page.Element(escapeCSSSelector(selector))
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
		// Wait for page to be stable before interacting
		page.MustWaitStable()
		el, err := page.Element(escapeCSSSelector(selector))
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to find element: %v", err)}, nil
		}
		if err := el.Input(text); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to type: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: "Typed text"}, nil

	case "press_key":
		key, _ := input["key"].(string)
		if key == "" {
			return &tools.Result{Success: false, Error: "key is required for press_key"}, nil
		}
		// Wait for page to be stable before pressing key
		page.MustWaitStable()
		// Map common key names to input.Key constants
		inputKey := keyFromString(key)
		if inputKey == 0 {
			return &tools.Result{Success: false, Error: fmt.Sprintf("unknown key: %s. Supported: Enter, Escape, Tab, Backspace, Delete, ArrowUp/Down/Left/Right, Home, End, PageUp, PageDown, Space", key)}, nil
		}
		if err := page.Keyboard.Press(inputKey); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to press key: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("Pressed key: %s", key)}, nil

	case "click_at":
		x, xOk := input["x"].(float64)
		y, yOk := input["y"].(float64)
		if !xOk || !yOk {
			return &tools.Result{Success: false, Error: "x and y coordinates are required for click_at"}, nil
		}
		// Wait for page to be stable before clicking
		page.MustWaitStable()
		// Move mouse to coordinates and click
		page.Mouse.MustMoveTo(x, y)
		if err := page.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to click: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("Clicked at (%d, %d)", int(x), int(y))}, nil

	case "scroll":
		// Scroll by pixel amount. Positive y = scroll down, negative y = scroll up
		// Positive x = scroll right, negative x = scroll left
		// Optional selector to scroll within a specific element
		scrollX := 0.0
		scrollY := 0.0
		if x, ok := input["x"].(float64); ok {
			scrollX = x
		}
		if y, ok := input["y"].(float64); ok {
			scrollY = y
		}
		if scrollX == 0 && scrollY == 0 {
			// Default: scroll down by 500px
			scrollY = 500
		}
		page.MustWaitStable()

		selector, hasSelector := input["selector"].(string)
		if hasSelector && selector != "" {
			// Scroll within a specific element
			el, err := page.Element(escapeCSSSelector(selector))
			if err != nil {
				return &tools.Result{Success: false, Error: fmt.Sprintf("failed to find element: %v", err)}, nil
			}
			// Use JavaScript to scroll the element
			_, err = el.Eval(fmt.Sprintf(`(el) => { el.scrollBy(%f, %f); }`, scrollX, scrollY))
			if err != nil {
				return &tools.Result{Success: false, Error: fmt.Sprintf("failed to scroll element: %v", err)}, nil
			}
			return &tools.Result{Success: true, Output: fmt.Sprintf("Scrolled element %s by (%d, %d)", selector, int(scrollX), int(scrollY))}, nil
		}

		// Scroll the page
		if err := page.Mouse.Scroll(scrollX, scrollY, 1); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to scroll: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("Scrolled page by (%d, %d)", int(scrollX), int(scrollY))}, nil

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

	case "get_text":
		// Get simplified text content without HTML tags
		body, err := page.Element("body")
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to find body: %v", err)}, nil
		}
		text, err := body.Text()
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to get text: %v", err)}, nil
		}
		return &tools.Result{Success: true, Output: text}, nil

	case "get_interactive_elements":
		// Get page number (default 1)
		pageNum := 1
		if p, ok := input["page"].(float64); ok && p > 0 {
			pageNum = int(p)
		}
		const perPage = 20
		offset := (pageNum - 1) * perPage

		// Get all interactive elements (inputs, buttons, links) with unique selectors
		// Returns data in TOON format (Token-Oriented Object Notation) for compact LLM output
		result := page.MustEval(fmt.Sprintf(`(offset, perPage) => {
			const elements = [];
			const seen = new Set();
			
			document.querySelectorAll('input, textarea, button, a[href], select, [role="button"], [onclick]').forEach((el, globalIdx) => {
				if (el.type === 'hidden') return;
				const rect = el.getBoundingClientRect();
				if (rect.width === 0 && rect.height === 0) return;
				if (el.offsetParent === null && getComputedStyle(el).position !== 'fixed') return;
				
				// Build unique selector
				let selector = '';
				if (el.id) {
					selector = '#' + el.id;
				} else if (el.name) {
					selector = el.tagName.toLowerCase() + '[name="' + el.name + '"]';
				} else if (el.getAttribute('data-testid')) {
					selector = '[data-testid="' + el.getAttribute('data-testid') + '"]';
				} else {
					// Build path-based selector
					let path = [];
					let current = el;
					while (current && current !== document.body) {
						let seg = current.tagName.toLowerCase();
						if (current.id) {
							seg = '#' + current.id;
							path.unshift(seg);
							break;
						}
						const siblings = current.parentElement ? 
							Array.from(current.parentElement.children).filter(c => c.tagName === current.tagName) : [];
						if (siblings.length > 1) {
							const idx = siblings.indexOf(current) + 1;
							seg += ':nth-of-type(' + idx + ')';
						}
						path.unshift(seg);
						current = current.parentElement;
						if (path.length > 3) break;
					}
					selector = path.join(' > ');
				}
				
				if (seen.has(selector)) {
					selector = el.tagName.toLowerCase() + '[data-idx="' + globalIdx + '"]';
					el.setAttribute('data-idx', globalIdx);
				}
				seen.add(selector);
				
				const text = (el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || '').substring(0, 60).trim();
				
				elements.push({
					selector: selector,
					tag: el.tagName.toLowerCase(),
					type: el.type || el.getAttribute('role') || '',
					text: text,
					href: el.href ? el.href.substring(0, 80) : undefined
				});
			});
			
			const total = elements.length;
			const paged = elements.slice(%d, %d);
			return { elements: paged, total: total, page: %d, perPage: %d, hasMore: total > %d };
		}`, offset, offset+perPage, pageNum, perPage, offset+perPage))

		// Format output in TOON format (Token-Oriented Object Notation)
		// TOON is ~40%% more token-efficient than JSON for structured data
		return &tools.Result{Success: true, Output: formatElementsAsTOON(result)}, nil

	case "eval":
		script, _ := input["script"].(string)
		if script == "" {
			return &tools.Result{Success: false, Error: "script is required for eval"}, nil
		}
		// Wait for page to be stable before evaluating
		page.MustWaitStable()
		// Wrap script to handle both expressions and statements
		// This prevents "apply is not a function" errors for assignment statements
		wrappedScript := fmt.Sprintf(`() => { %s }`, script)
		result, err := page.Eval(wrappedScript)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to eval: %v", err)}, nil
		}
		// Handle undefined/null results
		if result.Value.Nil() {
			return &tools.Result{Success: true, Output: "OK"}, nil
		}
		return &tools.Result{Success: true, Output: fmt.Sprintf("%v", result.Value)}, nil

	default:
		return &tools.Result{Success: false, Error: fmt.Sprintf("unknown action: %s", action)}, nil
	}
}

// formatElementsAsTOON formats the interactive elements result in TOON format
// TOON (Token-Oriented Object Notation) is a compact, LLM-friendly format
// that reduces token usage by ~40% compared to JSON
// See: https://github.com/toon-format/toon
func formatElementsAsTOON(result interface{}) string {
	data, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Sprintf("%v", result)
	}

	elements, ok := data["elements"].([]interface{})
	if !ok {
		return fmt.Sprintf("%v", result)
	}

	total := int(data["total"].(float64))
	pageNum := int(data["page"].(float64))
	perPage := int(data["perPage"].(float64))
	hasMore := data["hasMore"].(bool)

	var sb strings.Builder

	// TOON header with metadata
	sb.WriteString(fmt.Sprintf("total: %d\n", total))
	sb.WriteString(fmt.Sprintf("page: %d\n", pageNum))
	sb.WriteString(fmt.Sprintf("perPage: %d\n", perPage))
	sb.WriteString(fmt.Sprintf("hasMore: %v\n", hasMore))

	// TOON tabular array format: key[N]{field1,field2,...}:
	sb.WriteString(fmt.Sprintf("elements[%d]{selector,tag,type,text,href}:\n", len(elements)))

	for _, elem := range elements {
		el, ok := elem.(map[string]interface{})
		if !ok {
			continue
		}

		selector := escapeField(getString(el, "selector"))
		tag := getString(el, "tag")
		typ := getString(el, "type")
		text := escapeField(getString(el, "text"))
		href := getString(el, "href")

		// TOON row format: value1,value2,value3,...
		sb.WriteString(fmt.Sprintf("  %s,%s,%s,%s,%s\n", selector, tag, typ, text, href))
	}

	return sb.String()
}

// getString safely extracts a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// escapeField escapes a TOON field value if it contains special characters
// Per TOON spec: quote with " if value contains , or newline; escape " as ""
func escapeField(s string) string {
	if s == "" {
		return s
	}
	needsQuoting := strings.ContainsAny(s, ",\n\r\"")
	if !needsQuoting {
		return s
	}
	// Escape internal quotes by doubling them
	escaped := strings.ReplaceAll(s, "\"", "\"\"")
	return "\"" + escaped + "\""
}

// escapeCSSSelector escapes special characters in CSS selectors
// React generates IDs like ":r1:" which need escaping as CSS doesn't allow unescaped colons in IDs
func escapeCSSSelector(selector string) string {
	// If selector starts with # (ID selector), escape special chars in the ID part
	if strings.HasPrefix(selector, "#") {
		id := selector[1:]
		// Escape colons and other special CSS characters in the ID
		var escaped strings.Builder
		escaped.WriteByte('#')
		for _, c := range id {
			// CSS special characters that need escaping: : . [ ] ( ) # > + ~ = ^ $ * | ! @ % &
			if c == ':' || c == '.' || c == '[' || c == ']' || c == '(' || c == ')' ||
				c == '#' || c == '>' || c == '+' || c == '~' || c == '=' || c == '^' ||
				c == '$' || c == '*' || c == '|' || c == '!' || c == '@' || c == '%' ||
				c == '&' || c == '/' || c == '\\' {
				escaped.WriteByte('\\')
			}
			escaped.WriteRune(c)
		}
		return escaped.String()
	}
	return selector
}

// keyFromString converts a key name string to input.Key
// Supports common key names like Enter, Escape, Tab, etc.
func keyFromString(key string) input.Key {
	switch strings.ToLower(key) {
	case "enter", "return":
		return input.Enter
	case "escape", "esc":
		return input.Escape
	case "tab":
		return input.Tab
	case "backspace":
		return input.Backspace
	case "delete":
		return input.Delete
	case "space":
		return input.Space
	case "arrowup", "up":
		return input.ArrowUp
	case "arrowdown", "down":
		return input.ArrowDown
	case "arrowleft", "left":
		return input.ArrowLeft
	case "arrowright", "right":
		return input.ArrowRight
	case "home":
		return input.Home
	case "end":
		return input.End
	case "pageup":
		return input.PageUp
	case "pagedown":
		return input.PageDown
	case "f1":
		return input.F1
	case "f2":
		return input.F2
	case "f3":
		return input.F3
	case "f4":
		return input.F4
	case "f5":
		return input.F5
	case "f6":
		return input.F6
	case "f7":
		return input.F7
	case "f8":
		return input.F8
	case "f9":
		return input.F9
	case "f10":
		return input.F10
	case "f11":
		return input.F11
	case "f12":
		return input.F12
	default:
		return 0
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
