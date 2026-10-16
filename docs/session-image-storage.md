# File-backed session images

Image attachments are stored as original bytes in `sessions/<session-id>/images/<sha256>.<ext>` under the Brute data directory. SQLite `messages.metadata.images` and queued message images contain relative `path`, `media_type`, `width`, `height`, `sha256`, and optional `name` instead of base64. External HTTP(S) URLs remain URLs. New files are atomically published and synced before references are committed. Paths are confined with `os.OpenRoot`; LLM loads verify checksums. Session deletion cleans attachment files, including descendant sessions.

Session loads do not read attachment bytes. LLM request assembly reads retained images only; missing/corrupt attachments are logged and identified in request text. Caesar uses the existing image asset API, with browser lazy loading. Retry, duplication, linked continuations, Telegram, A2A forwarding, session-to-task conversion, and queued messages preserve images. Child sessions own copies so they do not depend on a deleted source session.

## Offline one-time migration

1. Finish active runs. Stop Brute **and its automatic reloader**. Do not run the migration against an actively written database.
2. Deploy the new Brute binary and Caesar build.
3. Ensure free disk space for a full SQLite backup, extracted originals, and SQLite VACUUM temporary files.
4. Run:

   ```sh
   brute session migrate-images --server-stopped
   # Custom data directory / recovery point:
   brute session migrate-images --server-stopped --data-dir /path/to/data --backup /path/to/new-backup.db
   ```

   Default data directory: `~/.local/share/aagent`. The command reserves a new mode-0600 backup and uses `VACUUM INTO` to capture committed WAL contents before modifying rows. Existing backup files are never overwritten. It extracts legacy transcript and queued attachments transactionally, preserves unrelated metadata, then VACUUMs and checkpoints the DB. Extraction failures roll back row changes; unreferenced immutable files are safe to retain and reuse on retry. A VACUUM failure after extraction is reported explicitly. Migration is idempotent, but each invocation requires a new backup path.
5. Restart Brute and Caesar. Open old image-bearing sessions, inspect images, retry/duplicate an image prompt, and check the asset requests load independently of the session JSON. Compare DB size and retain the recovery point until acceptance.

Backups must now include **both `aagent.db` and `sessions/`**, taken consistently with writers stopped (or equivalent snapshot tooling). SQLite alone no longer includes attachment bytes. Existing JSONL files are not rewritten by this migration and may still contain historical inline payloads.

To roll back, stop Brute and the reloader, close all DB connections, preserve the current database and WAL/SHM files as a separate recovery set, then restore the backup as `aagent.db` without reusing stale WAL/SHM files. Keep the extracted `sessions/` directory until rollback is verified. Do not delete the only backup or overwrite a running database.

## Verification in this change

- Save/load/database roundtrip, checksum/corruption/missing files, traversal/symlinks, linked-session copies, queued attachments, unpadded base64, retry references, cascading deletion, HTTP image response, LLM hydration, migration rollback/backup/idempotency, queued migration, and database shrink tests pass.
- Full Caesar suite: 398 files, 2435 tests passed. Brute and Caesar builds and `go vet ./...` passed.
- Shared-worktree blockers: unrelated `TestJevEffortStickyAndUserOverride` failure; unrelated Caesar lint errors in `src/pages/projects/pages/ProjectViewRouteContext.tsx` and `src/pages/projects/projectRenderProfiler.tsx` (task T-185).
- A consistent **copy**, not the active database, was migrated: 477 messages; 2,489,511,936 -> 1,250,357,248 bytes. All 506 attachments (758,001,548 original bytes) matched the backup byte-for-byte; no inline attachment fields remained; SQLite integrity check passed.
- Live migration and manual inspection of old sessions in Caesar remain pending (T-184). Private verification data is under `brute/tmp/images-migration-verification/data`; remove it after acceptance.
