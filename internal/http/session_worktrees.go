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
		if err := runWorktreeHook(wt.Path, command, worktreeEnv(wt, root)); err != nil {
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

// teardownSessionWorktree runs the teardown hook and removes the worktree folder; the
// branch is kept. git refuses to remove a worktree with uncommitted changes, which is
// returned so archive/delete/handoff never throw work away.
func (s *Server) teardownSessionWorktree(sessionID string) error {
	wt, err := s.store.GetSessionWorktree(sessionID)
	if err != nil || wt == nil {
		return err
	}
	if _, statErr := os.Stat(wt.Path); os.IsNotExist(statErr) {
		// A worktree may be removed outside the app; its absence must not block archiving.
		if root, rootErr := s.resolveProjectRootFolder(wt.ProjectID); rootErr == nil {
			unlockGit := lockWorktreeGit(root)
			if _, pruneErr := runGitCommand(root, "worktree", "prune"); pruneErr != nil {
				logging.Warn("Failed to prune missing session worktree: session=%s error=%v", sessionID, pruneErr)
			}
			unlockGit()
		}
		return s.store.DeleteSessionWorktree(sessionID)
	} else if statErr != nil {
		return fmt.Errorf("failed to inspect session worktree path: %w", statErr)
	}
	project, err := s.store.GetProject(wt.ProjectID)
	if err != nil {
		return err
	}
	root, err := s.resolveProjectRootFolder(wt.ProjectID)
	if err != nil {
		return err
	}
	topLevel, gitErr := runGitCommand(wt.Path, "rev-parse", "--show-toplevel")
	if gitErr != nil || !sameFilesystemPath(topLevel, wt.Path) || sameFilesystemPath(topLevel, root) {
		// A stale DB record can point to an ordinary directory; never run Git there
		// or remove that directory as if it were an owned worktree.
		logging.Warn("Session %s has a stale worktree record for non-worktree path %s", sessionID, wt.Path)
		unlockGit := lockWorktreeGit(root)
		if _, pruneErr := runGitCommand(root, "worktree", "prune"); pruneErr != nil {
			logging.Warn("Failed to prune stale session worktree metadata: session=%s error=%v", sessionID, pruneErr)
		}
		unlockGit()
		return s.store.DeleteSessionWorktree(sessionID)
	}
	if command := normalizeProjectSettings(project.Settings)[projectWorktreesTeardownCmdSettingKey]; command != "" {
		if err := runWorktreeHook(wt.Path, command, worktreeEnv(wt, root)); err != nil {
			logging.Warn("Session worktree teardown failed: session=%s error=%v", sessionID, err)
		}
	}
	// A folder deleted by hand only needs its stale git metadata pruned.
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		_, err = runGitCommand(root, "worktree", "prune")
	} else {
		_, err = runGitCommand(root, "worktree", "remove", wt.Path)
	}
	if err != nil {
		return fmt.Errorf("failed to remove session worktree %s: %w", wt.Path, err)
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

func runWorktreeHook(dir string, command string, env []string) error {
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(scrubInheritedGitEnv(os.Environ()), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%q: %w: %s", command, err, strings.TrimSpace(string(output)))
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
	if err := s.teardownSessionWorktree(wt.SessionID); err != nil {
		s.errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := runGitCommand(root, "switch", branch); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Worktree removed, but switching the main folder failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
