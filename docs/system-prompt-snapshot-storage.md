# System prompt snapshot storage (T-176)

Frozen prompts live in `session_system_prompt_snapshots(session_id, payload)`.
The session ID is a foreign key with delete cascade. Compression originals remain
in their independent `session_compression_entries` table.

## Persistence and migration

- Startup creates the snapshot table, extracts legacy objects, and removes both
  `sessions.metadata.system_prompt_snapshot` and its `display_metadata` copy in
  one transaction. The table itself is the migration marker. Any failure rolls
  back the table, extracted rows, and removals; restarting retries safely.
- Full metadata wins over a stale display copy. Display-only snapshots are also
  recovered. Malformed JSON is retained rather than destroying unrelated state.
  Migration does not compose prompts or change their string contents.
- `SaveSystemPromptSnapshot` writes only if the serialized payload changed.
  `SaveSession` and incremental saves filter the legacy key before encoding and
  never overwrite/delete a snapshot, even when given stale display metadata.
- Linked provider transitions use `SaveWithSystemPromptSnapshotFrom` to copy the
  prefix via SQL in the session-save transaction. If the source has a snapshot,
  observers cannot see a queued child before its prefix is durable. Sources that
  never materialized a snapshot retain their existing no-snapshot behavior.
  Replays do not overwrite an existing child snapshot, and deleting the source
  does not affect the independent copy.

## Lazy reads and compatibility

Ordinary full, summary, display, list, and live reads do not fetch snapshots.
Prompt composition and HTTP details with messages explicitly hydrate them.
The detail API retains both the top-level `system_prompt_snapshot` payload and
`metadata.system_prompt_snapshot`; `include_metadata=false` still leaves the
former intact. Lightweight details (`include_messages=false`) do not hydrate it.
Creating/regenerating a snapshot from a detail read writes only its own table,
never a partial transcript, session status, metadata, or `updated_at`. Read, decode,
or persistence errors stop prompt preparation and provider execution; a detail
read returns HTTP 500 instead of omitting or silently rebuilding a failed prefix.

Existing environment blocks, including legacy `Current time` strings, remain
frozen. A2A sub-agent prompts use the same persisted snapshot path. The existing
identity/configured-agent invalidation rules remain unchanged.

## SaveSession benchmark

```sh
go test ./internal/storage -run '^$' -bench '^BenchmarkSaveSessionSystemPromptSnapshot$' -benchtime=10x -count=3
```

Synthetic sessions with no messages and a changing `updated_at`. Snapshot payloads
are seeded outside the timed section; the caller retains a legacy metadata copy
to verify that even these values are not encoded on normal saves. Auto-checkpoint
is disabled and WAL truncated before timing. Medians of three runs on
darwin/arm64, Apple M5 Pro:

| Snapshot size | Metric | Before | After |
| --- | --- | ---: | ---: |
| 64 KiB | SaveSession | 0.265 ms/op | 0.062 ms/op |
| 64 KiB | WAL | 48,619 B/op | 20,603 B/op |
| 64 KiB | Allocations | 77,555 B/op | 3,529 B/op |
| 1 MiB | SaveSession | 3.970 ms/op | 0.063 ms/op |
| 1 MiB | WAL | 867,675 B/op | 20,603 B/op |
| 1 MiB | Allocations | 1,271,796 B/op | 3,417 B/op |

Unchanged standalone snapshot writes produce zero WAL bytes (covered by tests).
These measurements isolate snapshot persistence, not production transcript costs;
WAL frame counts can vary with timestamp encoding and SQLite overflow-page reuse.
