package integrationtools

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed browser_act_snapshot.js
var browserActAssets embed.FS

var browserActSnapshotJS = func() string {
	data, err := browserActAssets.ReadFile("browser_act_snapshot.js")
	if err != nil {
		panic("browser_act snapshot script missing: " + err.Error())
	}
	return string(data)
}()

// rodActBrowser drives the shared go-rod page. Node ids come from the snapshot's own WeakMap, so the
// classifier's integer choice is resolved against an actually observed DOM node, never a selector.
type rodActBrowser struct {
	page *rod.Page
}

// openRod holds the browser_chrome operation gate for the whole loop, making the multi-step sequence
// atomic against other browser calls. Concurrent browser_chrome calls queue, as already documented.
func (t *BrowserActTool) openRod(ctx context.Context, url string) (actBrowser, func(), error) {
	if t.chrome == nil {
		return nil, nil, fmt.Errorf("browser_chrome plumbing is not configured")
	}
	if err := t.chrome.acquireOperation(ctx); err != nil {
		return nil, nil, err
	}
	release := t.chrome.releaseOperation
	ensure := t.chrome.ensureBrowserAndPage
	if t.chrome.ensureBrowserAndPageOverride != nil {
		ensure = t.chrome.ensureBrowserAndPageOverride
	}
	if err := ensure(ctx); err != nil {
		release()
		return nil, nil, err
	}
	page, err := t.chrome.pageForContext(ctx)
	if err != nil {
		release()
		return nil, nil, err
	}
	if url != "" {
		target, err := browserChromeNavigationURL(url)
		if err != nil {
			release()
			return nil, nil, err
		}
		if err := page.Navigate(target); err != nil {
			release()
			return nil, nil, err
		}
		if err := page.WaitLoad(); err != nil {
			release()
			return nil, nil, err
		}
	}
	return &rodActBrowser{page: page}, release, nil
}

func (b *rodActBrowser) eval(ctx context.Context, js string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	return b.page.Context(ctx).Eval(js, args...)
}

func (b *rodActBrowser) Observe(ctx context.Context) (*actSnapshot, error) {
	result, err := b.eval(ctx, browserActSnapshotJS)
	if err != nil {
		return nil, err
	}
	if result.Value.Nil() {
		return nil, fmt.Errorf("the document is navigating")
	}
	// gson values are already parsed, so re-marshal before decoding into our structs.
	raw, err := result.Value.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to read the page snapshot: %w", err)
	}
	var snap actSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("failed to decode the page snapshot: %w", err)
	}
	return &snap, nil
}

// MarkerFresh re-runs the whole snapshot and compares only the marker, so text, controls and form
// state are recomputed rather than read from a stale closure.
func (b *rodActBrowser) MarkerFresh(ctx context.Context, snap *actSnapshot) (bool, error) {
	result, err := b.eval(ctx, "() => { const s = ("+browserActSnapshotJS+")(); return s ? s.marker : null; }")
	if err != nil {
		return false, nil // an exception during evaluation means the document moved under us
	}
	return sameActJSON(result, snap.Marker), nil
}

// NodeFresh is the scoped guard: document/url/viewport/form state plus the selected node's identity,
// name, value, state and nearby context. Unrelated visible changes are deliberately tolerated.
func (b *rodActBrowser) NodeFresh(ctx context.Context, snap *actSnapshot, node int) (bool, error) {
	result, err := b.eval(ctx, `(node) => {
		const c = window.__a2gentAct;
		return c ? [c.pageKey(), c.guard(c.nodes.get(node))] : null;
	}`, node)
	if err != nil {
		return false, nil
	}
	guard, ok := snap.Guards[fmt.Sprint(node)]
	if !ok {
		guard = json.RawMessage("null")
	}
	expected, err := json.Marshal([]json.RawMessage{snap.PageKey, guard})
	if err != nil {
		return false, err
	}
	return sameActJSON(result, expected), nil
}

// resolve re-reads current geometry and hit-tests the node. A covered, moved, hidden or disabled
// control yields null, which the caller turns into a stale decision instead of a blind click.
const actResolveJS = `(node, kind) => {
	const e = window.__a2gentAct?.nodes.get(node);
	if (!e?.isConnected || e.matches(':disabled') || e.closest('[aria-disabled="true"],[inert]') ||
		!e.checkVisibility({checkOpacity: true, checkVisibilityCSS: true})) return null;
	if (kind === 'fill' && (e.readOnly || e.getAttribute('aria-readonly') === 'true')) return null;
	const r = e.getBoundingClientRect(), x = r.x + r.width / 2, y = r.y + r.height / 2;
	if (!r.width || !r.height || x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) return null;
	if (!e.contains(document.elementFromPoint(x, y))) return null;
	return {x, y};
}`

func (b *rodActBrowser) resolve(ctx context.Context, node int, kind string) (float64, float64, error) {
	result, err := b.eval(ctx, actResolveJS, node, kind)
	if err != nil {
		return 0, 0, errActStale
	}
	if result.Value.Nil() {
		return 0, 0, errActStale
	}
	point := struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}{}
	raw, err := result.Value.MarshalJSON()
	if err != nil || json.Unmarshal(raw, &point) != nil {
		return 0, 0, errActStale
	}
	return point.X, point.Y, nil
}

func (b *rodActBrowser) clickPoint(ctx context.Context, x, y float64) error {
	page := b.page.Context(ctx)
	if err := page.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return err
	}
	return page.Mouse.Click(proto.InputMouseButtonLeft, 1)
}

func (b *rodActBrowser) Click(ctx context.Context, node int) error {
	x, y, err := b.resolve(ctx, node, "click")
	if err != nil {
		return err
	}
	return b.clickPoint(ctx, x, y)
}

// TypeText focuses the field, issues the browser's own select-all editing command, then inserts text,
// so existing content is replaced and frameworks observe real input events.
func (b *rodActBrowser) TypeText(ctx context.Context, node int, text string) error {
	x, y, err := b.resolve(ctx, node, "fill")
	if err != nil {
		return err
	}
	if err := b.clickPoint(ctx, x, y); err != nil {
		return err
	}
	page := b.page.Context(ctx)
	modifiers := 2 // Ctrl
	if runtime.GOOS == "darwin" {
		modifiers = 4 // Meta
	}
	selectAll := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyDown,
		Key: "a", Code: "KeyA", Modifiers: modifiers, Commands: []string{"selectAll"}}
	if err := selectAll.Call(page); err != nil {
		return err
	}
	keyUp := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyUp,
		Key: "a", Code: "KeyA", Modifiers: modifiers}
	if err := keyUp.Call(page); err != nil {
		return err
	}
	return page.InsertText(text)
}

// SelectOption mutates a native <select> and fires input+change. An interrupted evaluation is a hard
// error, not a stale read: the change event may already have fired.
func (b *rodActBrowser) SelectOption(ctx context.Context, node int, value string) error {
	result, err := b.eval(ctx, `(node, value) => {
		const e = window.__a2gentAct?.nodes.get(node);
		if (!e || e.tagName !== 'SELECT' || e.matches(':disabled') ||
			!e.checkVisibility({checkOpacity: true, checkVisibilityCSS: true})) return false;
		if (![...e.options].some(o => o.value === value && !o.disabled && !o.closest('optgroup[disabled]'))) return false;
		e.value = value;
		e.dispatchEvent(new Event('input', {bubbles: true}));
		e.dispatchEvent(new Event('change', {bubbles: true}));
		return true;
	}`, node, value)
	if err != nil {
		return fmt.Errorf("dropdown execution was interrupted; inspect before retrying: %w", err)
	}
	if !result.Value.Bool() {
		return errActStale
	}
	return nil
}

func (b *rodActBrowser) Scroll(ctx context.Context, delta float64) error {
	event := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel,
		X: 550, Y: 400, DeltaX: 0, DeltaY: delta}
	return event.Call(b.page.Context(ctx))
}

// Settle waits briefly for the page to reflect the action: two animation frames or 50 ms, but up to
// 200 ms for autocomplete options after typing into a combobox. This avoids paying for a prediction
// on a half-built popup. It runs after the action was already recorded.
func (b *rodActBrowser) Settle(ctx context.Context, kind string, node int) error {
	_, err := b.page.Context(ctx).Evaluate(rod.Eval(`(node, kind) => new Promise(resolve => {
		const field = window.__a2gentAct?.nodes.get(node);
		const autocomplete = kind === 'fill' && field?.getAttribute('role') === 'combobox';
		let frames = 0, stopped = false;
		const finish = () => { stopped = true; resolve(); };
		setTimeout(finish, autocomplete ? 200 : 50);
		const ready = () => {
			if (stopped) return;
			const ids = (field?.getAttribute('aria-controls') || field?.getAttribute('aria-owns') || '')
				.split(/\s+/).filter(Boolean);
			const roots = ids.length ? ids.map(id => document.getElementById(id)).filter(Boolean) : [document];
			const options = roots.flatMap(root => [...root.querySelectorAll('[role="option"]')]);
			if (++frames >= 2 && (!autocomplete || options.some(e => {
				const r = e.getBoundingClientRect();
				return r.width && r.height && r.bottom > 0 && r.top < innerHeight &&
					e.checkVisibility({checkOpacity: true, checkVisibilityCSS: true});
			}))) finish();
			else requestAnimationFrame(ready);
		};
		requestAnimationFrame(ready);
	})`, node, kind).ByPromise())
	return err
}

func sameActJSON(result *proto.RuntimeRemoteObject, expected json.RawMessage) bool {
	if result == nil || result.Value.Nil() || len(expected) == 0 {
		return false
	}
	actual, err := result.Value.MarshalJSON()
	if err != nil {
		return false
	}
	var left, right interface{}
	if json.Unmarshal(actual, &left) != nil || json.Unmarshal(expected, &right) != nil {
		return false
	}
	leftJSON, errLeft := json.Marshal(left)
	rightJSON, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && string(leftJSON) == string(rightJSON)
}
