# Relevance gate

`relevance_gate` reduces candidate file context using the existing Jev SystemOne client. It does not alter the router or intercept ordinary reads.

```json
{"paths":["internal/tools/filter.go","docs/notes.md",".env"],"task_context":"Fix filtering of empty lines"}
```

Alternatively, `input` accepts an array of paths or search-result objects with a `path` field, a JSON-encoded array, or native newline-delimited `file_search` / `content_search` output passed by `pipeline`. Other object fields (including snippets) are ignored. Supply exactly one of `paths` or `input`.

Each input receives an ordered JSON verdict with `path`, `action`, `reason`, and optional `confidence`, `content`, or `error`. Duplicate paths remain separate. Empty input returns `[]`.

| Confidence | Behavior |
| --- | --- |
| >= 0.85 | Apply Jev's include / summarize / skip choice |
| >= 0.60 and < 0.85 | Summarize using a filtered head/tail preview, regardless of choice |
| < 0.60, invalid/missing answer, API failure, or no credentials | Include the full file locally |

Summaries preserve both ends, capped at 4 KiB, and pass through the filter tool. They are excerpts, not generated prose. Classifier previews never cap full inclusion. Every skip is explicit with its original path, allowing the agent to override with `read` (and `force: true` when applicable). Unreadable/non-text files get an `include` verdict with an error, never an implicit omission.

Requests batch up to 10 choice questions with original-index IDs (`file_0`, etc.), bounded shared state of 64 KiB, 4 KiB previews and 30-second timeout per batch. Larger inputs split into bounded batches. Task context is capped by retaining both ends. File contents are marked untrusted data. Credentials resolve through the existing classify provider/environment/integration precedence.

## Secrets and disabling

Secret matching is case-insensitive and checks supplied paths and resolved symlink targets **before reading**. Excluded names include `.env*`, SSH private-key names, `keys`, `.ssh`, `.gnupg`, `secrets`, and `credentials` path components, plus `.key`, `.pem`, `.p12`, `.pfx`, and `.keystore` suffixes. These paths return `skip` with a secret reason and never reach Jev, even when credentials are absent. Search snippets are not forwarded. On macOS/Linux, descriptor-relative no-follow traversal prevents a checked file or parent directory from being replaced with a secret symlink before opening. Other platforms return explicit per-file errors rather than risking a leak. This is a conservative filename policy, not content-based secret detection: task context and ordinary-named files must not contain credentials.

Set `tools.relevance_gate_disabled` to `true` in the Brute JSON configuration to unregister this tool. Default is enabled. This does not disable `classify` or change ordinary `read`; secret exclusions cannot be turned off inside the gate.

Validation: `go test ./internal/tools ./internal/http -run 'TestRelevanceGate'`, `go build ./...`, `go test -race ./...`, and `go vet ./...` (`just lint`).
rnal/http -run 'TestRelevanceGate'`, `go build ./...`, `go test -race ./...`, and `go vet ./...` (`just lint`).
