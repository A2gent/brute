# Claude Native Tool Permissions

Per-tool approval for Claude Code when Brute runs the CLI as the tool executor. Verified against **Claude Code CLI 2.1.215** (bundled via `@anthropic-ai/claude-agent-sdk` 0.3.215).

## Why not raw `stream-json`?

The Claude CLI `--output-format stream-json` wire protocol includes `control_request` / `control_response` frames (e.g. `can_use_tool`). That transport is **internal to the Agent SDK ↔ CLI bridge**, version-coupled, and **not a supported public CLI callback API**. Brute does not parse those frames directly.

## Why an optional Agent SDK sidecar?

The public `claude` binary has no stable host hook for `canUseTool`. The sidecar (`sidecars/claude-agent-sdk/index.mjs`) is a thin Node process that:

1. Calls `@anthropic-ai/claude-agent-sdk` `query()` with `canUseTool` and a mandatory `PreToolUse` hook (`permissionDecision: ask`).
2. Speaks a small Brute-owned NDJSON protocol (`run_request`, `permission_request`, `permission_response`) over stdin/stdout.

This is **opt-in**. Without it, Brute keeps the legacy direct-CLI path.

## Default vs opt-in

| Mode | Trigger | Permission behavior |
|------|---------|---------------------|
| **Legacy (default)** | `AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH` unset | `claude --permission-mode acceptEdits` (or `AAGENT_CLAUDE_CLI_PERMISSION_MODE`) — auto-allows edits; no Caesar prompts |
| **Native approvals** | Sidecar path set **and** HTTP approval broker active | Every tool goes through user approval; `acceptEdits` / `bypassPermissions` / `auto` are stripped |

## Architecture flow

```
Claude SDK PreToolUse (ask)
        ↓
SDK canUseTool callback (sidecar)
        ↓
permission_request (NDJSON stdout)
        ↓
Brute claudecli transport → approval.Broker.Request
        ↓
HTTP GET /sessions/{id}/approval  +  SSE permission_required
        ↓
Caesar UI (user decides)
        ↓
HTTP POST /sessions/{id}/approval/{requestId}
        ↓
Broker.Resolve → permission_response (NDJSON stdin)
        ↓
SDK executes tool natively (same callback path)
```

**No A2gent re-execution:** after approval, the Claude Agent SDK runs the tool inside the CLI process. Brute does not re-invoke A2gent tools for that call.

## Decisions

| Decision | Effect |
|----------|--------|
| `allow_once` | Allow this tool call only |
| `allow_session` | Cache allow for same session + tool name; skipped for `AskUserQuestion` |
| `deny` | Return `behavior: deny` to SDK |
| `AskUserQuestion` | Kind `question`; POST may include `answers` or `message`; `allow_session` rejected |
| Audit | Events appended to `session.metadata.approval_audit`; broker in-memory audit log |

## Timeout and cancel

- Default approval wait: **5 minutes** (`approval.DefaultLimits().DefaultTimeout`).
- Override: `AAGENT_CLAUDE_AGENT_SDK_APPROVAL_TIMEOUT` (Go duration, e.g. `2m`).
- **Timeout** → broker returns deny with `permission request timed out`; SSE `permission_resolved` / `timed_out`.
- **Context cancel** (session stopped) → deny with `permission request cancelled`; SSE `cancelled`.
- Sidecar abort (SIGTERM, stdin close) → pending `canUseTool` promises reject as deny.

## Configuration

```bash
# Enable native per-tool approvals (required)
export AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH=/path/to/brute/sidecars/claude-agent-sdk/index.mjs

# Optional: node binary (default: `node` on PATH)
export AAGENT_CLAUDE_AGENT_SDK_NODE_PATH=/usr/local/bin/node

# Optional: approval wait timeout (default 5m)
export AAGENT_CLAUDE_AGENT_SDK_APPROVAL_TIMEOUT=5m
```

### Sidecar setup

```bash
cd sidecars/claude-agent-sdk
npm ci          # installs @anthropic-ai/claude-agent-sdk@0.3.215
node --version  # must be >= 20
```

## HTTP API (Caesar)

- `GET /sessions/{sessionID}/approval` — returns `{ "approval": <pending> | null }` (first pending only).
- `POST /sessions/{sessionID}/approval/{requestID}` — body: `{ "decision": "allow_once"|"allow_session"|"deny", "answers": {...}, "message": "..." }`.

Stream events: `permission_required`, `permission_resolved`.

## Security model and limitations

- **Human-in-the-loop:** every native tool (except session-cached allows) requires an explicit Caesar decision before SDK execution.
- **In-memory state:** pending requests and `allow_session` grants live in the process broker; **lost on Brute restart**.
- **Queued UI:** `GET .../approval` exposes the first pending request; live stream events queue additional concurrent requests in Caesar.
- **Explicit deny rules win:** Claude Code deny rules / auto-deny paths short-circuit before `canUseTool`; Brute cannot override SDK-side denials. `PreToolUse` hook denies also bypass the broker.
- **Stripped auto-approve knobs:** sidecar removes `allowedTools` and forbids `acceptEdits` / `bypassPermissions` / `auto` so tools cannot skip the broker.
- **Session binding:** resolve rejects requests whose `sessionID` does not match.
- **Input cap:** permission payloads limited to 64 KiB; max 64 concurrent pending approvals (broker defaults).

## Tests

```bash
# Broker unit tests
go test ./internal/approval/...

# HTTP approval handlers
go test ./internal/http/... -run Approval

# Sidecar transport integration (Go)
go test ./internal/llm/claudecli/... -run Sidecar

# Sidecar protocol + behavior (Node)
cd sidecars/claude-agent-sdk && npm ci && npm test

# Full suite
just test
```
