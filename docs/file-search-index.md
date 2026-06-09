# File search index

## Purpose

Brute provides a fast, per-project file search index for both Caesar UI search and agent tools. The index replaces repeated filesystem walks for common search flows and is designed to keep hot searches comfortably below interactive UI latency targets.

## Components

### `internal/filesearch`

Core implementation lives in `internal/filesearch`:

- `Build(ctx, root, opts)` walks a project folder once and returns an immutable `Index`.
- `Index.Search(req)` runs in-memory path/content lookup.
- `Manager` caches indexes by absolute project root.
- `DefaultManager()` exposes the process-wide cache used by HTTP handlers and tools.

### HTTP integration

`internal/http/handlers_project_search.go` serves Caesar's `GET /projects/search` endpoint through `filesearch.DefaultManager()`.

Query parameters:

- `projectID` — required project id.
- `query` — required search text.
- `mode` — optional. `files` disables content search and returns only filename/path matches.

The response keeps the existing Caesar contract:

- `filename_matches[]`
- `content_matches[]`
- `root_folder`
- `query`

### Agent tools

Registered in `internal/tools/manager.go`:

- `file_search`
  - Fast fuzzy search over file names and paths.
  - Prefer when the agent knows part of a filename/path.
- `content_search`
  - Fast indexed literal, case-insensitive content search.
  - Prefer when regex is not needed.
  - Use `grep` for regular expressions.

## Indexing model

The index is in-memory and process-local. There is no disk index.

For each indexed project:

- path records are stored for searchable files;
- text content is stored for UTF-8 files within limits;
- content trigram postings narrow candidate files before exact substring verification;
- file path search scans indexed path records and ranks exact, prefix, substring and small-fuzzy matches.

## Exclusions

Default directory exclusions keep dependency/generated folders out of the index:

- VCS: `.git`, `.hg`, `.svn`
- dependencies/build output: `node_modules`, `vendor`, `dist`, `build`, `coverage`, `target`, `out`
- framework/cache folders: `.next`, `.nuxt`, `.turbo`, `.pnpm-store`, `.cache`

Hidden files/folders are skipped unless explicitly enabled in `filesearch.Options`.

## Memory and CPU limits

Defaults:

- process cache budget: **1 GiB** (`DefaultMaxMemoryBytes`)
- indexed content budget per project: **256 MiB** (`DefaultMaxContentBytes`)
- max content-indexed file size: **512 KiB** (`DefaultMaxFileBytes`)
- max file lines: **20,000**

`ManagerOptions.MaxMemoryBytes` bounds total cached indexes. `Options.MaxIndexBytes` bounds a single build using conservative incremental accounting. If limits are reached, paths continue to be indexed where possible, content indexing is truncated, and `Stats.Truncated` is set.

## Freshness strategy

Brute intentionally avoids filesystem watchers to keep CPU, file handles and cross-platform complexity low.

Freshness is maintained by:

1. **Warm-up**: `Manager.Warm(root)` starts a background build when Caesar opens a project tree.
2. **Hot cache reuse**: `Manager.Search(...)` returns the existing immutable index when available.
3. **Lazy rebuild**: stale indexes are rebuilt in the background after `DefaultStaleAfter`.
4. **Explicit invalidation**: known Brute/Caesar file mutations call `Manager.Invalidate(root)`.

Internal invalidation happens after:

- Caesar project file save/delete/move/rename/create-folder operations;
- agent-side `write`, `edit`, `replace_lines`, and `insert_lines` tools.

External filesystem edits are picked up by lazy stale rebuild or by the next rebuild after invalidation.

## Ranking behavior

Path search ranking favors:

1. exact full path match;
2. exact basename/stem match;
3. basename prefix match;
4. basename substring match;
5. path prefix/substring match;
6. compact/fuzzy match for small typos.

Content search is literal and case-insensitive. It returns the first matching line preview per matching file and ranks earlier lines/columns higher.

## Performance notes

On the Caesar project during implementation validation:

- cold build: about **172 ms**;
- hot combined search: about **15 ms**;
- indexed files: **491**;
- indexed content files: **437**;
- approximate index size: about **12 MB**.

## Tests

Critical behavior is covered in:

- `internal/filesearch/index_test.go`
- `internal/tools/indexed_search_test.go`
- `internal/http/mind_project_file_test.go`

Recommended focused check:

```bash
go test ./internal/filesearch ./internal/tools ./internal/http
```

Recommended wider check, avoiding the known root-package layout issue:

```bash
go test ./cmd/aagent ./internal/...
```
