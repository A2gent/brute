# Claude Runtime Observability

Brute can surface and persist structured runtime telemetry from Anthropic Claude CLI streaming (native tool lifecycle, cost, warnings, and optional reasoning).

## Streaming events

Each normal or finalization LLM call receives a UUID `runtime_turn_id`. HTTP chat stream events include `turn_id` on:

- `assistant_delta`
- `reasoning_delta`
- native runtime tool lifecycle events (`tool_started`, `tool_updated`, `tool_input_completed`, `tool_completed`, `tool_output`)
- `cost`
- `runtime_warning`

Native Claude runtime tools are **not** executed by Brute and are **not** added to transcript `tool_calls` / tool results.

## Persisted assistant metadata

Assistant messages store diagnostic metadata alongside existing LLM timing fields:

| Key | Description |
|-----|-------------|
| `runtime_turn_id` | UUID for the LLM call |
| `runtime_native` | Present when native runtime tools were observed |
| `runtime_tools` | Upserted native tool lifecycle/output snapshots |
| `runtime_cost` | Latest cost/duration payload |
| `runtime_warnings` | Deduped runtime warnings (capped) |
| `runtime_reasoning` | Opt-in persisted reasoning text |
| `runtime_reasoning_truncated` | Set when reasoning was capped |

On LLM error, an empty assistant turn with metadata is persisted only when the accumulator captured durable payload beyond `runtime_turn_id`. Empty final-response retries persist an empty assistant turn first so every runtime turn remains durable.

## Privacy: reasoning persistence

Reasoning text is **off by default**. Enable with either:

- Environment: `A2GENT_CLAUDE_RUNTIME_REASONING_PERSISTENCE_ENABLED=true`
- SQLite app setting: same key set to `true`

Resolution order: explicit non-empty environment value wins, else SQLite settings, else `false`. The flag applies only when the active provider type is `anthropic`.

## Limits

- Reasoning: 32 KiB UTF-8
- Native tool output: 64 KiB UTF-8
- Runtime warnings: 100 unique entries
