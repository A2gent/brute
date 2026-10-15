# Automatic session run mode

Create a session with `queue_mode: "auto"` to classify its task through the configured System One backend. Legacy requests without a queue mode are unchanged.

- Only `read_only` with confidence at least `0.85` takes the ordinary non-queued create path.
- `modifies_project`, `external_side_effects`, low confidence, invalid answers, unavailable Jev, unsafe egress and errors use the per-project serial queue.
- Explicit `queue_mode: "serial"`, `queue_mode: "now"`, `queued: true`, and leading `/q`, `/queue`, `--queue`, `-q` or `/now` directives override automatic classification. `/now` also overrides `queued: true`.
- Task text is checked with `jev.SafeEgressText` before a UTF-8-safe 32 KiB head/tail preview is sent. Classification is performed once at creation, not once per turn.

The decision is stored as session metadata and returned as `run_mode_decision` in create, detail and list responses:

```json
{"label":"read_only","confidence":0.99,"source":"jev"}
```

Manual decisions use labels `now` or `serial`, confidence `1`, and source `manual`. Failed/unavailable automatic classification uses `unknown`, confidence `0`, and source `jev`. Auxiliary-call counters and timings use operation `run_mode`; no call metrics are emitted when no classifier request was made.

## Write escalation

An automatic read-only run exposes only a conservative allowlist of reading/search tools, plus `request_write`. File editors, git commit/push tools, shell, arbitrary code, SQL, delegation and unknown integration tools are hidden. The same policy is enforced during execution, including namespaced tools and nested `parallel`/`pipeline` calls. This is a capability restriction, not an OS filesystem sandbox.

When the model calls `request_write`, the current tool batch finishes without write capabilities, the run stops, and the same session enters the project serial queue. Its original decision remains available for audit. Only the serial worker owning the session's run lease lifts the read-only guard, then resumes from the saved transcript.

Direct remote/team targets are conservatively queued because their tools cannot be guarded locally. Native CLI and fallback runtimes likewise enter the serial queue before executing model work, including after provider transitions.
