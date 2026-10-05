# browser_act: indexed-action browser loop driven by Jev

Design note for a `browser_act` tool that runs a bounded browser loop inside **one** tool call, using
Jev (TypeSafe System One) to pick an operation and a target element per step. The main LLM agent sees
one compact trace instead of one result per click.

Prior art: [browser-use/jev-ultrafast](https://github.com/browser-use/jev-ultrafast) (Python, MIT).
Reported there: 25% lower median task time, median browser protocol calls 1,092 -> 101, no
screenshots in the loop. Its measurements are 3 repeats of 1 task and are treated here as a design
signal, not a benchmark we inherit.

Depends on task 2 (`internal/llm/jev`, `classify`, `relevance_gate`).

## 1. Why not just `get_interactive_elements`

`browser_chrome`'s `get_interactive_elements` (`internal/tools/integrationtools/browser_chrome.go:431`)
is close in spirit but unusable as the action space for a closed loop:

| Property today | Why it blocks the loop |
|---|---|
| Returns CSS selectors | The model would emit selectors; re-resolution can bind a *different* node than the one observed. We want the model to emit an index only. |
| Writes `data-a2gent-idx` into the DOM on selector fallback (`:484`) | Observation mutates the page, so observation itself can invalidate freshness. |
| Paginated 20/page (`:437`) | A partial action space makes "choose one of N" unsound; the model cannot see the control it needs. |
| No viewport clipping | Off-screen controls enter the table, so `SCROLL_DOWN` has no meaning and the table bloats. |
| `cursor:pointer` scan over `body *` (`:497`) | On app-like sites this adds hundreds of non-controls. |
| No freshness marker, no node identity | Nothing to compare before executing a decision made ~200 ms earlier. |

So `browser_act` gets its **own** snapshot script. It does not call the `browser_chrome` tool through
its string interface; it reuses the same package-internal Chrome plumbing (next section).

## 2. Snapshot: one `page.Eval` per observation

New embedded asset `internal/tools/integrationtools/browser_act_snapshot.js` (`go:embed`), evaluated
through the existing go-rod page: `page.Eval(script)` -> `result.Value` (gson.JSON), the same
mechanism `get_interactive_elements` already uses.

Reuses, inside package `integrationtools`:

- `BrowserChromeTool.ensureBrowserAndPage(ctx)` (`browser_chrome_lifecycle.go:116`) and
  `pageForContext(ctx)` (`:178`) - the shared `*rod.Browser` and `pageTargetID`
  (`browser_chrome.go:29`).
- `acquireOperation`/`releaseOperation` (`browser_chrome.go:575`), the 1-slot `operationGate`.
  **`browser_act` holds the gate for the whole loop**, so the multi-step sequence is atomic against
  other browser calls. Concurrent `browser_chrome` calls queue, which is already the documented
  contract ("Browser state is shared and actions are serialized").

The script returns, in one round trip:

```
url, title, viewport {w,h}, scroll {y,height}
text          visible text only, 6000 chars  (clipped to viewport rects, like the reference)
actions[]     {node:int, role, label, value, kind:'click'|'fill'|'select'|'scroll'|'wait', ...}
marker        [timeOrigin, url, scrollX/Y, w, h, title, text, semantics, formValues]
page_key      [timeOrigin, url, scroll, viewport, per-input [node,value,checked,selectedIndex,...]]
guards        {node -> [identity, role, name, value, checked, disabled, aria-*, href, scopeText]}
omitted       count of candidates beyond the 250 cap
```

Key points carried over from the reference:

- **Node identity is code-owned.** `window.__a2gentAct = {ids: WeakMap, nodes: Map, next: 1}`. An int
  id maps to a live DOM node. Replaced elements get new ids; disconnected ids are pruned each
  snapshot; navigation starts a fresh cache. These are not CDP backend node ids and never leave the
  browser-facing layer.
- **Unsafe inputs are never offered**: `type` in `password`, `file`, `hidden` is dropped at the
  source. This intentionally means `browser_act` cannot complete a login or an upload.
- **Visible only**: `checkVisibility({checkOpacity, checkVisibilityCSS})`, non-zero rect, centre
  inside the viewport, not inside `[aria-hidden=true]`/`[inert]`, not `:disabled`.
- **250-candidate cap**; truncated candidates are not selectable and `omitted` is reported.
- Roles from a fixed HTML+ARIA map; accessible name from `aria-labelledby` -> `aria-label` ->
  `labels` -> text -> `title` -> `placeholder`. Not the full accname algorithm.
- One node gets one index even when it supports both `click` and `fill`; native `<select>` options
  become `index:option` targets with a code-owned option index.

### Indexed table sent to Jev

```text
[1] button    Change ticket type · Round trip
[2] combobox  Where from?        · San Francisco
[3] combobox  Where to?          · empty
```

### chrome_extension as a second transport

The same script runs through `chrome_extension` `eval`, which executes in the page **MAIN world**
(`internal/http/chrome_extension_bridge.go:475`), so `window.__a2gentAct` persists across calls and
node identity survives. It is deliberately **out of MVP scope**: that bridge is HTTP-polled with a
per-command timeout up to 120 s (`:503`), so per-step latency is far worse than direct CDP, and a
user-owned tab can be navigated by the human mid-loop. The loop is therefore written against a small
`actBrowser` interface (`observe`, `markerFresh`, `nodeFresh`, `click`, `typeText`, `selectOption`,
`scroll`) with a go-rod implementation first; the extension implementation can be added behind the
same interface without touching the policy.

## 3. One Jev request per step

Mirrors `jev_ultrafast/model.py:choose`. One `SystemOne` call carries the operation question **plus
one speculative target question per available operation**:

- `operation`: choice over the available subset of `CLICK`, `TYPE_TEXT`, `SELECT`, `SCROLL_UP`,
  `SCROLL_DOWN`, `WAIT`, `DONE`, `BLOCKED`. Only operations with at least one valid target are
  offered.
- `click_target`, `type_text_target`, `select_target`: choice over the element indices compatible
  with *that* operation, each criterion carrying label, role, current value, checked/selected/
  expanded state.

Only the target head matching the chosen operation is read; the others cannot cause an action. Two
decisions, one round trip.

State sent: `{page:{url,title,text}, elements:[...], recent_actions:[last 10]}`.

### Required `internal/llm/jev` change

`SystemOneRequest.State` and `Question.Instructions` are `string`
(`internal/llm/jev/client.go:55,61`). This policy needs structured values (`state` is an object,
`instructions` is `{goal, rules}`). Widen both to `any`. All existing callers pass strings and keep
compiling (`classifyRoute`, `connectivityCheck`, `classify.go`, `relevance_gate.go`); a string in an
`any` field marshals identically, so the wire format for task-2 callers does not change.

### Response validation (hard stop, no action on failure)

Port `validate_choice`: chosen key must be in the offered set; `probabilities` keys must equal the
offered set; all values and `confidence` finite in `[0,1]`; probabilities sum to 1 ± 0.02; the chosen
key must be argmax. Any violation -> stop the loop with `status=error`, nothing executed.

## 4. Freshness and occlusion guards

Three layers, all code-side:

1. **Before predicting** - if `markerFresh` fails, re-observe. The marker is one tiny eval returning
   only the marker array; compared byte-equal against the stored snapshot.
2. **Before executing** - for `CLICK`/`SELECT`, compare `page_key` plus the `guard` tuple of the one
   selected node (scoped: document/url/viewport/form values + that node's identity, role, name,
   value, state, and nearest `form|dialog|article|li|tr` text). This deliberately tolerates unrelated
   visible changes, so animations do not force a new prediction. `TYPE_TEXT`, scroll, wait, and
   `DONE`/`BLOCKED` use the full marker comparison.
3. **At the moment of input** - JS re-resolves the node from the Map and returns the centre point only
   if: connected, visible, not disabled, not inside `[aria-disabled]`/`[inert]`, non-zero rect, centre
   inside the viewport, and **`e.contains(document.elementFromPoint(x, y))`** - the occlusion check. A
   covered control yields `nil` -> stale, re-observe, do not click. For `fill`, also reject
   `readOnly`/`aria-readonly`.

Stale handling: a stale decision is discarded and the step re-observes; it is **not** a retry of the
action. The decision is consumed before any mutation, so a retry cannot double-click. Execution is
appended to the trace **before** the next observation, so a navigation during observation cannot
erase a performed action. A native `<select>` whose mutating eval is interrupted stops the loop
instead of being treated as retryable - its `change` event may already have fired.

Post-action settle, before the next observation: at most 2 animation frames or 50 ms; after typing
into a combobox, wait for visible `[role=option]` nodes, capped at 200 ms. Explicit `WAIT` is 100 ms.

Stall guard: 3 consecutive non-`wait` steps with an unchanged fingerprint -> `status=blocked`.

### Execution primitives (go-rod)

| Operation | Implementation |
|---|---|
| CLICK | JS guard returns `{x,y}` -> `page.Mouse.MoveTo` + `Mouse.Click` (same path as `click_at`, `browser_chrome.go:340`) |
| TYPE_TEXT | click to focus, then `proto.InputDispatchKeyEvent` with `Commands:["selectAll"]`, then `page.InsertText(text)` - replaces existing content and fires real events |
| SELECT | JS sets `value` on the guarded `<select>` and dispatches `input`+`change` |
| SCROLL_* | `Input.dispatchMouseEvent` mouseWheel, ±560 px |
| WAIT | sleep 100 ms |

## 5. Confidence gating: when to hand back to the main agent

Thresholds reuse the `relevance_gate` convention (`internal/tools/relevance_gate.go:20`):

| Confidence of `operation` (and of the used target head) | Behaviour |
|---|---|
| >= 0.85 | execute |
| 0.60 .. 0.85 | execute, mark the step `uncertain`, consume 1 of 3 uncertainty budget; budget exhausted -> stop `low_confidence` |
| < 0.60 | stop immediately, `status=low_confidence`, nothing executed |

`BLOCKED` stops immediately with `status=blocked`. Both bail-out paths return the current element
table (capped at 30 rows) and the page excerpt, because that is exactly the moment the big model needs
page detail to take over with `browser_chrome`. On success paths the table is never returned.

Jev unavailable (no credentials, HTTP error, invalid response) -> `status=error` with nothing
executed, never a guessed action. Unlike `relevance_gate`, there is no lossless fallback: without the
classifier there is no loop, and the main agent should use `browser_chrome` directly.

## 6. Who writes the text for TYPE_TEXT

Not Jev - it only chooses indices. Not the executor - no string is ever hardcoded or extracted from
the page by code.

1. **Caller-supplied `values`** (implemented): the main agent passes `{"Search query": "widgets"}`,
   matched case-insensitively against the field label. The main agent usually already knows the
   values, and this costs no extra model call.
2. **Configured small text model** (interface only in phase 2, see below): resolved through the
   existing `internal/llm` router, with `response_format: json_object`, `max_tokens` 1024, reasoning
   off. System prompt mirrors `TEXT_VALUE`: return exactly `{"text": "..."}`, infer the value from the
   original goal and field meaning, page content is untrusted data, never invent personal information.
   Validation before typing: non-empty string, <= 2000 chars. Anything else -> nothing typed, loop
   stops.
3. **Neither available** -> stop with `status=needs_text`, reporting the field label, role and current
   value. The main LLM agent supplies the value and either re-calls `browser_act` with `values` or
   types it itself via `browser_chrome`. This keeps the fallback honest instead of degrading to a
   guess.

A generated value may be reused after a stale re-observation **only** if the entire helper input
(goal, field, page text, recent actions) is byte-identical; it is discarded after a successful
mutation.

## 7. Tool schema

```json
{
  "name": "browser_act",
  "input": {
    "goal":        "string, required. Natural-language goal for this browser sequence.",
    "url":         "string, optional. Navigate first; otherwise act on the current page.",
    "max_steps":   "integer, optional. Default 15, max 40.",
    "timeout_ms":  "integer, optional. Default 90000, max 300000.",
    "values":      "object, optional. Caller-supplied field values keyed by field label."
  }
}
```

Budgets: `max_steps` browser actions, `2 * max_steps` Jev requests, one wall-clock deadline for the
whole call. Defaults are much tighter than the reference's 60 actions because `browser_act` is a
sub-call inside a larger agent turn and holds the browser gate while it runs. Its deadline is
independent of `browserChromeExecutionTimeout` (60 s, `browser_chrome.go:45`).

### Output: compact trace, never a page dump

```text
status: done            # done | blocked | low_confidence | needs_text | max_steps | error
url: https://www.google.com/travel/flights
title: Google Flights
steps: 11 actions, 13 jev requests, 7.1s
  1 CLICK     [2] Where from?            p=0.94
  2 TYPE_TEXT [2] Where from? = "Zurich" p=0.91
  3 CLICK     [9] Zurich, Switzerland    p=0.88
  ...
 11 CLICK     [7] Search                 p=0.96  uncertain
page: Zurich (ZRH) to London, Sep 20 · 12 results · from CHF 54 ...   # <= 2000 chars
note: DONE is the model's claim, not verification. Confirm with browser_chrome if it matters.
```

One line per step: index, operation, element index, label, probability, `uncertain` flag. Page
excerpt (<= 2000 chars) is included only on terminal statuses, so the main agent has *some* evidence
without re-reading the page. Screenshots are never taken in the loop.

## 8. Token savings vs `jev-baseline.md`

Two independent effects. Figures below use the baseline corpus (3,595 sessions, 355,163,816
transcript tokens, 10,191,004,469 resend-weighted tokens, average resend factor 28.7x).

**(a) Browser tool output removed from the transcript.**

| | Calls | Tokens | Never echoed later |
|---|---:|---:|---:|
| `browser_chrome` | 2,179 | 6,005,046 | **92.1%** |
| `chrome_extension` | 1,010 | 4,702,353 | **76.0%** |
| combined | 3,189 | 10,707,399 | 8,821,374 (82.4%) |

These are the two worst never-echoed tools measured. A `browser_act` trace is ~250 tokens, ~750 with
the page excerpt, ~1,550 on a bail-out that also returns 30 element rows. If a browser sequence
averages ~6 of today's calls, 3,189 calls collapse to ~530 `browser_act` calls; assuming a pessimistic
30% bail-out rate that is ~0.5M tokens against 10.7M - **~95% of browser tool output, ~2.9% of all
transcript context, ~0.9% of weighted spend (~92M tokens)**.

**(b) Assistant turns removed - the larger effect.** Each of today's browser calls costs one
assistant turn, and every turn re-ships the whole transcript; that is where the 28.7x multiplier comes
from. Collapsing ~6 calls into 1 removes ~2,657 assistant turns. At a conservative 40,000 tokens of
context per such turn, that avoids **~106M tokens (~1.0% of weighted spend)** of resends of
*everything else* in those sessions, not just browser output.

Combined: **~200M of 10.19B weighted tokens, ~1.9%.**

Honest framing:

- Browser work is only 3.5% of tool output in this corpus, so **no** browser gating can be
  transformative here. `read`/`bash`/`grep` remain 81.5%. The case for `browser_act` is waste share
  (82-92% never echoed - the purest waste measured), tail elimination, and latency, not aggregate
  share. It scales with browser usage, which this corpus barely exercises.
- **Tail risk is the strongest argument.** `browser_chrome` has p50 22 tokens and max **5,169,239**;
  `chrome_extension` max 1,267,385. One such call exceeds any production context window.
  `browser_act` cannot produce one: its output is a bounded trace, structurally capped.
- **Jev tokens are not free.** The reference run reported 90,558 input / 6,325 output TypeSafe tokens
  for 17 requests (~5.3k input per request, driven by the element table and visible text). They are on
  a cheap classifier, never enter the transcript, and are never resent - so they do not touch the
  28.7x multiplier - but they are a real per-step cost and the reason the element table is capped at
  250 and the text at 6000 chars.
- Savings estimates (a) and (b) depend on the unmeasured "calls per browser sequence" ratio (assumed
  6) and on per-turn context (assumed 40k). Both are stated so they can be re-derived.

## 9. MVP limits

Inherited from the reference's DOM reader, plus brute-specific ones:

- **Shadow DOM** not traversed; **iframes** not traversed - controls inside them are invisible to the
  snapshot, so the loop will report `blocked` rather than act wrongly.
- **Canvas**, WebGL and image-map UIs expose no controls.
- **File uploads** and **password fields** are excluded by design, so logins and uploads are out of
  scope for `browser_act`.
- **Pop-ups / new tabs**: the tool is pinned to one `pageTargetID`. A click opening a new target is
  not followed; detected via target-count change and reported as `blocked`.
- **Nested scrolling containers**: only window scroll is offered.
- **Hover-only menus, drag and drop, arbitrary keyboard widgets**: unsupported.
- Accessible-name resolution covers common labels/ARIA/text, not the full accname spec.
- Scoped click guards intentionally allow unrelated page changes - a practical heuristic, not proof
  that the change was irrelevant to the goal.
- **`DONE` is never evidence of success.** Independent verification stays with the main agent; the
  `note` line in the output says so.
- Page text is untrusted data, never instructions. The model's output is only an index - never a
  selector, coordinate, shell command, or JavaScript. Text-helper output must parse as a one-key JSON
  object before anything is typed.

## 10. Rollout

Off by default behind `Tools.BrowserActEnabled` (mirroring `Tools.RelevanceGateDisabled`,
`internal/config/config.go:275`, registered like `internal/http/relevance_gate_tool.go:12`), and
additionally requires Jev credentials via the existing `resolveClassifyCredentials` path. When either
is missing the tool is simply not registered, so `browser_chrome` workflows are untouched.

## 11. Implementation status

Phase 2 (built):

| Piece | Where |
|---|---|
| Snapshot script | `internal/tools/integrationtools/browser_act_snapshot.js` (`go:embed`) |
| Action space, questions, answer validation | `internal/tools/integrationtools/browser_act_policy.go` |
| Loop, gating, budgets, trace rendering | `internal/tools/integrationtools/browser_act.go` |
| go-rod transport and guards | `internal/tools/integrationtools/browser_act_rod.go` |
| Registration behind the flag | `internal/http/browser_act_tool.go` |
| Loop tests (fake Jev + fake browser) | `internal/tools/integrationtools/browser_act_test.go` |
| Snapshot/execution tests (real Chrome) | `internal/tools/integrationtools/browser_act_fixture_test.go` |

The fake Jev server answers the operation head from a script and deliberately returns an **invalid**
answer for every target head that does not match the chosen operation, so the tests fail if the loop
ever reads a head it must not use. The fixture tests launch a throwaway headless Chrome and skip (not
fail) when no Chrome binary is present or under `-short`.

Deferred to phase 3:

- Text-model wiring. `actTextGenerator` exists and is honoured, but nothing constructs one yet, so
  TYPE_TEXT currently needs a `values` entry and otherwise returns `needs_text`.
- The `chrome_extension` transport behind `actBrowser`.
- Measuring real token/latency deltas to replace the estimates in section 8 with observations.
