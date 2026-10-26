# Docker CLI MCP relay

Docker agents may lack host CLI providers. Their explicitly configured parent-proxy LM Studio client opens an outbound WebSocket at `/v1/mcp-relay` before each session-scoped completion. The parent runs the selected CLI provider, while every A2gent tool executes in the owning child.

## Trust boundaries

- Each newly created proxy-enabled Docker runtime receives a random 256-bit bootstrap credential, prefixed by its container name. Docker stores only its SHA-256 digest in the `a2gent.mcp_relay_key` label. The child consumes `A2GENT_PARENT_MCP_KEY` into server memory and unsets it before launching tools. It never receives the parent's provider credentials or Claude profiles.
- Relay authentication checks the credential against the currently running, managed Docker container. Replacing the container rotates the credential; stopping it or restarting the parent terminates its live channels. Docker access itself remains trusted host-administrator access.
- The child initiates the connection, with its session ID. The parent returns an unpredictable invocation capability. A completion must present that capability, the same runtime credential, and the same child session ID. It is claimable once. Runtime state and credential rotation are checked again on claim. Private relay headers are opt-in client configuration, never inferred from a provider URL or forwarded upstream to model APIs.
- The parent creates a separate opaque invocation identity, not a parent session and not a lookup of the child's ID in the parent database. Its existing loopback-only MCP endpoint receives a different invocation-scoped bearer token. Requests with that token go through the reverse channel, not the parent's tool manager.
- Child dispatch uses its existing session-scoped tool manager, project context, execution context, progress events, delegation restrictions, and approval broker. File/shell tools are exposed through the relay only, so they execute inside Docker. Compound tools and MCP-through-MCP remain excluded; qualified-name aliases cannot bypass exclusions.
- Claude runs in a fresh temporary directory with native tools disabled, strict MCP configuration, project/user settings and slash commands disabled, and no persistence, resume or sidecar. Cursor runs in a fresh temporary workspace and a fresh `CURSOR_CONFIG_DIR` with allowlist mode, native shell/file/web tools denied, the workspace sandbox enabled, and only the A2gent child MCP plugin allowed. It does not approve other configured MCP servers or resume a host-side chat. These restrictions follow provider fallback nodes through the invocation context. Local host-runtime CLI sessions keep their original behavior.
- CLI proxy calls without an authenticated relay fail closed, including CLI nodes in fallback chains. Kimi CLI proxy execution remains rejected because it does not yet implement equivalent confinement. API-only proxy requests retain their existing behavior.

## Lifetime and cancellation

Each completion owns one reverse connection. Completion exit, cancellation, child disconnect, or the ten-minute proxy deadline cancels the parent invocation, revokes its MCP token, and cancels child calls and pending approvals. Individual callback cancellation/revocation sends a cancellation frame without revoking unrelated callbacks. Already-started side effects are best-effort cancellation, not rolled back. Frames are capped at 4 MiB and each invocation permits up to 32 concurrent callbacks. The parent caps live relays at 32 globally and four per runtime, and allows at most four concurrent Docker authentication checks. There is no callback URL, externally exposed child MCP listener, or reconnect/replay of a partially executed completion.

The existing `/mcp/sessions/{id}` endpoint is still loopback-only and requires its own session-bound bearer token. The reverse WebSocket is a separate authenticated protocol. HTTP/WS is intended for the existing local Docker-to-host network; a remote parent must use HTTPS/WSS.

Existing warm containers need recreation with an updated Brute image to receive the new bootstrap credential and client implementation. Do not work around missing credentials by exposing the child MCP listener or forwarding a parent session ID.

## Verification

Normal regression coverage includes runtime/session/capability mismatch, one-shot claim, stopped/rotated credentials, loopback token isolation, disabled tools, owning project/session context, approvals, disconnect cancellation, callback revocation, Claude and Cursor CLI confinement, and opt-in request-header cleanup.

```sh
go test ./internal/http ./internal/llm/claudecli ./internal/llm/cursorcli ./internal/llm/lmstudio -run 'Test(MCPRelay|MCPBridge|Host|MCPOnly|PrepareRequest)'
AAGENT_DOCKER_CLAUDE_MCP_LIVE=1 go test ./internal/http -run '^TestLiveDockerClaudeMCP$' -count=1 -v -timeout=7m
AAGENT_CLAUDE_MCP_LIVE=1 go test ./internal/http -run '^TestLiveHostClaudeMCP$' -count=1 -v -timeout=5m
```

The Claude Docker live test cross-compiles the current HTTP test binary for Linux/arm64, mounts it into `a2gent-brute:latest` (without Claude), and verifies a real parent Claude CLI handshake/tool call returning the child's exact session marker. Claude live checks consume subscription quota and are opt-in.

### Cursor Agent CLI: opt-in Docker round trip

Run commands from `brute/`. This check makes **two real Cursor completions** (Chat and ChatStream) and consumes Cursor account quota. It is skipped by default, including in normal CI; do not enable the gate globally or run it in parallel with other quota-sensitive checks.

Prerequisites:

- A running Docker daemon and a local `a2gent-brute:latest` image for `linux/arm64`, without an `agent` executable. The test cross-compiles the current source, so the image only supplies the child runtime. Build it when needed: `docker build --platform linux/arm64 -t a2gent-brute:latest .`.
- Go with Linux cross-compilation support and a host Cursor Agent CLI supporting plugins, allowlist permissions and the workspace sandbox. `agent status` should confirm login, or supply `CURSOR_API_KEY` through the environment. `AAGENT_CURSOR_CLI_PATH` can select another executable.
- Docker-to-host access through `host.docker.internal`. The test starts an ephemeral host listener; no persistent server or public child MCP port is needed.

```sh
# Quota-free regression coverage, including a valid existing-chat resume control:
go test ./internal/http ./internal/llm/cursorcli ./internal/llm/claudecli ./internal/llm/lmstudio \
  -run 'Test(MCPRelay|MCPBridge|Host|MCPOnly|PrepareRequest|LiveDockerCursorMCP)' -count=1

# Explicitly opt in, without Go's successful-test cache:
AAGENT_DOCKER_CURSOR_MCP_LIVE=1 go test ./internal/http \
  -run '^TestLiveDockerCursorMCP$' -count=1 -v -timeout=7m

# Optional: select a model available to this account (default: composer-2.5).
AAGENT_DOCKER_CURSOR_MCP_LIVE=1 AAGENT_DOCKER_CURSOR_MCP_MODEL=composer-2.5 \
  go test ./internal/http -run '^TestLiveDockerCursorMCP$' -count=1 -v -timeout=7m
```

The live test uses real Docker label authentication and the real parent HTTP proxy, not a mocked relay. For each completion it checks that:

- The child has no Cursor executable and obtains an HTTP proxy client. Exactly one MCP probe runs with the owning **child session and project**, and the model returns the probe's fresh unpredictable marker, which was never in the prompt. A parent probe is a failure, and no parent A2gent session may be created.
- A test-only auditing wrapper snapshots the actual Cursor subprocess argv/configuration and then `exec`s the real CLI. Both invocation paths use separate disposable workspaces/config directories, the workspace sandbox, exact native shell/read/write/web denies, and only the child A2gent MCP allowlist/plugin. They must not pass `--force`, blanket `--approve-mcps`, `--resume`, or `--continue`.
- An unrelated MCP canary is configured in a disposable parent project, config directory and `HOME/.cursor/mcp.json`. A quota-free `agent mcp list` control confirms it is discoverable, but the relay must never contact it. The real user's MCP files/chats are not modified. Login tokens, when needed, are copied only into private temporary auth files and never passed into Docker or printed.
- Temporary relay workspaces and plugin directories have been removed after completion. Separate quota-free Cursor subprocess tests supply an existing, scope-valid synthetic chat cursor and prove that MCP-only Chat and ChatStream still start fresh; this prevents a missing/mismatched chat from making the no-resume assertion pass accidentally.

These are subprocess-policy, routing and canary checks, not a general proof that the Cursor sandbox is escape-proof. Cursor's project `.cursor/cli.json` must omit `version`; it belongs only in the global `cli-config.json` schema. The live check catches installed-CLI schema incompatibilities that a fake executable cannot detect.

Cleanup runs on success and ordinary test failure: the uniquely named `t237-mcp-*` container is force-removed, the HTTP listener/stores are closed, and temporary test binaries, workspaces, plugin bearer-token snapshots and auth/config fixtures are removed. The wrapper uses `exec` so cancellation targets the real CLI rather than leaving a wrapper child behind. An external `SIGKILL` or Go's hard test timeout can prevent cleanup; remove only the container named in that run's Docker diagnostics (`docker rm -f <exact-container-name>`) and inspect temporary `TestLiveDockerCursorMCP*`, `brute-mcp-relay-*` and `a2gent-cursor-mcp-*` directories before removing them. Do not delete another running session's resources.

A missing gate or missing credentials is reported as `SKIP`, not a successful live round trip. Once opted in with credentials, CLI, Docker, schema, authentication or model failures are test failures, with no automatic quota-consuming retries.
