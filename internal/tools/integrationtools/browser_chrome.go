package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"github.com/go-rod/rod"
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
