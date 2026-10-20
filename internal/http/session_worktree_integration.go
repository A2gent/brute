package http

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

// Complete modes: what happens to the session worktree's changes.
const (
	completeModeCurrent = "merge_current" // commit, merge into the branch checked out in the main project folder (default)
	completeModeParent  = "merge_parent"  // commit, merge into the parent session's worktree branch
	completeModeKeep    = "keep"          // commit on the session branch only, no merge
	completeModeDiscard = "discard"       // Delete: no commit, no merge
)

func validCompleteMode(mode string) bool {
	switch mode {
	case "", completeModeCurrent, completeModeParent, completeModeKeep, completeModeDiscard:
		return true
	}
	return false
}

// integrateSessionWorktree delivers into the checkout dir, whatever branch it has open:
// the human picks the target by switching branches in the main folder (or by choosing a
// parent worktree). A remote ref is not a delivery target: pushing never updates a checkout.
// The caller holds lockWorktreeGit for the entire commit/merge/removal sequence.
func integrateSessionWorktree(sessionID, dir, source string) error {
	branch, err := currentGitBranch(dir)
	if err != nil || branch == "" {
		return fmt.Errorf("cannot complete: %s has no branch checked out; switch it to the branch to merge into", dir)
	}
	commit, err := runGitCommand(dir, "rev-parse", "--verify", source+"^{commit}")
	if err != nil {
		return fmt.Errorf("cannot complete: session commit is unavailable; preserve the session branch and resolve it first: %w", err)
	}
	// An already delivered session needs no writes to the target checkout, even if
	// the human has since started unrelated work there.
	if _, err := runGitCommand(dir, "merge-base", "--is-ancestor", commit, "HEAD"); err == nil {
		archiveProgressLog(sessionID, "Session changes are already in local "+branch)
		return nil
	}
	status, err := runGitCommand(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("failed to inspect target folder: %w", err)
	}
	if status != "" {
		return fmt.Errorf("cannot merge into local %s: target folder has uncommitted changes; preserve or commit them, then retry Complete. Session worktree is kept", branch)
	}
	// A fully staged merge/rebase can look clean. Never complete someone else's
	// in-progress Git operation or allow Git to autostash their files.
	for _, state := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, pathErr := runGitCommand(dir, "rev-parse", "--git-path", state)
		if pathErr != nil {
			return pathErr
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("cannot complete: finish the target folder's %s operation first", state)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	archiveProgressLog(sessionID, "Merging session changes into local "+branch+" (no push)")
	if _, err := runGitLogged(sessionID, dir, "merge", "--no-edit", "--no-autostash", "--ff", "--no-squash", commit); err != nil {
		// The checkout was clean before our merge. Abort only a merge we started;
		// preserve both branches and the session checkout for conflict resolution.
		if _, stateErr := runGitCommand(dir, "rev-parse", "--verify", "MERGE_HEAD"); stateErr == nil {
			if _, abortErr := runGitLogged(sessionID, dir, "merge", "--abort"); abortErr != nil {
				return fmt.Errorf("local %s merge failed: %v; automatic merge abort also failed: %w", branch, err, abortErr)
			}
		}
		return fmt.Errorf("local %s merge failed; resolve the conflict or hook failure and retry Complete. Session worktree is kept: %w", branch, err)
	}
	if _, err := runGitCommand(dir, "merge-base", "--is-ancestor", commit, "HEAD"); err != nil {
		return fmt.Errorf("cannot complete: session commit was not integrated into local %s: %w", branch, err)
	}
	archiveProgressLog(sessionID, "Session changes verified in local "+branch)
	return nil
}

// parentSessionWorktree is the worktree of the nearest ancestor session, i.e. the
// checkout a child session's branch was split off from. nil when there is none.
func (s *Server) parentSessionWorktree(sessionID string) *storage.SessionWorktree {
	summary, err := s.store.GetSessionSummary(sessionID)
	if err != nil || summary.ParentID == nil {
		return nil
	}
	parent, err := s.sessionManager.Get(*summary.ParentID)
	if err != nil {
		return nil
	}
	if wt := s.sessionWorktree(parent); wt != nil && wt.SessionID != sessionID {
		return wt
	}
	return nil
}

type completePreviewParent struct {
	SessionID string `json:"session_id"`
	Branch    string `json:"branch"`
}

type completePreviewResponse struct {
	HasWorktree  bool                   `json:"has_worktree"`
	HasChanges   bool                   `json:"has_changes"`
	Branch       string                 `json:"branch,omitempty"`
	TargetBranch string                 `json:"target_branch,omitempty"`
	Parent       *completePreviewParent `json:"parent,omitempty"`
}

// handleCompletePreview tells the Complete dialog what there is to deliver: whether the
// session's own worktree holds changes, and the branches they can be merged into.
func (s *Server) handleCompletePreview(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	wt, err := s.store.GetSessionWorktree(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load session worktree: "+err.Error())
		return
	}
	resp := completePreviewResponse{}
	if wt == nil {
		s.jsonResponse(w, http.StatusOK, resp)
		return
	}
	root, err := s.resolveProjectRootFolder(wt.ProjectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	resp.HasWorktree = true
	resp.Branch = worktreeBranch(wt)
	resp.TargetBranch, _ = currentGitBranch(root)
	if parent := s.parentSessionWorktree(sessionID); parent != nil {
		resp.Parent = &completePreviewParent{SessionID: parent.SessionID, Branch: worktreeBranch(parent)}
	}
	// A folder or branch that cannot be read counts as "has changes": the dialog then
	// lets the human decide instead of silently dropping work.
	status, statusErr := runGitCommand(wt.Path, "status", "--porcelain")
	// The agent may have renamed its branch, so compare the checkout's real HEAD commit.
	head, headErr := runGitCommand(wt.Path, "rev-parse", "HEAD")
	_, mergedErr := runGitCommand(root, "merge-base", "--is-ancestor", head, "HEAD")
	resp.HasChanges = statusErr != nil || status != "" || headErr != nil || mergedErr != nil
	s.jsonResponse(w, http.StatusOK, resp)
}
