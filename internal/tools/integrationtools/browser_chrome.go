package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserChromeTool allows controlling a Chrome browser instance.
type BrowserChromeTool struct {
	mu               sync.Mutex
	browser          *rod.Browser
	page             *rod.Page // Persistent page across calls
	workDir          string
	debugPort        string
	userDataDir      string
	profileDir       string
	profileDirectory string
	headless         bool
	capabilities     []string
}

const browserChromeActionTimeout = 45 * time.Second

// NewBrowserChromeTool creates a new instance of the browser tool.
func NewBrowserChromeTool(workDir string) *BrowserChromeTool {
	debugPort := os.Getenv("CHROME_DEBUG_PORT")
	if debugPort == "" {
		debugPort = "9223"
	}

	userDataDir := strings.TrimSpace(os.Getenv("CHROME_USER_DATA_DIR"))
	if userDataDir == "" {
		userDataDir = AgentChromeDebugUserDataDir()
	}

	profileDir := strings.TrimSpace(os.Getenv("CHROME_AGENT_PROFILE_DIR"))
	if profileDir == "" {
		profileDir = AgentChromeProfileDir()
	}

	profileDirectory := strings.TrimSpace(os.Getenv("CHROME_PROFILE_DIRECTORY"))
	if profileDirectory == "" {
		profileDirectory = AgentChromeProfileDirectoryName
	}

	return &BrowserChromeTool{
		workDir:          workDir,
		debugPort:        debugPort,
		userDataDir:      userDataDir,
		profileDir:       profileDir,
		profileDirectory: profileDirectory,
		headless:         strings.ToLower(os.Getenv("CHROME_HEADLESS")) == "true",
		capabilities:     []string{"navigate", "click", "type", "scroll", "screenshot", "read_content", "eval"},
	}
}

func (t *BrowserChromeTool) Name() string {
	return "browser_chrome"
}

func (t *BrowserChromeTool) Description() string {
	return `Control a Chrome browser instance with a dedicated agent profile.

Actions:
- navigate: Go to a URL (requires 'url')
- get_interactive_elements: Compact paginated DOM snapshot of clickable/typeable/visually clickable elements with selectors, text, state, and viewport coordinates. USE THIS FIRST to find controls.
- get_text: Get page text content (simplified, no HTML)
- click: Click an element (requires 'selector')
- click_at: Click at specific pixel coordinates (requires 'x' and 'y')
- type: Type text into an input (requires 'selector' and 'text')
- press_key: Press a keyboard key (requires 'key', e.g. 'Enter', 'Escape', 'Tab')
- scroll: Scroll page or element (optional 'x', 'y' in pixels, optional 'selector' for element)
- screenshot: Take a screenshot
- read_content: Get full HTML (verbose)
- eval: Run JavaScript (requires 'script')

Workflow: navigate -> get_interactive_elements -> click/type -> verify with get_text or screenshot.
Browser state is shared and actions are serialized. Do not call browser_chrome through parallel or issue multiple browser_chrome calls in the same turn.
For visual apps, menus, tabs, timetables, canvases, or pages where text exists but the right control is unclear, take a screenshot and use click_at with the coordinates from get_interactive_elements or the screenshot.
Prefer get_text/get_interactive_elements for cheap orientation; use screenshot only when visual layout matters or DOM signals are incomplete.`
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

	t.mu.Lock()
	defer t.mu.Unlock()

	// Ensure browser and page are ready
	if err := t.ensureBrowserAndPage(); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to ensure browser: %v", err)}, nil
	}

	opCtx, cancel := context.WithTimeout(ctx, browserChromeActionTimeout)
	defer cancel()
	page := t.page.Context(opCtx)

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
		return &tools.Result{
			Success: true,
			Output:  fmt.Sprintf("Screenshot saved to %s", screenshotPath),
			Metadata: map[string]interface{}{
				"image_file": map[string]interface{}{
					"path":        screenshotPath,
					"format":      "png",
					"bytes":       len(data),
					"source_tool": t.Name(),
					"action":      "screenshot",
				},
			},
		}, nil

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

		// Get all interactive elements with unique selectors.
		// Includes visually clickable elements (cursor:pointer/tab/menu controls) because
		// many JS-heavy sites expose navigation tabs as styled div/span elements rather
		// than semantic links or buttons.
		// Returns data in TOON format (Token-Oriented Object Notation) for compact LLM output
		result := page.MustEval(fmt.Sprintf(`(offset, perPage) => {
			const elements = [];
			const seen = new Set();

			const cssEscape = window.CSS && CSS.escape
				? CSS.escape
				: (value) => String(value).replace(/[^a-zA-Z0-9_-]/g, '\\$&');

			const selectorFor = (el, globalIdx) => {
				if (el.id) return '#' + cssEscape(el.id);
				if (el.getAttribute('data-testid')) return '[data-testid="' + el.getAttribute('data-testid').replace(/"/g, '\\"') + '"]';
				if (el.getAttribute('aria-label')) {
					const label = el.getAttribute('aria-label').replace(/"/g, '\\"');
					const byLabel = el.tagName.toLowerCase() + '[aria-label="' + label + '"]';
					if (document.querySelectorAll(byLabel).length === 1) return byLabel;
				}
				if (el.name) return el.tagName.toLowerCase() + '[name="' + el.name.replace(/"/g, '\\"') + '"]';

				const path = [];
				let current = el;
				while (current && current !== document.body) {
					let seg = current.tagName.toLowerCase();
					if (current.id) {
						seg = '#' + cssEscape(current.id);
						path.unshift(seg);
						break;
					}
					const siblings = current.parentElement
						? Array.from(current.parentElement.children).filter(c => c.tagName === current.tagName)
						: [];
					if (siblings.length > 1) {
						seg += ':nth-of-type(' + (siblings.indexOf(current) + 1) + ')';
					}
					path.unshift(seg);
					current = current.parentElement;
					if (path.length > 5) break;
				}
				let selector = path.join(' > ');
				if (!selector || document.querySelectorAll(selector).length !== 1 || seen.has(selector)) {
					selector = el.tagName.toLowerCase() + '[data-a2gent-idx="' + globalIdx + '"]';
					el.setAttribute('data-a2gent-idx', globalIdx);
				}
				return selector;
			};

			const baseSelector = [
				'input', 'textarea', 'button', 'a[href]', 'select', 'summary',
				'[role="button"]', '[role="link"]', '[role="tab"]', '[role="menuitem"]',
				'[role="option"]', '[role="checkbox"]', '[role="radio"]',
				'[onclick]', '[tabindex]:not([tabindex="-1"])'
			].join(',');
			const candidates = new Set(Array.from(document.querySelectorAll(baseSelector)));
			document.querySelectorAll('body *').forEach((el) => {
				const style = getComputedStyle(el);
				if (style.cursor === 'pointer') candidates.add(el);
			});

			Array.from(candidates).forEach((el, globalIdx) => {
				if ((el.type || '').toLowerCase() === 'hidden') return;
				const rect = el.getBoundingClientRect();
				if (rect.width === 0 || rect.height === 0) return;
				const style = getComputedStyle(el);
				if (style.visibility === 'hidden' || style.display === 'none' || style.pointerEvents === 'none') return;
				if (el.offsetParent === null && style.position !== 'fixed') return;

				const selector = selectorFor(el, globalIdx);
				seen.add(selector);

				const text = (el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || el.title || '').replace(/\s+/g, ' ').substring(0, 80).trim();
				const stateParts = [];
				for (const attr of ['aria-selected', 'aria-current', 'aria-expanded', 'aria-checked']) {
					const value = el.getAttribute(attr);
					if (value) stateParts.push(attr.replace('aria-', '') + '=' + value);
				}
				if (el.classList && Array.from(el.classList).some(c => /active|selected|current|open/i.test(c))) {
					stateParts.push('class=' + Array.from(el.classList).filter(c => /active|selected|current|open/i.test(c)).slice(0, 3).join('|'));
				}
				
				elements.push({
					selector: selector,
					tag: el.tagName.toLowerCase(),
					role: el.getAttribute('role') || '',
					type: el.type || (style.cursor === 'pointer' ? 'pointer' : ''),
					text: text,
					x: Math.round(rect.left + rect.width / 2),
					y: Math.round(rect.top + rect.height / 2),
					w: Math.round(rect.width),
					h: Math.round(rect.height),
					state: stateParts.join(';'),
					href: el.href ? el.href.substring(0, 100) : undefined
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
		wrappedScript := buildBrowserEvalScript(script)
		result, err := page.Eval(wrappedScript)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to eval: %v", err)}, nil
		}
		// Handle undefined/null results
		if result.Value.Nil() {
			return &tools.Result{Success: true, Output: "OK"}, nil
		}
		return &tools.Result{Success: true, Output: result.Value.JSON("", "  ")}, nil

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
	sb.WriteString(fmt.Sprintf("elements[%d]{selector,tag,role,type,text,x,y,w,h,state,href}:\n", len(elements)))

	for _, elem := range elements {
		el, ok := elem.(map[string]interface{})
		if !ok {
			continue
		}

		selector := escapeField(getString(el, "selector"))
		tag := getString(el, "tag")
		role := getString(el, "role")
		typ := getString(el, "type")
		text := escapeField(getString(el, "text"))
		x := formatNumberField(el, "x")
		y := formatNumberField(el, "y")
		w := formatNumberField(el, "w")
		h := formatNumberField(el, "h")
		state := escapeField(getString(el, "state"))
		href := getString(el, "href")

		// TOON row format: value1,value2,value3,...
		sb.WriteString(fmt.Sprintf("  %s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n", selector, tag, role, typ, text, x, y, w, h, state, href))
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

func formatNumberField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("%.0f", n)
		case float32:
			return fmt.Sprintf("%.0f", n)
		case int:
			return fmt.Sprintf("%d", n)
		case int64:
			return fmt.Sprintf("%d", n)
		case json.Number:
			return n.String()
		}
	}
	return ""
}

func buildBrowserEvalScript(script string) string {
	encoded, err := json.Marshal(script)
	if err != nil {
		encoded = []byte(`""`)
	}
	return fmt.Sprintf(`() => {
		const __script = %s;
		try {
			return (0, eval)(__script);
		} catch (__exprErr) {
			try {
				return (new Function(__script))();
			} catch (__stmtErr) {
				return {
					error: String(__stmtErr && __stmtErr.message ? __stmtErr.message : __stmtErr),
					expression_error: String(__exprErr && __exprErr.message ? __exprErr.message : __exprErr)
				};
			}
		}
	}`, string(encoded))
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
