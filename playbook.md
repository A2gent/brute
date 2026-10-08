- Before delegating edits, check the agent workspace mount mode: read-only agents can review or propose patches but cannot implement them. Use a writable coding agent or apply the reviewed patch locally.
- Scope file discovery to the component repository and verify files/recipes exist before reading them; this workspace root is not a Git repository.
- Discover component repositories and source filenames before Git commands or reads; the workspace root is not a Git worktree and tool interfaces live in `internal/tools/manager.go`, not `tool.go`. Verify delegated compression claims against actual request construction and nested tool outputs.
- If a task-board ref renders with replacement characters and update/get fails, find the task by title or tag before trying an update; do not assume the displayed ref is a usable identifier.
# Playbook
- If CI job logs require GitHub authentication and `gh` is unavailable, inspect public Actions job step timestamps through the GitHub API, then profile locally; avoid claiming unverified cache-hit status or CI speedups.
- On a locally downloaded Go toolchain missing `pkg/tool/*/covdata`, a full `go test -coverprofile ./...` may fail only for packages without tests; verify coverage on tested packages and run the full suite without coverage before attributing the failure to project code.

- Before delegating repository analysis, verify the selected agent is bound to the current project and can see the expected source tree under `/workspace`.
- Before exact string replacement, read the target fragment and preserve its current tabs, spaces, and alignment.
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
- `TestHandleGetSessionApprovalReturnsMultiQuestionDTO` failing on `TempDir RemoveAll: directory not empty` is an open SQLite WAL plus a leftover `approvalBroker.Request` goroutine. Close the store in `newBruteHTTPProxyTestServer` and wait for `t.Context()`-canceled Request goroutines before cleanup. Do not treat a retry as the fix.
- When `local-speech-mlx.py` gains a TTS flag, add the same optional argument to every inline `fake-local-speech-mlx.py` (`speechengine_test.go`, `mlx_tts_test.go`, `stt_test.go`). Argparse rejects unknown flags, so `TestSynthesizeWithLanguageQwenRussian` and `TestQwen3TTSToolExecute` fail on `--instruct` even though the real helper already accepts it.
- A stale `caesar/test_output.log` is not current brute status. Re-run `go test` in `brute/` before changing code. Coverage (`-coverprofile`) is the command that surfaces the TempDir flake more often than a cached `go test ./...`.
- Public GitHub Actions pages expose only failure annotations, not private job logs; when `gh` or admin credentials are unavailable, reproduce the workflow in a Linux container with `CI=true` and `GITHUB_ACTIONS=true` rather than inferring the failure from the final exit code.
- Long container test commands can outlive the tool's call timeout and lose their final output; run them detached with log and exit-code files, then inspect those files before claiming success.
- Check the storage interface before setting fixture options: `storage.Store` exposes `SaveSettings(map[string]string)`, not `SetSetting`.

- Preview status must inspect both Chrome product and User-Agent: headless Chrome may report the ordinary Chrome product string. Keep target lifecycle waits scoped per target and cancellation-aware.
- When splitting Go tests into a new file, carry over the imports used by that fragment and compile the focused package immediately.
- A real headless Chrome smoke check found no initial screencast frame within four seconds on an already-loaded static page with everyNthFrame=2. Preserve explicit protocol parameters, report this limitation, and investigate a read-only initial snapshot without resizing the viewport.
- Docker delegation tests that use a fake OpenAI provider must clear both `A2GENT_PARENT_PROXY_URL` and `OPENAI_BASE_URL` with `t.Setenv`; either inherited override redirects requests away from the fixture. If unrelated staged code prevents package compilation, leave it untouched and report any explicit-file test exclusion.

## Jev classify tool
- Locate the component repository before Git commands; the multi-component workspace root has no Git metadata.
- Discover file paths before reads and avoid optional-path shell probes that hide useful output behind a nonzero exit.
- Verify agent research against current API docs: System One score now uses an ordered criteria array, not a levels field; noul does not expose separate confidence.
- Conditional tools must refresh credentials after settings changes. Test integrations through HTTP validation, not only direct database inserts.
- Reject special file descriptors without blocking: a FIFO can hang in os.Open before IsRegular checks or context timeout handling.
- Before removing a tool-local constant, search package-wide users: maxOutputSize in bash.go is also used by code_execution.go.
- Read-cache references need outgoing-request validation after compaction, not just cache invalidation. Keep a recovery snapshot in non-model metadata and strip it from parallel JSON; a result can be compacted between emitting a stub and sending the next request.
- Long full-suite validation can outlive the parallel wrapper's 90-second limit. Use a direct bash call or inspect the completed log before claiming a timeout or rerunning tests. Concurrent test-first edits may temporarily break unrelated package compilation; verify the scoped patch against HEAD.
- During concurrent edits, shared test failures can be transient incomplete code. Re-read the current diff and run focused tests instead of overwriting the other session's files. Run lengthy suites detached with explicit exit files; a tool timeout is not a test result.

## Jev browser_act loop
- Verify third-party API shapes with `go doc` before writing code against them: `gson.JSON.Unmarshal` fails with "value has been parsed" on a value rod already decoded, so `MarshalJSON()` then `json.Unmarshal` is the working path for `page.Eval` results.
- Widening a shared struct field to `any` (jev `SystemOneRequest.State`, `Question.Instructions`) compiles for all string callers but breaks test assertions that use `len`/`strings` on the field. Add `StateString()`/`InstructionsString()` helpers and update only the tests that decode into the real type - a blanket sed also hits tests that declare their own local struct.
- A JS freshness marker must not be captured in a closure on `window`. Closing over the observation's own text/actions makes every later check compare equal; re-run the whole snapshot and read only `.marker`.
- Mutation-check guard tests before trusting them: a `sed` pattern that does not match silently leaves the code unmutated, so the test passes for the wrong reason. Confirm the mutation applied (grep for it) before reading the result.
- Reuse the registered `browser_chrome` instance via `manager.Get("browser_chrome")` rather than constructing a second one; the operation gate is per-instance, so a fresh instance would not serialize against plain browser calls.

## Jev web relevance
- Classifier candidates must fit the state budget in full; a high-confidence verdict on a partial preview cannot justify dropping unseen text.
- Preserve native search boundaries before formatting: snippets can forge numbered headings and URL lines.
- Credential guards must include OAuth, sensitive secrets and credential-bearing environment overrides; failed inventory refresh must replace stale clients with a no-egress fallback.
- Reject malformed UTF-8 before section splitting to guarantee progress, and inspect complete source inputs for secrets before truncation.
- A delegated benchmark prepared before the implementation exists is only a reference renderer. Replace it with the production wrapper and regenerate measurements before reporting savings.
- Scope scripted text replacements to an exact declaration; replacing a field fragment globally can also corrupt function parameter lists. Compile the focused package immediately after structural edits.
- macOS BSD sed needs `sed -i ""`; a bare `sed -i` fails silently in a pipeline and the "mutated" run is really the original. Use python or perl for in-place edits and grep to confirm.
- Read the workspace AGENTS.md before initial Git commands: `/Users/artjomkurapov/git/a2gent` is a multi-component workspace, and Git commands must run in a component repository.
- Linked-context `Truncated` marks the final prompt cap, not every section cap. Tests for image preservation should assert the text length and untouched image bytes rather than require this flag for oversized original messages.
- The read-only dev-code-reviewer container may lack Go. Treat its result as static review and run build, vet, and race tests on the host.
