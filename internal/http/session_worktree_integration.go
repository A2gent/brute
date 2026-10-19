package http

import (
	"fmt"
	"os"
	"path/filepath"
)

// integrateSessionWorktree delivers into the checkout the user actually runs.
// A remote ref is not a delivery target: pushing never updates that checkout.
// The caller holds lockWorktreeGit for the entire commit/merge/removal sequence.
func integrateSessionWorktree(sessionID, root, source string) error {
	branch := ""
	for _, candidate := range []string{"main", "master"} {
		if _, err := runGitCommand(root, "show-ref", "--verify", "--quiet", "refs/heads/"+candidate); err == nil {
			branch = candidate
			break
		}
	}
	if branch == "" {
		return fmt.Errorf("cannot complete: project has no local main or master branch")
	}
	current, err := currentGitBranch(root)
	if err != nil || current != branch {
		return fmt.Errorf("cannot complete: open local %s in the main project folder first (current branch: %s)", branch, current)
	}
	commit, err := runGitCommand(root, "rev-parse", "--verify", source+"^{commit}")
	if err != nil {
		return fmt.Errorf("cannot complete: session commit is unavailable; preserve the session branch and resolve it first: %w", err)
	}
	// An already delivered session needs no writes to the main checkout, even if
	// the human has since started unrelated work there.
	if _, err := runGitCommand(root, "merge-base", "--is-ancestor", commit, "HEAD"); err == nil {
		archiveProgressLog(sessionID, "Session changes are already in local "+branch)
		return nil
	}
	status, err := runGitCommand(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("failed to inspect main project folder: %w", err)
	}
	if status != "" {
		return fmt.Errorf("cannot merge into local %s: main project folder has uncommitted changes; preserve or commit them, then retry Complete. Session worktree is kept", branch)
	}
	// A fully staged merge/rebase can look clean. Never complete someone else's
	// in-progress Git operation or allow Git to autostash their files.
	for _, state := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, pathErr := runGitCommand(root, "rev-parse", "--git-path", state)
		if pathErr != nil {
			return pathErr
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("cannot complete: finish the main project folder's %s operation first", state)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	archiveProgressLog(sessionID, "Merging session changes into local "+branch+" (no push)")
	if _, err := runGitLogged(sessionID, root, "merge", "--no-edit", "--no-autostash", "--ff", "--no-squash", commit); err != nil {
		// The checkout was clean before our merge. Abort only a merge we started;
		// preserve both branches and the session checkout for conflict resolution.
		if _, stateErr := runGitCommand(root, "rev-parse", "--verify", "MERGE_HEAD"); stateErr == nil {
			if _, abortErr := runGitLogged(sessionID, root, "merge", "--abort"); abortErr != nil {
				return fmt.Errorf("local %s merge failed: %v; automatic merge abort also failed: %w", branch, err, abortErr)
			}
		}
		return fmt.Errorf("local %s merge failed; resolve the conflict or hook failure and retry Complete. Session worktree is kept: %w", branch, err)
	}
	if _, err := runGitCommand(root, "merge-base", "--is-ancestor", commit, "refs/heads/"+branch); err != nil {
		return fmt.Errorf("cannot complete: session commit was not integrated into local %s: %w", branch, err)
	}
	archiveProgressLog(sessionID, "Session changes verified in local "+branch)
	return nil
}
