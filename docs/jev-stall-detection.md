# Opt-in stall detection

Runaway sessions resend their growing history on every step (see `brute/docs/jev-baseline.md`). Stall checks interrupt repetition before the step limit without sending the full transcript to the classifier.

Enable in the Brute server's `config.json`:

```json
{
  "tools": {
    "jev_stall": {
      "enabled": true,
      "interval": 8,
      "window": 12
    }
  }
}
```

The feature defaults **off**. `interval` defaults to 8 agent steps; `window` defaults to the last 12 tool calls and is bounded to 3-64. It uses the configured System One backend and credentials, independently of admission gating and tool selection. Programmatic callers can opt in with `agent.Config.StallDetector`.

After tool execution, either the interval or a cheap pre-filter triggers one System One request containing two questions:

- Noul: is the agent making progress toward the task?
- Choice: `progressing`, `repeating`, or `blocked_needs_user`.

The pre-filter fires when a new call brings its tool-name/JSON-argument pair to three matches (ignoring JSON formatting and object key order), or its error result to two matches in the recent window. Old repeats do not keep triggering checks after a change of approach. Hashes compare full inputs/results; only tool names, argument heads, status, and result first lines enter the digest. The task context comes from `contextcompress.RelevanceTask`. Checks use a three-second timeout and the existing Jev secret-egress guard.

If noul indicates progress (probability >= 0.5), the run continues unchanged. Otherwise:

- `repeating`: persist a short system nudge to change approach or ask the user before the next main-model request.
- `blocked_needs_user`, choice confidence >= 0.85: persist a user-facing question, set `input_required`, and stop the run without another main-model request. The normal pending-question API handles the answer/resume flow.
- Uncertain blocking decisions, missing credentials, malformed classifier responses, timeouts, and API failures: continue unchanged (fail-open).

Tracker state is bounded and local to one run, so answering a question or starting a new task does not inherit stale repetition evidence. Each attempted request records `stall_check_attempt_count`, duration, timestamp, and outcome through `recordAuxiliaryCall`.

Verification:

```sh
cd brute
go test ./internal/agent
go test ./internal/config ./internal/http -run 'TestJevStall|TestStallDetector'
```
