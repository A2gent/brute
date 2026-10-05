package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/tools"
)

const (
	actDefaultSteps    = 15
	actMaxSteps        = 40
	actDefaultTimeout  = 90 * time.Second
	actMaxTimeout      = 300 * time.Second
	actWaitDuration    = 100 * time.Millisecond
	actStallWindow     = 3
	actPageExcerptCap  = 2000
	actBailElementRows = 30
)

// errActStale means the observed page no longer matches the decision. The decision is discarded and
// the step is re-observed; it is never retried as an action, so a stale click cannot double-fire.
var errActStale = errors.New("page changed since the decision")

// actBrowser is the transport seam. go-rod is the only implementation in this phase; the
// chrome_extension bridge can be added behind the same interface without touching the policy.
type actBrowser interface {
	Observe(ctx context.Context) (*actSnapshot, error)
	MarkerFresh(ctx context.Context, snap *actSnapshot) (bool, error)
	NodeFresh(ctx context.Context, snap *actSnapshot, node int) (bool, error)
	Click(ctx context.Context, node int) error
	TypeText(ctx context.Context, node int, text string) error
	SelectOption(ctx context.Context, node int, value string) error
	Scroll(ctx context.Context, delta float64) error
	Settle(ctx context.Context, kind string, node int) error
}

// actTextGenerator writes the value for TYPE_TEXT. Neither the classifier nor the executor invents
// field text: the classifier only returns indices, and no string is extracted from the page by code.
type actTextGenerator interface {
	FieldText(ctx context.Context, goal string, field actAction, snap *actSnapshot) (string, error)
}

// BrowserActTool runs a bounded browser loop in one tool call, so a multi-step browser sequence costs
// one tool result instead of one per click.
type BrowserActTool struct {
	chrome    *BrowserChromeTool
	client    *jev.Client
	text      actTextGenerator
	openProxy func(ctx context.Context) (actBrowser, func(), error)
}

func NewBrowserActTool(chrome *BrowserChromeTool, client *jev.Client) *BrowserActTool {
	return &BrowserActTool{chrome: chrome, client: client}
}

func (t *BrowserActTool) Name() string { return "browser_act" }

func (t *BrowserActTool) Description() string {
	return `Pursue one browser goal over multiple steps in a single call, returning a compact trace instead of per-click page dumps.

A classifier picks an operation (CLICK, TYPE_TEXT, SELECT, SCROLL, WAIT, DONE, BLOCKED) and a visible
element index each step; only indices are ever chosen, never selectors or JavaScript. Use it for
multi-step flows (search, filter, navigate a form). Shares and serializes the same Chrome instance as
browser_chrome, so do not call both in one turn.

Returns status done | blocked | low_confidence | needs_text | max_steps | error. On blocked,
low_confidence and needs_text, control returns to you with the element table so you can continue with
browser_chrome. "done" is the model's claim, not verification: confirm it yourself if it matters.

Not supported: shadow DOM, iframes, canvas, file uploads, password fields, pop-up tabs, nested
scrolling, hover-only menus.`
}

func (t *BrowserActTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"goal": map[string]interface{}{
				"type":        "string",
				"description": "Natural-language goal for this browser sequence, including any concrete values to enter.",
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "Optional URL to navigate to first. Otherwise the current page is used.",
			},
			"max_steps": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum browser actions (default %d, max %d).", actDefaultSteps, actMaxSteps),
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("Wall-clock budget for the whole loop (default %d, max %d).", actDefaultTimeout.Milliseconds(), actMaxTimeout.Milliseconds()),
			},
			"values": map[string]interface{}{
				"type":        "object",
				"description": "Optional field values keyed by field label, used when the loop needs to type text.",
			},
		},
		"required": []string{"goal"},
	}
}

type actParams struct {
	Goal      string            `json:"goal"`
	URL       string            `json:"url"`
	MaxSteps  int               `json:"max_steps"`
	TimeoutMS int               `json:"timeout_ms"`
	Values    map[string]string `json:"values"`
}

type actStep struct {
	Operation   string
	Target      string
	Label       string
	Text        string
	Probability float64
	Uncertain   bool
	PageChanged bool
}

type actRun struct {
	Status   string
	Steps    []actStep
	Requests int
	Note     string
	Snapshot *actSnapshot
	Space    actSpace
}

func (t *BrowserActTool) Execute(ctx context.Context, raw json.RawMessage) (*tools.Result, error) {
	var params actParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to parse input: %v", err)}, nil
	}
	if strings.TrimSpace(params.Goal) == "" {
		return &tools.Result{Success: false, Error: "goal is required"}, nil
	}
	if t.client == nil {
		return &tools.Result{Success: false, Error: "browser_act needs Jev credentials; use browser_chrome instead"}, nil
	}
	if params.MaxSteps <= 0 || params.MaxSteps > actMaxSteps {
		params.MaxSteps = min(max(params.MaxSteps, actDefaultSteps), actMaxSteps)
	}
	timeout := actDefaultTimeout
	if params.TimeoutMS > 0 {
		timeout = min(time.Duration(params.TimeoutMS)*time.Millisecond, actMaxTimeout)
	}

	loopCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	browser, release, err := t.open(loopCtx, params.URL)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to prepare Chrome: %v", err)}, nil
	}
	defer release()

	run := t.run(loopCtx, browser, params)
	return &tools.Result{
		Success:  run.Status != "error",
		Output:   formatActRun(run, params),
		Metadata: map[string]interface{}{"status": run.Status, "steps": len(run.Steps), "jev_requests": run.Requests},
	}, nil
}

func (t *BrowserActTool) open(ctx context.Context, url string) (actBrowser, func(), error) {
	if t.openProxy != nil {
		return t.openProxy(ctx)
	}
	return t.openRod(ctx, url)
}

// run is the loop. Every exit path is a bounded status, never a page dump.
func (t *BrowserActTool) run(ctx context.Context, browser actBrowser, params actParams) actRun {
	run := actRun{Status: "error"}
	snap, err := browser.Observe(ctx)
	if err != nil {
		run.Note = fmt.Sprintf("failed to observe the page: %v", err)
		return run
	}
	run.Snapshot = snap
	uncertain := 0

	for {
		if len(run.Steps) >= params.MaxSteps {
			run.Status, run.Note = "max_steps", fmt.Sprintf("stopped at the %d-action budget", params.MaxSteps)
			return run
		}
		if run.Requests >= params.MaxSteps*2 {
			run.Status, run.Note = "max_steps", "stopped at the classifier request budget"
			return run
		}
		if ctx.Err() != nil {
			run.Status, run.Note = "max_steps", "stopped at the time budget"
			return run
		}

		if fresh, err := browser.MarkerFresh(ctx, snap); err != nil || !fresh {
			if snap, err = browser.Observe(ctx); err != nil {
				run.Note = fmt.Sprintf("failed to re-observe the page: %v", err)
				return run
			}
			run.Snapshot = snap
		}

		space := buildActSpace(snap.Actions)
		run.Space = space
		decision, err := decideAct(ctx, t.client, snap, space, params.Goal, actHistory(run.Steps))
		run.Requests++
		if err != nil {
			run.Note = fmt.Sprintf("classifier failed, nothing executed: %v", err)
			return run
		}

		if decision.Operation == "BLOCKED" {
			run.Status, run.Note = "blocked", "the classifier sees no supported operation that makes progress"
			return run
		}
		confidence := decision.minConfidence()
		if confidence < actMediumConfidence {
			run.Status = "low_confidence"
			run.Note = fmt.Sprintf("confidence %.2f below %.2f on %s, nothing executed", confidence, actMediumConfidence, decision.Operation)
			return run
		}
		if confidence < actHighConfidence {
			if uncertain++; uncertain > actMaxUncertain {
				run.Status, run.Note = "low_confidence", fmt.Sprintf("%d uncertain steps in a row", uncertain-1)
				return run
			}
		}

		if decision.Operation == "DONE" {
			// A DONE decided on a stale page proves nothing: re-observe and decide again.
			if fresh, err := browser.MarkerFresh(ctx, snap); err == nil && !fresh {
				continue
			}
			run.Status = "done"
			run.Note = "DONE is the classifier's claim, not verification. Confirm with browser_chrome if it matters."
			return run
		}

		text := ""
		if decision.Operation == "TYPE_TEXT" {
			value, err := t.fieldText(ctx, params, decision.Action, snap)
			if err != nil {
				run.Status = "needs_text"
				run.Note = fmt.Sprintf("needs a value for field %q (%s, currently %q): %v",
					decision.Action.Label, decision.Action.Role, decision.Action.Value, err)
				return run
			}
			text = value
		}

		if err := t.execute(ctx, browser, snap, decision, text); err != nil {
			if errors.Is(err, errActStale) {
				if snap, err = browser.Observe(ctx); err != nil {
					run.Note = fmt.Sprintf("failed to re-observe after a stale decision: %v", err)
					return run
				}
				run.Snapshot = snap
				continue
			}
			run.Note = fmt.Sprintf("failed to execute %s: %v", decision.Operation, err)
			return run
		}

		// Record execution before observing: a navigation during observation must not erase the action.
		run.Steps = append(run.Steps, actStep{Operation: decision.Operation, Target: decision.Target,
			Label: decision.Action.Label, Text: text, Probability: decision.Probability,
			Uncertain: confidence < actHighConfidence})
		tools.ReportProgress(ctx, tools.ProgressEvent{Status: "running",
			Content: fmt.Sprintf("step %d: %s %s", len(run.Steps), decision.Operation, decision.Action.Label)})

		_ = browser.Settle(ctx, decision.Action.Kind, decision.Action.Node)
		previous := snap.Marker
		if snap, err = browser.Observe(ctx); err != nil {
			run.Note = fmt.Sprintf("executed %s but failed to observe the result: %v", decision.Operation, err)
			return run
		}
		run.Snapshot = snap
		run.Steps[len(run.Steps)-1].PageChanged = !bytes.Equal(previous, snap.Marker)

		if actStalled(run.Steps) {
			run.Status, run.Note = "blocked", fmt.Sprintf("%d actions in a row changed nothing", actStallWindow)
			return run
		}
	}
}

func (t *BrowserActTool) execute(ctx context.Context, browser actBrowser, snap *actSnapshot, decision *actDecision, text string) error {
	// Scoped node guard for mutations that depend on one control; everything else compares the
	// whole observation, because time passed while the classifier was deciding.
	if decision.Action.Kind == "click" || decision.Action.Kind == "select" {
		fresh, err := browser.NodeFresh(ctx, snap, decision.Action.Node)
		if err != nil {
			return err
		}
		if !fresh {
			return errActStale
		}
	} else if decision.Operation != "WAIT" {
		fresh, err := browser.MarkerFresh(ctx, snap)
		if err != nil {
			return err
		}
		if !fresh {
			return errActStale
		}
	}

	switch decision.Operation {
	case "CLICK":
		return browser.Click(ctx, decision.Action.Node)
	case "TYPE_TEXT":
		return browser.TypeText(ctx, decision.Action.Node, text)
	case "SELECT":
		return browser.SelectOption(ctx, decision.Action.Node, decision.Action.Value)
	case "SCROLL_UP", "SCROLL_DOWN":
		return browser.Scroll(ctx, decision.Action.Delta)
	case "WAIT":
		select {
		case <-time.After(actWaitDuration):
		case <-ctx.Done():
		}
		return nil
	}
	return fmt.Errorf("unsupported operation %q", decision.Operation)
}

// fieldText prefers a caller-supplied value, then a configured text model. It never guesses.
func (t *BrowserActTool) fieldText(ctx context.Context, params actParams, field actAction, snap *actSnapshot) (string, error) {
	label := strings.ToLower(strings.TrimSpace(field.Label))
	for key, value := range params.Values {
		if strings.ToLower(strings.TrimSpace(key)) == label && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	if t.text == nil {
		return "", errors.New("no value supplied and no text model configured")
	}
	value, err := t.text.FieldText(ctx, params.Goal, field, snap)
	if err != nil {
		return "", err
	}
	if value = strings.TrimSpace(value); value == "" || len(value) > 2000 {
		return "", errors.New("the text model returned no usable value")
	}
	return value, nil
}

func actHistory(steps []actStep) []actHistoryEntry {
	history := make([]actHistoryEntry, 0, len(steps))
	for _, step := range steps {
		history = append(history, actHistoryEntry{Action: step.Label, Operation: step.Operation,
			Text: step.Text, PageChanged: step.PageChanged})
	}
	return history
}

// actStalled reports repeated actions that changed nothing. WAIT is excluded: waiting is expected to
// leave the page unchanged.
func actStalled(steps []actStep) bool {
	if len(steps) < actStallWindow {
		return false
	}
	for _, step := range steps[len(steps)-actStallWindow:] {
		if step.PageChanged || step.Operation == "WAIT" {
			return false
		}
	}
	return true
}

func formatActRun(run actRun, params actParams) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "status: %s\n", run.Status)
	if run.Snapshot != nil {
		fmt.Fprintf(&sb, "url: %s\ntitle: %s\n", run.Snapshot.URL, run.Snapshot.Title)
	}
	fmt.Fprintf(&sb, "steps: %d actions, %d classifier requests\n", len(run.Steps), run.Requests)
	for i, step := range run.Steps {
		target := step.Target
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(&sb, " %2d %-10s [%s] %s", i+1, step.Operation, target, step.Label)
		if step.Text != "" {
			fmt.Fprintf(&sb, " = %q", step.Text)
		}
		fmt.Fprintf(&sb, " p=%.2f", step.Probability)
		if step.Uncertain {
			sb.WriteString(" uncertain")
		}
		if !step.PageChanged {
			sb.WriteString(" no-change")
		}
		sb.WriteString("\n")
	}
	if run.Snapshot != nil && run.Snapshot.Omitted > 0 {
		fmt.Fprintf(&sb, "omitted: %d elements beyond the %d cap were not selectable\n", run.Snapshot.Omitted, actMaxElements)
	}
	// The element table is only worth its tokens when the main agent has to take over.
	if run.Status == "low_confidence" || run.Status == "blocked" || run.Status == "needs_text" {
		if rows := renderActElements(run.Space, actBailElementRows); rows != "" {
			sb.WriteString("elements:\n")
			sb.WriteString(rows)
		}
	}
	if run.Snapshot != nil && strings.TrimSpace(run.Snapshot.Text) != "" {
		excerpt := run.Snapshot.Text
		if len(excerpt) > actPageExcerptCap {
			excerpt = excerpt[:actPageExcerptCap] + " [truncated]"
		}
		fmt.Fprintf(&sb, "page: %s\n", strings.ReplaceAll(excerpt, "\n", " · "))
	}
	if run.Note != "" {
		fmt.Fprintf(&sb, "note: %s\n", run.Note)
	}
	return sb.String()
}
