# Context Compression MVP

## Motivation
When agents interact with large codebases or execute shell commands, they often generate massive amounts of text output (e.g., from `grep`, `bash`, `find_files`, `content_search`). 
Passing these raw, unbounded tool results directly to the LLM has several negative effects:
1. **Context Overflow**: Large outputs quickly consume the LLM's finite context window.
2. **Cost & Latency**: Sending massive payloads increases inference time and token costs.
3. **Distraction**: LLMs can get "lost in the middle" of huge logs, missing critical instructions or error signals.

Previously, relying on external services (like Headroom) added latency and external dependencies. This Context Compression MVP provides a built-in, lightweight, and optional mechanism to replace massive tool outputs with deterministic, compact summaries and markers, while storing the full originals locally so the LLM can retrieve them if actually needed.

## Core Mechanics

### 1. Selective Compression
Compression is only applied to specific, high-volume tools. 
- **Preserved by default**: Tools like `read`, `write`, and `edit` are **never** compressed. Precise formatting, trailing whitespaces, and newlines are essential for these operations to function correctly and remain predictable for the LLM. 
- **Summarized**: Search tools (`grep`, `content_search`, `file_search`) are compressed into summaries retaining only unique file paths and line ranges. `bash` executions and logs retain error hints, exit codes, and truncated boundaries.

### 2. Session-Scoped Context Compression Registry (CCR)
When a large tool result is compressed, the original text is saved in a session-scoped CCR store.
- **In-Memory & Persisted**: The CCR caches entries in process memory during active reasoning. Durable originals live in SQLite `session_compression_entries(session_id, hash, payload, created_at)`, never in session metadata. Admission (including command previews and Jev) and request-time compression upsert individual entries; identical payloads do not dirty WAL pages.
- **Resilience**: Originals survive HTTP request boundaries, agent instantiations, and server restarts. If persistence fails, the original result is kept instead of replacing it with a retrieval marker.
- **Lazy reads**: Only a `contextcompress` cache miss fetches a single entry by session ID and hash. Full/display/list/live session reads never load CCR payloads. Deleting a session cascades to its entries.
- **Migration**: At startup, table creation, extraction of legacy `context_compression_store` entries, and removal of the metadata key commit atomically. The table is the migration marker; later startups skip extraction. Invalid JSON metadata is left untouched. Back up the database before deployment; the migration can temporarily grow the database/WAL. It does not run `VACUUM` or shrink the database file automatically.

### 3. The `context_retrieve` Tool
When compression is enabled, a new internal tool `context_retrieve` is dynamically injected into the agent's available tools.
- If the LLM sees a compressed marker (e.g., `[COMPRESSED: hash123]`) and decides it needs the full context, it can call `context_retrieve` with that hash.
- The tool supports an optional `query` parameter (case-insensitive substring), allowing the LLM to filter the massive original text on the server side instead of loading the entire payload into its context window.

### 4. Provider Integrity
To ensure no subtle bugs are introduced during tool result formatting, provider adapters (e.g., `openaicodex`, `claudecli`) were updated. 
- Stripping and trimming (like `strings.TrimSpace`) of `tool_result` output was removed to guarantee that exact file contents (e.g., from `read`) are relayed precisely to the LLM. 
- The exact structure, order, and `ToolCallID` mappings of the tool messages are preserved.

## Configuration
The compression feature is enabled by default.
You can disable it explicitly with:
```bash
A2GENT_TOOL_RESULT_COMPRESSION_ENABLED=false
```
The web UI also exposes this setting and defaults it to enabled for agents that do not yet have an explicit saved value.

## SaveSession regression benchmark

Run from `brute/`:

```sh
go test ./internal/storage -run '^$' -bench '^BenchmarkSaveSessionLargeCompression$' -benchtime=3x -count=1
```

Synthetic session with 50 MiB of compression originals, no messages, and a changing
`updated_at`. Auto-checkpoint is disabled and WAL is truncated before timing three
saves. The post-change setup also seeds the separate compression table outside the
timed section. On darwin/arm64 (Apple M5 Pro):

| Metric | Before | After |
| --- | ---: | ---: |
| SaveSession time | 116.40 ms/op | 0.113 ms/op |
| WAL growth | 35,249,357 B/op | 20,611 B/op |
| Go allocations | 122,361,861 B/op | 2,874 B/op |

These isolate compression persistence, not full production transcript costs.
`system_prompt_snapshot` remains in metadata (and the display projection); moving
it requires explicit lazy reads for prompt composition and HTTP snapshot details,
while preserving frozen prompt prefixes. This follow-up is tracked as T-176.
