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

The Docker live test cross-compiles the current HTTP test binary for Linux/arm64, mounts it into `a2gent-brute:latest` (without Claude), and verifies a real parent Claude CLI handshake/tool call returning the child's exact session marker. Live checks consume Claude subscription quota and are opt-in.
