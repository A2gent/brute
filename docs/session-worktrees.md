# Session worktrees and local delivery

## Complete versus Delete

For a worktree-backed session, `POST /sessions/{sessionID}/archive` (Caesar's **Complete**) now:

1. Rejects running sessions/descendants and holds their run leases.
2. Serializes Git changes for the project.
3. Saves remaining tracked and untracked session files in a commit, using normal commit hooks.
4. Integrates the actual session HEAD into the **local `main`** branch (`master` if there is no `main`) in the project's main folder. Renamed branches and detached session HEADs are supported.
5. Verifies the session commit is an ancestor of the local target branch.
6. Runs teardown and removes the session worktree, preserving its branch for recovery.
7. Archives the session subtree only after all owned worktrees were delivered successfully.

Complete does **not** fetch, push, merge into `origin/main`, or switch branches in the human's checkout. Sessions without a worktree retain their archive-only behavior. Delete remains teardown/removal without delivery; it does not merge unaccepted work.

The archive progress endpoint streams snapshot, local merge and removal output. Caesar's Complete tooltip describes the local merge and explicitly says no push. Complete is not a substitute for task-specific tests, review, or a board QA acceptance gate.

## Safety and retries

The main project folder must have the local target branch checked out. For an undelivered commit it must also be clean, including the index and untracked files, with no in-progress merge/rebase/cherry-pick/revert. Already integrated commits require no main-folder writes and can be cleaned up even if the human has since started new WIP.

A fast-forward is preferred. Diverged histories are merged with normal hooks, without autostash or squashing. Conflicting merges started by Complete are aborted; the session remains unarchived and its worktree/branch remain available. Dirty main, missing source commits, wrong checked-out branch and hook errors return HTTP 409 with a clearing condition. Fix the reported condition and retry Complete. The application never resets, stashes, force-pushes or overwrites foreign WIP to complete a session.

If the session folder was removed externally, Complete first integrates its surviving recorded branch before pruning stale metadata. A plain directory at a stale path is preserved. If the source branch is unavailable, completion is blocked rather than silently losing the delivery obligation.

Project Git operations are serialized within this server. External Git commands or editors are not covered by this lock; do not concurrently modify the main checkout during integration. If several descendants own worktrees, successful earlier merges are retained when a later merge fails, but no archive flags are written; retry resumes the remaining delivery.

## Agent rules

Worktree session environment instructions distinguish local delivery from remote publication and assign bound-folder cleanup to Complete. Project task-loop skills should use the same local-integration gate before claiming delivery. A push alone cannot update the user's running checkout. Remote publication, when explicitly requested, follows local integration as a separate operation.

## Verification

```sh
go test ./internal/http -run 'Test(Complete|DeleteDoesNotMerge|SessionWorktrees|ArchiveSessionWhenWorktree)' -count=1
```

Regressions cover main/master delivery, unchanged bare remote, renamed/detached session branches, diverged histories, dirty/staged main with retry, conflicts with automatic abort, commit-hook rejection, already-integrated cleanup, wrong target branch, missing worktree recovery and missing source refs.
