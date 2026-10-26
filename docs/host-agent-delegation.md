# Host agent discovery and on-demand delegation

Local agent definitions can use `runtime.type: host` or `runtime.type: docker`.
Both start on the first delegation and reuse a healthy warm runtime. Stopped
agents are still discoverable; they do not have to run permanently.

## Discovery and scope

The main system prompt and `list_agents` share the same configured-agent catalog:

- Global and current-project YAML definition folders.
- Imported definitions stored in the application database.
- Legacy saved sub-agents.

Imported definitions take precedence over legacy records with the same ID, then
on-disk definitions. Project visibility is checked against the selected definition,
not a lower-priority duplicate. Use the canonical definition ID from `list_agents`.
Raw Docker container aliases must also pass definition and mounted-workspace checks.

## Direct host sessions

A host runtime keeps its own database. A user chatting directly with a host agent
must therefore use the main application's catalog rather than that isolated copy.
For each direct host turn, the parent issues a session-bound, cancellable MCP
capability allowing only `list_agents`, `delegate_to_agent`, and
`delegate_to_subagent`. The child forwards these tools to the parent; other tools
remain local. Both runtimes' disabled-tool settings still apply.

The parent origin is provisioned at process startup and must be numeric loopback.
The capability travels in an internal stream header, stays in memory, and is revoked
when the turn ends or is cancelled. It is not saved in session metadata, prompts,
or environment variables. Delegated sessions cannot acquire this capability or
delegate again. Host processes are trusted native processes, not a sandbox.

Warm host processes with a missing or outdated parent origin are restarted lazily
before reuse. Existing main-session prompt snapshots using the old running-only
catalog are migrated once; subsequent turns retain a stable cached prefix.
