# Playbook

- Before delegating repository analysis, verify the selected agent is bound to the current project and can see the expected source tree under `/workspace`.
- Before exact string replacement, read the target fragment and preserve its current tabs, spaces, and alignment.
- Never run parallel edits against the same file: each edit may write from stale content and silently discard another change.
- When `storage.Store` gains methods, update every hand-rolled test stub (`memStore` and similar) in the same change; add `var _ storage.Store = (*stub)(nil)` so compile fails early.
- For test-fix sessions, first run `go test ./...` without cache assumptions on packages that failed to compile; interface drift often surfaces as `[build failed]` before any assertion runs.
- Git hooks that run `go test` must unset `GIT_DIR`/`GIT_INDEX_FILE`/`GIT_WORK_TREE` (and related) before tests; otherwise nested fixture repos inherit the outer commit index and fail with `invalid object` / `Error building trees`.
- When a Docker sub-agent itself reproduces a provider bootstrap bug, expect delegation to fail before task execution; use the failure as manual reproduction evidence, then perform repository analysis locally until the bootstrap path is fixed.
- Run Git commands from the actual component repository (for example `brute/`), not from the multi-component project container root.
- Before inserting at a numbered line, confirm the current file length and that the line is outside any open function; use append or an exact structural replacement when placement is not safely known.
- If delegation fails with a provider fallback configuration error, do not retry blindly; continue the scoped investigation locally and record the infrastructure failure separately from repository test results.
- A Go auto-toolchain may report `go: no such tool "covdata"` only for coverage runs of packages without test files; verify plain and race tests separately before changing repository code.
- A non-fast-forward push requires fetching and rebasing the local commits onto the updated remote branch before retrying the push.
- Git `--numstat --find-renames` prints `prefix/{old => new}suffix`. Parsing only the right-hand side drops the shared prefix, so merge with `--name-status` fails and the file looks like a plain `M`. Expand the brace form to the destination path and keep `old_path`.
- Python snippets executed with `python -c` must use `True`/`False`. JSON `true`/`false` is a runtime NameError, and Go fakes that return `{"ok":true}` will not catch it. Execute the real script against a stub package. On macOS, `python3` may be 3.9; prefer `python3.11+` for 3.10+ probes.
- Brute pre-commit/pre-push run `go test -race ./...` in the developer shell. Unset `AAGENT_TTS_ENGINE` before those hooks; ambient local TTS settings make `speech_completion_test.go` call a real engine instead of the fake tool.
- Retry a Brute pre-commit once when `TestHandleGetSessionApprovalReturnsMultiQuestionDTO` fails only on `TempDir RemoveAll cleanup: directory not empty`. That is leftover fixture cleanup, not a product regression.
