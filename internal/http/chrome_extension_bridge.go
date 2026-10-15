package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	chromeExtensionDefaultToolWait  = 45 * time.Second
	chromeExtensionMaxToolWait      = 120 * time.Second
	chromeExtensionMinCommandWaitMS = 1000
)

type chromeExtensionBridge struct {
	mu       sync.Mutex
	pages    map[string]*chromeExtensionPageState
	commands map[string]*chromeExtensionCommandState
}

type chromeExtensionPageSnapshot struct {
	URL             string                 `json:"url,omitempty"`
	Title           string                 `json:"title,omitempty"`
	VisibilityState string                 `json:"visibility_state,omitempty"`
	UserAgent       string                 `json:"user_agent,omitempty"`
	Viewport        map[string]interface{} `json:"viewport,omitempty"`
}

type chromeExtensionPageState struct {
	PageID           string
	ClientID         string
	ExtensionVersion string
	Page             chromeExtensionPageSnapshot
	RegisteredAt     time.Time
	LastSeenAt       time.Time
	Queue            []string
	// WS is set while the page is reachable through the extension WebSocket.
	WS *chromeExtensionWSConn
}

type chromeExtensionCommandState struct {
	ID        string
	PageID    string
	Action    string
	Params    map[string]interface{}
	CreatedAt time.Time
	ExpiresAt time.Time
	ResultCh  chan chromeExtensionCommandResult
}

type chromeExtensionCommandResult struct {
	PageID     string
	ClientID   string
	OK         bool
	Result     json.RawMessage
	Error      string
	ReceivedAt time.Time
}

type chromeExtensionRegisterRequest struct {
	PageID           string                      `json:"page_id"`
	ClientID         string                      `json:"client_id,omitempty"`
	ExtensionVersion string                      `json:"extension_version,omitempty"`
	Page             chromeExtensionPageSnapshot `json:"page,omitempty"`
}

type chromeExtensionResultRequest struct {
	PageID   string          `json:"page_id,omitempty"`
	ClientID string          `json:"client_id,omitempty"`
	OK       bool            `json:"ok"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type chromeExtensionCommandWire struct {
	ID        string                 `json:"id"`
	Action    string                 `json:"action"`
	Params    map[string]interface{} `json:"params,omitempty"`
	TimeoutMS int                    `json:"timeout_ms"`
	CreatedAt string                 `json:"created_at"`
}

func newChromeExtensionBridge() *chromeExtensionBridge {
	return &chromeExtensionBridge{
		pages:    make(map[string]*chromeExtensionPageState),
		commands: make(map[string]*chromeExtensionCommandState),
	}
}

func (b *chromeExtensionBridge) listPages() []map[string]interface{} {
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(now)

	pages := make([]map[string]interface{}, 0, len(b.pages))
	for _, page := range b.pages {
		pages = append(pages, page.toResponse(now))
	}
	sort.Slice(pages, func(i, j int) bool {
		left, _ := pages[i]["last_seen_at"].(string)
		right, _ := pages[j]["last_seen_at"].(string)
		return left > right
	})
	return pages
}

func (b *chromeExtensionBridge) enqueue(pageID, action string, params map[string]interface{}, timeout time.Duration) (*chromeExtensionCommandState, error) {
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(now)

	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		pageID = b.defaultPageIDLocked()
	}
	if pageID == "" {
		return nil, fmt.Errorf("no active Chrome extension pages are registered; open a page with the A2gent Chrome extension loaded first")
	}
	page, ok := b.pages[pageID]
	if !ok || !page.activeAt() {
		return nil, fmt.Errorf("Chrome extension page is not active: %s", pageID)
	}

	command := &chromeExtensionCommandState{
		ID:        uuid.NewString(),
		PageID:    pageID,
		Action:    action,
		Params:    cloneMap(params),
		CreatedAt: now,
		ExpiresAt: now.Add(timeout),
		ResultCh:  make(chan chromeExtensionCommandResult, 1),
	}
	b.commands[command.ID] = command
	page.Queue = append(page.Queue, command.ID)
	page.WS.wakeUp()
	return command, nil
}

func (c *chromeExtensionCommandState) toWire() *chromeExtensionCommandWire {
	wire := &chromeExtensionCommandWire{
		ID:        c.ID,
		Action:    c.Action,
		Params:    cloneMap(c.Params),
		TimeoutMS: int(time.Until(c.ExpiresAt).Milliseconds()),
		CreatedAt: c.CreatedAt.Format(time.RFC3339Nano),
	}
	if wire.TimeoutMS < chromeExtensionMinCommandWaitMS {
		wire.TimeoutMS = chromeExtensionMinCommandWaitMS
	}
	return wire
}

func (b *chromeExtensionBridge) complete(commandID string, req chromeExtensionResultRequest) error {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return fmt.Errorf("command id is required")
	}

	b.mu.Lock()
	command, ok := b.commands[commandID]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("command not found or already completed: %s", commandID)
	}
	delete(b.commands, commandID)
	b.removeFromPageQueueLocked(command.PageID, commandID)
	b.mu.Unlock()

	result := chromeExtensionCommandResult{
		PageID:     strings.TrimSpace(req.PageID),
		ClientID:   strings.TrimSpace(req.ClientID),
		OK:         req.OK,
		Result:     req.Result,
		Error:      strings.TrimSpace(req.Error),
		ReceivedAt: time.Now().UTC(),
	}
	if len(result.Result) == 0 {
		result.Result = json.RawMessage("null")
	}

	select {
	case command.ResultCh <- result:
	default:
	}
	return nil
}

func (b *chromeExtensionBridge) cancel(commandID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	command, ok := b.commands[commandID]
	if !ok {
		return
	}
	delete(b.commands, commandID)
	b.removeFromPageQueueLocked(command.PageID, commandID)
}

func (b *chromeExtensionBridge) upsertPageLocked(pageID, clientID, extensionVersion string, snapshot chromeExtensionPageSnapshot, now time.Time) *chromeExtensionPageState {
	page := b.pages[pageID]
	if page == nil {
		page = &chromeExtensionPageState{
			PageID:       pageID,
			RegisteredAt: now,
		}
		b.pages[pageID] = page
	}
	page.ClientID = strings.TrimSpace(clientID)
	page.ExtensionVersion = strings.TrimSpace(extensionVersion)
	if snapshot.URL != "" || snapshot.Title != "" || snapshot.VisibilityState != "" || len(snapshot.Viewport) > 0 || snapshot.UserAgent != "" {
		page.Page = snapshot
	}
	page.LastSeenAt = now
	return page
}

func (b *chromeExtensionBridge) cleanupLocked(now time.Time) {
	for pageID, page := range b.pages {
		if !page.activeAt() {
			for _, commandID := range page.Queue {
				delete(b.commands, commandID)
			}
			delete(b.pages, pageID)
		}
	}
	for commandID, command := range b.commands {
		if now.After(command.ExpiresAt) {
			delete(b.commands, commandID)
			b.removeFromPageQueueLocked(command.PageID, commandID)
		}
	}
}

func (b *chromeExtensionBridge) defaultPageIDLocked() string {
	var best *chromeExtensionPageState
	for _, page := range b.pages {
		if !page.activeAt() {
			continue
		}
		if best == nil {
			best = page
			continue
		}
		if page.Page.VisibilityState == "visible" && best.Page.VisibilityState != "visible" {
			best = page
			continue
		}
		if page.LastSeenAt.After(best.LastSeenAt) {
			best = page
		}
	}
	if best == nil {
		return ""
	}
	return best.PageID
}

func (b *chromeExtensionBridge) removeFromPageQueueLocked(pageID, commandID string) {
	page := b.pages[pageID]
	if page == nil || len(page.Queue) == 0 {
		return
	}
	filtered := page.Queue[:0]
	for _, queuedID := range page.Queue {
		if queuedID != commandID {
			filtered = append(filtered, queuedID)
		}
	}
	page.Queue = filtered
}

// activeAt reports whether commands can still reach the page over WebSocket.
func (p *chromeExtensionPageState) activeAt() bool {
	return p.WS != nil
}

func (p *chromeExtensionPageState) toResponse(now time.Time) map[string]interface{} {
	ageMs := now.Sub(p.LastSeenAt).Milliseconds()
	return map[string]interface{}{
		"page_id":           p.PageID,
		"client_id":         p.ClientID,
		"extension_version": p.ExtensionVersion,
		"page":              p.Page,
		"registered_at":     p.RegisteredAt.Format(time.RFC3339Nano),
		"last_seen_at":      p.LastSeenAt.Format(time.RFC3339Nano),
		"age_ms":            ageMs,
		"pending_commands":  len(p.Queue),
	}
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func durationFromMS(raw int, fallback, max time.Duration) time.Duration {
	if raw <= 0 {
		return fallback
	}
	d := time.Duration(raw) * time.Millisecond
	if d > max {
		return max
	}
	return d
}

func (s *Server) registerBrowserExtensionRoutes(r chi.Router) {
	r.Route("/browser-extension", func(r chi.Router) {
		r.Get("/pages", s.handleChromeExtensionListPages)
		r.Get("/ws", s.handleChromeExtensionWS)
		r.Post("/commands/{commandID}/result", s.handleChromeExtensionCommandResult)
	})
}

func (s *Server) handleChromeExtensionListPages(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"pages": s.chromeExtensionBridge.listPages(),
	})
}

func (s *Server) handleChromeExtensionCommandResult(w http.ResponseWriter, r *http.Request) {
	commandID := chi.URLParam(r, "commandID")
	var req chromeExtensionResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := s.chromeExtensionBridge.complete(commandID, req); err != nil {
		s.errorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"ok": true})
}

type chromeExtensionTool struct {
	server *Server
}

type chromeExtensionToolParams struct {
	Action    string `json:"action"`
	PageID    string `json:"page_id,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

func newChromeExtensionTool(server *Server) *chromeExtensionTool {
	return &chromeExtensionTool{server: server}
}

func (t *chromeExtensionTool) Name() string {
	return "chrome_extension"
}

func (t *chromeExtensionTool) Description() string {
	return `Control a user-opened Chrome page through the A2gent Chrome extension.

Actions:
- list_pages: list extension-connected pages and their page_id values
- eval: execute JavaScript in the page MAIN world (requires script)
- get_text: get visible page text
- read_content: get page HTML
- get_interactive_elements: compact list of clickable/typeable elements
- click: click an element by CSS selector
- click_at: move the visible AI cursor and click viewport coordinates
- move_mouse: move the visible AI cursor to viewport coordinates
- type: set/type text into an input/contenteditable element by CSS selector
- press_key: dispatch a keyboard key to the focused element
- scroll: scroll the page or a selected element
- get_console_logs: fetch console/page error logs on demand
- get_network_logs: fetch full stored fetch/XHR logs on demand
- get_diagnostics: fetch page, DOM, console, and network diagnostics on demand
- screenshot: capture the visible tab through the extension

Use list_pages first unless the page_id is already known. Commands require an active tab where the extension content script is loaded and connected to local Brute.`
}

func (t *chromeExtensionTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"list_pages", "eval", "get_text", "read_content", "get_interactive_elements", "click", "click_at", "move_mouse", "type", "press_key", "scroll", "get_console_logs", "get_network_logs", "get_diagnostics", "screenshot"},
				"description": "Operation to perform.",
			},
			"page_id":      map[string]interface{}{"type": "string", "description": "Target page from list_pages. Defaults to the most recently visible extension page."},
			"timeout_ms":   map[string]interface{}{"type": "integer", "description": "Optional command timeout in milliseconds (max 120000)."},
			"script":       map[string]interface{}{"type": "string", "description": "JavaScript for action=eval."},
			"selector":     map[string]interface{}{"type": "string", "description": "CSS selector for click/type/scroll."},
			"text":         map[string]interface{}{"type": "string", "description": "Text for action=type."},
			"key":          map[string]interface{}{"type": "string", "description": "Keyboard key for action=press_key, e.g. Enter, Escape, Tab."},
			"x":            map[string]interface{}{"type": "number", "description": "Viewport X coordinate for click_at/move_mouse/scroll."},
			"y":            map[string]interface{}{"type": "number", "description": "Viewport Y coordinate for click_at/move_mouse/scroll."},
			"page":         map[string]interface{}{"type": "integer", "description": "Page number for get_interactive_elements."},
			"page_size":    map[string]interface{}{"type": "integer", "description": "Page size for list actions."},
			"detail_level": map[string]interface{}{"type": "string", "enum": []string{"compact", "full"}, "description": "Diagnostics detail level; full is intended for on-demand logs."},
		},
		"required": []string{"action"},
	}
}

func (t *chromeExtensionTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var input map[string]interface{}
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	action := strings.TrimSpace(fmt.Sprint(input["action"]))
	if action == "" {
		return &tools.Result{Success: false, Error: "action is required"}, nil
	}
	if action == "list_pages" {
		return jsonToolOutput(map[string]interface{}{
			"action": "list_pages",
			"pages":  t.server.chromeExtensionBridge.listPages(),
		})
	}
	if !chromeExtensionAllowedAction(action) {
		return &tools.Result{Success: false, Error: "invalid action; expected one of: list_pages, eval, get_text, read_content, get_interactive_elements, click, click_at, move_mouse, type, press_key, scroll, get_console_logs, get_network_logs, get_diagnostics, screenshot"}, nil
	}

	pageID, _ := input["page_id"].(string)
	timeout := chromeExtensionDefaultToolWait
	if raw, ok := input["timeout_ms"].(float64); ok {
		timeout = durationFromMS(int(raw), chromeExtensionDefaultToolWait, chromeExtensionMaxToolWait)
	}
	commandParams := cloneMap(input)
	delete(commandParams, "action")
	delete(commandParams, "page_id")
	delete(commandParams, "timeout_ms")

	command, err := t.server.chromeExtensionBridge.enqueue(pageID, action, commandParams, timeout)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	defer t.server.chromeExtensionBridge.cancel(command.ID)

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case result := <-command.ResultCh:
		payload := map[string]interface{}{
			"action":      action,
			"page_id":     command.PageID,
			"command_id":  command.ID,
			"received_at": result.ReceivedAt.Format(time.RFC3339Nano),
			"ok":          result.OK,
			"result":      result.Result,
		}
		if result.Error != "" {
			payload["error"] = result.Error
		}
		if !result.OK {
			body, marshalErr := json.MarshalIndent(payload, "", "  ")
			if marshalErr != nil {
				return nil, fmt.Errorf("failed to encode extension error output: %w", marshalErr)
			}
			message := result.Error
			if message == "" {
				message = string(body)
			}
			return &tools.Result{Success: false, Error: message, Output: string(body), Metadata: map[string]interface{}{"page_id": command.PageID, "command_id": command.ID}}, nil
		}
		toolResult, err := jsonToolOutput(payload)
		if toolResult != nil {
			toolResult.Metadata = map[string]interface{}{"page_id": command.PageID, "command_id": command.ID}
		}
		return toolResult, err
	case <-waitCtx.Done():
		return &tools.Result{Success: false, Error: fmt.Sprintf("timed out waiting for Chrome extension command result after %s", timeout)}, nil
	}
}

func chromeExtensionAllowedAction(action string) bool {
	switch action {
	case "eval", "get_text", "read_content", "get_interactive_elements", "click", "click_at", "move_mouse", "type", "press_key", "scroll", "get_console_logs", "get_network_logs", "get_diagnostics", "screenshot":
		return true
	default:
		return false
	}
}

var _ tools.Tool = (*chromeExtensionTool)(nil)
