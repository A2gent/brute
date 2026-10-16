# Session storage maintenance

## WAL retention

Every SQLite connection sets `journal_size_limit(67108864)` (64 MiB).
Bulk session metadata migrations run `wal_checkpoint(TRUNCATE)` after commit,
before the application's reader pool opens. Normal SQLite auto-checkpointing
remains enabled.

The limit controls retained WAL after checkpoint/reset, not the size of an
active transaction. Long-lived readers or a second process can pin WAL frames
and temporarily exceed it. Do not delete `aagent.db-wal` or `aagent.db-shm`.

On first startup, a transactional metadata-compaction migration:

- clears the redundant `display_metadata` column; display reads use `metadata`;
- keeps the last 20 provider failures, truncating string fields to 2048 UTF-8
  bytes including the truncation marker;
- checkpoints and truncates the WAL after the rewrite.

The legacy column remains only for compatibility with earlier extraction
migrations. Ordinary saves do not populate it. Subsequent startups skip the
compaction migration and do not acquire a write lock for it.

## Reclaiming free database pages

Checkpointing releases WAL space, not free pages in `aagent.db`. Freed pages
remain reusable by subsequent writes. We deliberately do not automatically
`VACUUM` multi-gigabyte databases on startup: it blocks writes and requires
additional disk space. Enabling incremental auto-vacuum for an existing
non-auto-vacuum database would itself require a full `VACUUM` first.

For a one-off compaction, during an idle maintenance window:

1. Stop Brute cleanly and confirm no other process has the database open.
2. Take a consistent backup, for example with SQLite's `.backup` command.
3. Check free disk space (allow at least twice the current database size).
4. Run, using the actual data directory:

   ```sh
   sqlite3 "$DATA_DIR/aagent.db" 'PRAGMA busy_timeout=30000; PRAGMA wal_checkpoint(TRUNCATE); VACUUM;'
   ```

5. Restart Brute with the updated binary and observe `aagent.db-wal` during a
   representative run. In the absence of pinned readers or large transactions,
   expect it below 64 MiB. Check `PRAGMA freelist_count` and database/WAL sizes.

Do not force a restart while sessions are running. The development reloader
waits for active and about-to-start sessions to finish before restarting.

## Regression checks

```sh
go test -race ./internal/storage/... ./internal/session/... ./internal/http/...
go test ./...
go build ./...
go vet ./...
```

`TestMigrationCheckpointAndWALRetentionAfterRestart` grows a real WAL beyond
64 MiB, keeps another connection open across restart, runs migration cleanup,
and measures WAL retention during resumed writes. Catalog tests cover both
message-metadata-only writes and the archived-session analysis tool.
