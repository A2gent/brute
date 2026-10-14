# Claude CLI persistent-process prototype

## Enable and scope

Set `AAGENT_CLAUDE_CLI_PERSISTENT=1` in Brute's environment and restart Brute.
The default remains the one-process-per-request transport. No Caesar changes or
speculative warmup on focus/typing are included. This prototype uses the direct
Claude CLI, not the Agent SDK streaming-input implementation.

Persistent mode requires a nonempty `ChatRequest.SessionID`, session persistence,
and no `MaxBudgetUSD` (the CLI budget is process-cumulative, not per input turn).
It is disabled on Windows until process-tree cancellation is implemented there.
An explicitly enabled SDK sidecar retains precedence and remains per-request.
Non-streaming `Chat` also uses the persistent streaming transport when eligible.

## Lifecycle and safety

`internal/llm/claudecli/persistent.go` keeps a process pool across rebuilt Client
instances. Each process uses `-p --input-format stream-json --output-format
stream-json --verbose --include-partial-messages`. Each input is one native user
NDJSON envelope, including base64 image content blocks. A `result` completes a
turn, not the process; stdin stays open.

- Pool identity includes Brute session, provider identity, executable, workdir,
  home and config directories. Environment, model, exact system prompt, tool
  definitions, permission flags and MCP bridge generation are fingerprinted.
- The incoming identity-bound resume cursor must match the previous result.
  Missing/changed cursors or changed fingerprints replace the process. New
  processes use the existing `--resume` validation and native permission mapping.
- Idle timeout is 10 minutes. Go embedders can set `Options.PersistentIdleTimeout`.
- Active cancellation or callback/protocol/result errors kill the process group,
  close stdin, wait/reap, and revoke the bridge. Cancelling a context after a
  successful turn does not kill its idle process.
- HTTP server shutdown calls `ClosePersistentProcesses`. Other embedders must
  call it on shutdown. Disabling persistent mode for a session closes its cached
  process on the next request.
- Overlapping turns on the same pool key are rejected, not interleaved.
- Startup failures before input submission fall back to the legacy spawn path.
  Once input is written, the current turn is **never replayed automatically**:
  native/MCP tools might already have performed side effects. Such errors are
  unsafe for retry and future requests use the legacy path for 10 minutes.
  Cancellation evicts without the temporary fallback marker.

### MCP configuration

Brute's a2gent MCP bridge is already an HTTP endpoint, not a new stdio server.
The old path minted a bearer token per invocation. Persistent mode mints once
per process using a detached request context, keeps the identical inline config,
and revokes it when the process ends. Cancellation still cancels blocked bridge
calls through revocation. Native `--allowedTools`, `--tools`, `--permission-mode`
and `--strict-mcp-config` construction are unchanged.

`Options.MCPBridgeGeneration` represents stable endpoint/config identity,
excluding the token. HTTP wiring includes the bridge-state identity and port;
request tool definitions participate in the process fingerprint. Embedders must
change the generation when changing their hook configuration. Hook errors abort
persistent startup, so a temporary outage cannot pin a bridge-less process.

## Measurements

`internal/llm/claudecli/timing.go` records UTC RFC3339Nano milestones and logs one
`Claude CLI turn timing session=...` JSON object per attempted streamed turn:

- `llm_turn_started_at`, `llm_process_spawn_at`
- `llm_first_stream_line_at`, `llm_system_init_at`
- `llm_first_text_delta_at`, `llm_result_at`
- `llm_first_stream_line_ms`, `llm_spawn_to_init_ms`, `llm_init_to_text_ms`
- `llm_ttft_ms`, `llm_turn_result_ms`
- `llm_transport`: `cli_spawn`, `cli_persistent`, or `sdk_spawn`
- `llm_process_reused`

Successful response metadata is merged through `llmTimingMetadata` into the
assistant message. Failed/cancelled turns retain available milestones in logs;
error-message metadata does not yet carry provider timings. Missing milestones
are absent, not zero. Reused turns have no spawn timestamp. TTFT is measured from
transport setup immediately before spawn (or persistent acquisition), not from
browser input; existing `llm_duration_ms` covers the wider agent call.

`spawn -> init` is a startup/resume/handshake proxy. `init -> text` is **not pure
model inference**: it includes network, queueing, thinking and tools before text.
Text-only non-streaming legacy `Chat` has no stream milestones. SDK measurements
start at Node spawn and include SDK/Claude startup; they exclude sidecar-specific
permission and warning envelopes from the first Claude stream milestone.

### Live result, 2026-10-14

macOS arm64, Claude Code 2.1.299, model alias `sonnet`, authenticated Claude Max.
One five-turn session per transport, new Client each turn, resume cursor carried
forward, fixed prompt asking for `OK`, no tools/MCP. All numbers below are ms.

| Transport | Turn | First stream line | Init to text | TTFT | Result |
|---|---:|---:|---:|---:|---:|
| Spawn | 2 | 1162 | 1674 | 2836 | 2860 |
| Persistent | 2 | 7 | 1572 | 1580 | 1590 |
| Spawn | 5 | 1066 | 1587 | 2653 | 2665 |
| Persistent | 5 | 4 | 1546 | 1551 | 1557 |

TTFT decreased by 44.3% on turn 2 and 41.5% on turn 5 in this run. This is a
smoke measurement, not a statistically controlled benchmark: order is fixed,
network/cache conditions vary, sessions are short, and real MCP/tool-heavy
workloads were not benchmarked. Fake-CLI tests verify token reuse and images.
The measured startup reduction does not isolate JSONL parsing from auth/config
or CLI initialization costs.

Reproduce (spends real Claude quota, opt-in test normally skipped):

```sh
cd brute
AAGENT_CLAUDE_TTFT_LIVE=1 go test -v ./internal/llm/claudecli \
  -run '^TestLivePersistentTTFT$' -count=1
```

## Validation

```sh
cd brute
go test ./internal/llm/claudecli/...
just test  # go test -v -race ./...
just lint # go vet ./...
go build ./cmd/aagent
```

Critical offline coverage includes reuse across clients, bridge token lifetime,
idle eviction, prompt/tools/model/environment/permission/bridge/cursor changes,
image stdin, cancellation, malformed output, callback errors, safe pre-send
fallback, and propagation of response timings to assistant metadata.
