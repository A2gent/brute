# Claude CLI persistent process

## Enable and scope

Persistent mode is **on by default** (decision 2026-10-14, after T-129). Set
`AAGENT_CLAUDE_CLI_PERSISTENT=0` and restart Brute to return to the
one-process-per-request transport. The pool holds at most 4 processes
(`AAGENT_CLAUDE_CLI_PERSISTENT_MAX`, Go: `Options.PersistentMaxProcesses`). No
Caesar changes or speculative warmup on focus/typing are included. This prototype uses the direct
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
- When the pool is full, starting a process first evicts the least recently used
  idle process (closing it and revoking its bridge token). Busy processes are
  never evicted; if every slot is busy, the turn uses the spawn transport.
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

### Benchmark (T-129), 2026-10-14

Conditions: macOS 26 (Darwin 25.3), Apple M5 Pro, 48 GB, load average ~2,
Claude Code 2.1.299, alias `sonnet` (served as `claude-sonnet-5-5`), Claude Max OAuth.
Harness: `internal/llm/claudecli/persistent_bench_live_test.go` and
`internal/llm/claudecli/persistent_warmup_live_test.go`.

- **Long resume JSONL**: a real 3.6 MB session truncated after its last
  `compact_boundary` + summary (2.9 MB, 871 lines). The CLI parses the whole file
  on every `--resume`, but only the summary reaches the model (~23k input tokens per
  turn including the tool list, nearly all cache reads, identical in both modes).
  For comparison, local JSONL sizes have p50 135 KB, p90 3.3 MB, p95 7.3 MB.
- **Real MCP**: an HTTP MCP server with Brute bridge token semantics (per-invocation
  bearer token, revocation cancels in-flight calls, 401 after revoke) and 40 tools.
  Native tools `Read/Grep/Glob/LS` are enabled through `--tools`.
- 20 trials. Each trial runs both modes on fresh copies of the JSONL. The order
  alternates (spawn first on even trials, persistent first on odd ones). There are
  5 turns per arm, a new `Client` each turn, and the cursor is carried forward.
  Turn 3 calls an MCP tool once. The other turns reply `OK`. RSS is the sum over
  the CLI process tree, sampled every 100 ms.

TTFT and memory (ms / MB; n=20 per cell; nearest-rank p95 = 19th of 20):

| Turn | Mode | TTFT p50 | TTFT p95 | First line p50 | Init to text p50 | Peak RSS p50 | Idle RSS p50 |
|---|---|---:|---:|---:|---:|---:|---:|
| 2 | spawn | 1950 | 3203 | 555 | 1384 | 303 | 0 |
| 2 | persistent | 1458 | 1627 | 4 | 1451 | 293 | 293 |
| 5 | spawn | 1974 | 2257 | 566 | 1405 | 303 | 0 |
| 5 | persistent | 1422 | 2403 | 4 | 1418 | 300 | 300 |

- Paired per-trial TTFT difference (spawn minus persistent): turn 2 median 557 ms,
  persistent faster in 19/20 trials. Turn 5 median 542 ms, faster in 18/20. Turns 3
  and 4 show medians of 668 and 665 ms. Turn 1 is a cold start for both modes
  (median -28 ms). Splitting by order does not change the outcome. Turn 5 p95 is
  dominated by single network outliers in the 19th sample.
- The saving is almost entirely startup and resume (`first stream line` ~560 ms).
  `init -> text` is equivalent in both modes. With the 2.9 MB JSONL, startup is
  ~0.55 s, versus ~1.1 s for a fresh session in the earlier smoke run. That run was
  executed under different conditions, so the comparison cannot attribute the cost
  to JSONL parsing.
- Memory: a persistent process holds ~290-300 MB RSS for the whole idle window
  (10 min) and grows ~+7 MB over 5 turns. Spawn peaks at the same ~303 MB, but
  only for the duration of a turn. At benchmark time the pool was unbounded
  (N live sessions kept about N x 0.3 GB); it is now capped.
- MCP token lifecycle: spawn minted and revoked 5 tokens per arm (MCP
  `initialize` + `tools/list` 5 times). Persistent minted and revoked 1 token
  (handshake once). All 40 arms ended with 0 active tokens, 0 rejected requests,
  0 leaked processes (process tree gone ~37 ms after close), and exactly one
  execution of the turn-3 tool.

Cancellation and failure (`TestLivePersistentBenchCancellation`). The tool blocks
in MCP until the token is revoked:

| Case | Error | Tree gone | Held MCP call ended | Tool executions | Next turn |
|---|---|---:|---|---:|---|
| spawn cancel | `context canceled` | 34 ms | yes (revoke) | 1 | `cli_spawn` |
| persistent cancel | `context canceled` | 36 ms | yes (revoke) | 1 | `cli_persistent`, new process |
| persistent SIGKILL mid-tool | unsafe-for-retry | 37 ms | yes (revoke) | 1 | `cli_spawn` fallback |

The time from cancel to return is <= 0.43 s, which includes a 0.3 s measurement
pause. No mode replayed the interrupted tool.

Warmup probe (`TestLivePersistentBenchWarmupProbe`): the exact persistent command
line is started without stdin input. Model requests are counted through a local
`ANTHROPIC_BASE_URL` forwarding proxy, which the CLI honors with OAuth. The proxy is
used only for counting, because one proxied turn showed 31 s API time:

- Idle for 10-20 s: **0** `/v1/messages`, one `HEAD /api/hello`, no stdout. The
  MCP handshake (`initialize`, `tools/list`) happens **eagerly** before input, so
  a pre-spawn must mint its bridge token at spawn time. RSS is ~275 MB.
- First input on the pre-spawned process: first line 17 ms, TTFT 1165-1805 ms. This
  is equivalent to a reused turn, so a pre-spawn removes the ~0.55 s startup from
  the first turn.
- Closing stdin exits cleanly in ~0.5 s with no leaked processes.
- Model requests per turn: exactly 1 for both spawn and persistent over 3 turns.

Limitations: one machine and network, one model, and short `OK` replies, which
isolate transport overhead. Image inputs were not re-measured. Turn TTFT includes
the network. The production reuse rate is unknown: real per-turn `req.Tools` or
model changes replace the process (fingerprint). Brute logs did not yet contain
`Claude CLI turn timing` lines.

Assessment:

- **Bounded pool: needed before any default-on rollout** (implemented, default cap 4). The benefit (~0.55 s per
  reused turn, a much tighter p95 on turn 2) is real. However, each idle process
  pins ~0.3 GB for up to 10 minutes and the pool has no cap. Add an LRU cap
  (small default, env-configurable) that evicts the least recently used idle
  process, never a busy one. Re-check reuse rate and RSS from production timing
  logs.
- **Pre-spawn on focus/typing: not now.** It is technically safe (no model
  requests, clean exit, token revocable). However, the gain is limited to the
  first turn after a process is absent (new or reopened session, after idle
  eviction, after config change). It costs ~0.28 GB and a live MCP token per warm
  process. It is wasted whenever the eventual request fingerprint differs.
  Revisit only after the bounded pool lands. If pursued, use typing (not focus),
  debounce, at most one warm process, a short TTL (~60 s), only for sessions with
  a resume cursor, and counted against the pool cap. Warmup stays disabled by
  default.

Reproduce (spends real Claude quota; prepare a JSONL as described above):

```sh
cd brute
AAGENT_CLAUDE_BENCH_LIVE=1 AAGENT_CLAUDE_BENCH_JSONL=/tmp/long.jsonl \
  AAGENT_CLAUDE_BENCH_TRIALS=20 AAGENT_CLAUDE_BENCH_OUT_DIR=/tmp \
  go test -v ./internal/llm/claudecli -run '^TestLivePersistentBench' -count=1 -timeout 60m
```

The harness writes copies to `~/.claude/projects/<sanitized temp workdir>/` and
removes that directory on cleanup.

## Validation

```sh
cd brute
go test ./internal/llm/claudecli/...
just test  # go test -v -race ./...
just lint # go vet ./...
go build ./cmd/aagent
```

Critical offline coverage includes reuse across clients, bridge token lifetime,
idle eviction, LRU pool cap, prompt/tools/model/environment/permission/bridge/cursor changes,
image stdin, cancellation, malformed output, callback errors, safe pre-send
fallback, and propagation of response timings to assistant metadata.
