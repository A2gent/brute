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

// createSessionWorktree is a no-op unless worktrees are enabled for the project.
// Branch defaults to a2gent/<session-short-id>; the folder is named after the branch.
func (s *Server) createSessionWorktree(sessionID string, projectID string, branch string) error {
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
	if branch == "" {
		branch = "a2gent/" + sessionID[:min(8, len(sessionID))]
	}
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, branch)
	baseRef := settings[projectWorktreesBaseRefSettingKey]
	if baseRef == "" {
		baseRef = "origin/HEAD"
	}

	// The slot is taken before git runs, so two sessions created at once cannot race.
	wt := &storage.SessionWorktree{
		SessionID: sessionID,
		ProjectID: projectID,
		Path:      filepath.Join(projectWorktreesDirectory(root, settings[projectWorktreesDirectorySettingKey]), name),
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
	if _, err := runGitCommand(root, "worktree", "add", "--relative-paths", "-b", branch, wt.Path, baseRef); err != nil {
		_ = s.store.DeleteSessionWorktree(sessionID)
		return err
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
			if _, pruneErr := runGitCommand(root, "worktree", "prune"); pruneErr != nil {
				logging.Warn("Failed to prune missing session worktree: session=%s error=%v", sessionID, pruneErr)
			}
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

// Only project sessions can have a worktree, which also skips the lookup for all others.
func (s *Server) sessionWorktreePayload(sess *session.Session) *SessionWorktreePayload {
	if sess.ProjectID == nil {
		return nil
	}
	wt, err := s.store.GetSessionWorktree(sess.ID)
	if err != nil || wt == nil {
		return nil
	}
	payload := &SessionWorktreePayload{Path: wt.Path, Branch: wt.Branch, BaseRef: wt.BaseRef, Slot: wt.Slot}
	if project, err := s.store.GetProject(wt.ProjectID); err == nil {
		if template := normalizeProjectSettings(project.Settings)[projectWorktreesAppURLTemplateSettingKey]; template != "" {
			payload.AppURL = strings.ReplaceAll(template, "{slot}", strconv.Itoa(wt.Slot))
		}
	}
	return payload
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
	wt, err := s.store.GetSessionWorktree(sess.ID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if wt == nil {
		s.errorResponse(w, http.StatusBadRequest, "Session has no worktree")
		return
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
	if err := s.teardownSessionWorktree(sess.ID); err != nil {
		s.errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	if _, err := runGitCommand(root, "switch", branch); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Worktree removed, but switching the main folder failed: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
