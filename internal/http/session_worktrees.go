// session_worktrees.go gives every new session of a worktree-enabled project its own
// git worktree on its own branch, so parallel sessions never edit the same folder and
// the main project folder stays the human's.
package http

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	projectWorktreesSetupCmdSettingKey       = "A2GENT_PROJECT_WORKTREES_SETUP_CMD"
	projectWorktreesTeardownCmdSettingKey    = "A2GENT_PROJECT_WORKTREES_TEARDOWN_CMD"
	projectWorktreesAppURLTemplateSettingKey = "A2GENT_PROJECT_WORKTREES_APP_URL_TEMPLATE"
)

type SessionWorktreePayload struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	BaseRef string `json:"base_ref"`
	Slot    int    `json:"slot"`
	AppURL  string `json:"app_url,omitempty"`
}

var projectWorktreeGitLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

// lockWorktreeGit serializes operations that update shared Git metadata for the same
// project. Concurrent worktree adds otherwise race while writing .git/config.lock.
func lockWorktreeGit(root string) func() {
	key := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		key = resolved
	}
	if absolute, err := filepath.Abs(key); err == nil {
		key = absolute
	}

	projectWorktreeGitLocks.mu.Lock()
	lock := projectWorktreeGitLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		projectWorktreeGitLocks.locks[key] = lock
	}
	projectWorktreeGitLocks.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// createSessionWorktree is a no-op unless worktrees are enabled for the project.
// Branch defaults to a2gent/<session-short-id>, an existing branch is continued (see
// addSessionWorktree); baseRef defaults to the project setting. The folder is always
// a2gent-<session-short-id>: the branch may be renamed later, but the session cwd and
// WORKTREE_NAME-derived resources (e.g. Spareto's app container) are bound to the folder.
func (s *Server) createSessionWorktree(sessionID string, projectID string, branch string, baseRef string) error {
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return err
	}
	settings := normalizeProjectSettings(project.Settings)
	if settings[projectWorktreesEnabledSettingKey] != "true" {
		return nil
	}
	root, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		return err
	}
	shortID := sessionID[:min(8, len(sessionID))]
	if branch == "" {
		branch = "a2gent/" + shortID
	}
	if baseRef == "" {
		baseRef = settings[projectWorktreesBaseRefSettingKey]
	}
	if baseRef == "" {
		baseRef = "origin/HEAD"
	}

	// The slot is taken before git runs, so two sessions created at once cannot race.
	wt := &storage.SessionWorktree{
		SessionID: sessionID,
		ProjectID: projectID,
		Path:      filepath.Join(projectWorktreesDirectory(root, settings[projectWorktreesDirectorySettingKey]), "a2gent-"+shortID),
		Branch:    branch,
		BaseRef:   baseRef,
		CreatedAt: time.Now(),
	}
	if err := s.store.CreateSessionWorktree(wt); err != nil {
		return err
	}
	if _, err := runGitCommand(root, "fetch", "origin"); err != nil {
		logging.Warn("Session worktree fetch failed, using local refs: session=%s error=%v", sessionID, err)
	}
	unlockGit := lockWorktreeGit(root)
	addErr := addSessionWorktree(root, wt.Path, branch, baseRef)
	unlockGit()
	if addErr != nil {
		_ = s.store.DeleteSessionWorktree(sessionID)
		return addErr
	}
	// A failed setup (e.g. Docker not running) still leaves a usable checkout, so it is only logged.
	if command := settings[projectWorktreesSetupCmdSettingKey]; command != "" {
		if err := runWorktreeHook(sessionID, wt.Path, command, worktreeEnv(wt, root)); err != nil {
			logging.Warn("Session worktree setup failed: session=%s error=%v", sessionID, err)
		}
	}
	logging.LogSession("worktree", sessionID, fmt.Sprintf("slot=%d branch=%s path=%s", wt.Slot, branch, wt.Path))
	return nil
}

func sameFilesystemPath(first string, second string) bool {
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(resolved)
		}
		if absolute, err := filepath.Abs(path); err == nil {
			return filepath.Clean(absolute)
		}
		return filepath.Clean(path)
	}
	return resolve(first) == resolve(second)
}

// teardownSessionWorktree commits the session's work and, depending on mode, merges it
// into the branch open in the main folder or in the parent worktree before removing the
// checkout. completeModeDiscard (Delete, handoff) deliberately neither commits nor merges.
// Branches are kept so committed work remains recoverable even after removal.
func (s *Server) teardownSessionWorktree(sessionID string, mode string) error {
	wt, err := s.store.GetSessionWorktree(sessionID)
	if err != nil || wt == nil {
		return err
	}
	root, err := s.resolveProjectRootFolder(wt.ProjectID)
	if err != nil {
		return err
	}
	commitChanges := mode != completeModeDiscard
	mergeChanges := commitChanges && mode != completeModeKeep
	mergeDir := root
	if mode == completeModeParent {
		parent := s.parentSessionWorktree(sessionID)
		if parent == nil {
			return fmt.Errorf("session has no parent worktree to merge into")
		}
		mergeDir = parent.Path
	}
	unlockGit := lockWorktreeGit(root)
	defer unlockGit()

	_, statErr := os.Stat(wt.Path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to inspect session worktree path: %w", statErr)
	}
	topLevel, gitErr := runGitCommand(wt.Path, "rev-parse", "--show-toplevel")
	valid := statErr == nil && gitErr == nil && sameFilesystemPath(topLevel, wt.Path) && !sameFilesystemPath(topLevel, root)
	if !valid {
		// A missing folder can still have undelivered commits on its branch.
		// Deliver those before dropping the DB record; never touch a plain folder
		// that happens to occupy the stale path.
		// If the branch was deleted too there is nothing left to deliver, so
		// completing must not fail on the missing ref.
		_, branchErr := runGitCommand(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+wt.Branch+"^{commit}")
		if mergeChanges && branchErr == nil {
			if err := integrateSessionWorktree(sessionID, mergeDir, "refs/heads/"+wt.Branch); err != nil {
				return err
			}
		}
		if _, err := runGitCommand(root, "worktree", "prune"); err != nil {
			return fmt.Errorf("failed to prune stale session worktree: %w", err)
		}
		return s.store.DeleteSessionWorktree(sessionID)
	}
	// A stale record can also point at a different repository. Never stage,
	// merge or remove files there on behalf of this project.
	rootCommon, rootErr := runGitCommand(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	worktreeCommon, worktreeErr := runGitCommand(wt.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if rootErr != nil || worktreeErr != nil || !sameFilesystemPath(rootCommon, worktreeCommon) {
		return fmt.Errorf("session worktree does not belong to the project's Git repository; checkout is kept")
	}
	project, err := s.store.GetProject(wt.ProjectID)
	if err != nil {
		return err
	}
	if commitChanges {
		// Use the actual checkout, not the stored branch name: agents may rename
		// their branch. Keep detached commits too by merging their immutable SHA.
		status, err := runGitCommand(wt.Path, "status", "--porcelain")
		if err != nil {
			return fmt.Errorf("failed to inspect session worktree changes: %w", err)
		}
		if status != "" {
			if _, err := runGitLogged(sessionID, wt.Path, "add", "--all"); err != nil {
				return fmt.Errorf("failed to stage session worktree changes: %w", err)
			}
			shortID := sessionID[:min(8, len(sessionID))]
			message := fmt.Sprintf("Complete session %s", shortID)
			if _, err := runGitLogged(sessionID, wt.Path, "commit", "-m", message); err != nil {
				return fmt.Errorf("failed to commit session worktree changes: %w", err)
			}
			logging.LogSession("worktree-commit", sessionID, fmt.Sprintf("branch=%s", wt.Branch))
		}
		commit, err := runGitCommand(wt.Path, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if mergeChanges {
			if err := integrateSessionWorktree(sessionID, mergeDir, commit); err != nil {
				return err
			}
		}
	}
	// Do not stop the session's dev environment until local delivery succeeds.
	if command := normalizeProjectSettings(project.Settings)[projectWorktreesTeardownCmdSettingKey]; command != "" {
		if err := runWorktreeHook(sessionID, wt.Path, command, worktreeEnv(wt, root)); err != nil {
			logging.Warn("Session worktree teardown failed: session=%s error=%v", sessionID, err)
		}
	}
	if _, err := runGitLogged(sessionID, root, "worktree", "remove", wt.Path); err != nil {
		return fmt.Errorf("failed to remove session worktree %s: %w", wt.Path, err)
	}
	// A session finishing after worktrees were switched off may have held the last one.
	if normalizeProjectSettings(project.Settings)[projectWorktreesEnabledSettingKey] != "true" {
		dropRelativeWorktreesExtension(root)
	}
	return s.store.DeleteSessionWorktree(sessionID)
}

// sessionWorktree returns the worktree of the session or of its nearest ancestor, so
// child sessions (continuations, sub-sessions) keep working on the parent's branch.
func (s *Server) sessionWorktree(sess *session.Session) *storage.SessionWorktree {
	if sess == nil {
		return nil
	}
	id := sess.ID
	for id != "" {
		wt, err := s.store.GetSessionWorktree(id)
		if err != nil {
			logging.Warn("Failed to load session worktree: session=%s error=%v", id, err)
			return nil
		}
		if wt != nil {
			return wt
		}
		parent, err := s.store.GetSessionSummary(id)
		if err != nil || parent.ParentID == nil {
			return nil
		}
		id = *parent.ParentID
	}
	return nil
}

// sessionWorktreeEnv is the env contract shared with project hooks (e.g. Spareto's
// `just wt-up`): hooks and the session's shell see the same three variables.
func (s *Server) sessionWorktreeEnv(wt *storage.SessionWorktree) []string {
	root, _ := s.resolveProjectRootFolder(wt.ProjectID)
	return worktreeEnv(wt, root)
}

func worktreeEnv(wt *storage.SessionWorktree, mainRoot string) []string {
	return []string{
		"WORKTREE_SLOT=" + strconv.Itoa(wt.Slot),
		"WORKTREE_NAME=" + filepath.Base(wt.Path),
		"WORKTREE_MAIN_ROOT=" + mainRoot,
	}
}

func runWorktreeHook(sessionID string, dir string, command string, env []string) error {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(scrubInheritedGitEnv(os.Environ()), env...)
	output, err := runLogged(sessionID, command, cmd)
	if err != nil {
		return fmt.Errorf("%q: %w: %s", command, err, output)
	}
	return nil
}

// sessionWorktreePayload tells the UI where a session works: wt (its own or an ancestor's
// worktree), or mainFolder when the project uses worktrees but the session has none, e.g.
// it was created before worktrees were enabled. settings are the normalized project settings.
func sessionWorktreePayload(wt *storage.SessionWorktree, settings map[string]string) (payload *SessionWorktreePayload, mainFolder bool) {
	if wt == nil {
		return nil, settings[projectWorktreesEnabledSettingKey] == "true"
	}
	payload = &SessionWorktreePayload{Path: wt.Path, Branch: worktreeBranch(wt), BaseRef: wt.BaseRef, Slot: wt.Slot}
	if template := settings[projectWorktreesAppURLTemplateSettingKey]; template != "" {
		payload.AppURL = strings.ReplaceAll(template, "{slot}", strconv.Itoa(wt.Slot))
	}
	return payload, false
}

// worktreeBranch reads the checked-out branch from git, because the agent or the user may
// rename it with `git branch -m`; the stored name is only the initial one and the fallback
// when git cannot be read (e.g. the folder was removed). Detached HEAD shows the short commit.
func worktreeBranch(wt *storage.SessionWorktree) string {
	if branch, err := currentGitBranch(wt.Path); err == nil && branch != "" {
		return branch
	}
	if commit, err := runGitCommand(wt.Path, "rev-parse", "--short", "HEAD"); err == nil && commit != "" {
		return commit
	}
	return wt.Branch
}

// sessionWorktreeFields loads the worktree fields of a single session; lists preload them instead.
// Only project sessions can have a worktree, which also skips the lookups for all others.
func (s *Server) sessionWorktreeFields(sess *session.Session) (*SessionWorktreePayload, bool) {
	if sess.ProjectID == nil {
		return nil, false
	}
	var settings map[string]string
	if project, err := s.store.GetProject(*sess.ProjectID); err == nil {
		settings = normalizeProjectSettings(project.Settings)
	}
	return sessionWorktreePayload(s.sessionWorktree(sess), settings)
}

// handleSessionWorktreeHandoff moves the session's branch into the main project folder,
// so the human can continue there: the worktree is removed (branch kept) and the main
// folder switches to the branch. Requires an idle session and a clean main folder.
func (s *Server) handleSessionWorktreeHandoff(w http.ResponseWriter, r *http.Request) {
	// The run lease keeps the session from starting while its folder is being removed.
	_, release, err := s.sessionManager.AcquireRun(r.Context(), chi.URLParam(r, "sessionID"))
	if err != nil {
		s.errorResponse(w, http.StatusConflict, "Session is still running; stop it before the handoff")
		return
	}
	defer release()
	sess, err := s.sessionManager.Get(chi.URLParam(r, "sessionID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	if s.sessionIsBusy(sess) {
		s.errorResponse(w, http.StatusConflict, "Session is still running; stop it before the handoff")
		return
	}
	// A continuation works in its ancestor's worktree, so the handoff moves that one.
	wt := s.sessionWorktree(sess)
	if wt == nil {
		s.errorResponse(w, http.StatusBadRequest, "Session has no worktree")
		return
	}
	if wt.SessionID != sess.ID {
		// The owner's lease too, so it cannot start while its folder is being removed.
		_, releaseOwner, err := s.sessionManager.AcquireRun(r.Context(), wt.SessionID)
		if err != nil {
			s.errorResponse(w, http.StatusConflict, "The session that owns this worktree is still running; stop it before the handoff")
			return
		}
		defer releaseOwner()
	}
	root, err := s.resolveProjectRootFolder(wt.ProjectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	status, err := runGitCommand(root, "status", "--porcelain")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read main folder status: "+err.Error())
		return
	}
	if status != "" {
		s.errorResponse(w, http.StatusConflict, "Main folder has uncommitted changes; commit or stash them first")
		return
	}
	// The agent may have renamed its branch, so switch to what the worktree has checked out now.
	branch, err := currentGitBranch(wt.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read worktree branch: "+err.Error())
		return
	}
	// Removing a worktree on a detached HEAD would leave its commits on no branch.
	if branch == "" {
		s.errorResponse(w, http.StatusConflict, "Worktree is on a detached HEAD; check out a branch first")
		return
	}
	if err := s.teardownSessionWorktree(wt.SessionID, completeModeDiscard); err != nil {
		s.errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := runGitCommand(root, "switch", branch); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Worktree removed, but switching the main folder failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
