// handlers_session_archive.go implements "Complete" (soft delete) for sessions:
// archived sessions are hidden from the default list and later reviewed by the
// built-in archived-sessions review loop.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	sessionArchiveFilterExclude = "exclude"
	sessionArchiveFilterOnly    = "only"
	sessionArchiveFilterInclude = "include"
)

func normalizeSessionArchiveFilter(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case sessionArchiveFilterOnly:
		return sessionArchiveFilterOnly
	case sessionArchiveFilterInclude, "all":
		return sessionArchiveFilterInclude
	default:
		return sessionArchiveFilterExclude
	}
}

func sessionArchivedAt(sess *session.Session) *time.Time {
	if sess == nil {
		return nil
	}
	return storage.SessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey)
}

func sessionMatchesArchiveFilter(sess *session.Session, filter string) bool {
	archived := sessionArchivedAt(sess) != nil
	switch filter {
	case sessionArchiveFilterOnly:
		return archived
	case sessionArchiveFilterInclude:
		return true
	default:
		return !archived
	}
}

func (s *Server) handleArchiveSession(w http.ResponseWriter, r *http.Request) {
	// The body is optional: callers without the Complete dialog keep the default merge.
	var req struct {
		Mode string `json:"mode"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}
	// Discard is the Delete path; Complete must never drop work.
	if !validCompleteMode(req.Mode) || req.Mode == completeModeDiscard {
		s.errorResponse(w, http.StatusBadRequest, "Invalid complete mode: "+req.Mode)
		return
	}
	s.setSessionArchived(w, r.Context(), chi.URLParam(r, "sessionID"), true, req.Mode)
}

func (s *Server) handleUnarchiveSession(w http.ResponseWriter, r *http.Request) {
	s.setSessionArchived(w, r.Context(), chi.URLParam(r, "sessionID"), false, "")
}

func (s *Server) sessionIsBusy(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	if s.activeSessionRunCount(sess.ID) > 0 {
		return true
	}
	switch sess.Status {
	case session.StatusRunning, session.StatusWaitingExternal:
		return true
	default:
		return false
	}
}
func (s *Server) setSessionArchived(w http.ResponseWriter, ctx context.Context, sessionID string, archived bool, mode string) {
	if archived {
		// Lets the UI show live progress while the (possibly slow) teardown runs.
		defer startArchiveProgress(sessionID)()
		// Archive tears down the shared worktree, so serialize it with child creation.
		s.sessionTreeMu.Lock()
		defer s.sessionTreeMu.Unlock()
	}
	root, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	// WHY: a running agent loop persists its in-memory metadata on every step and
	// would silently drop archived_at, so only idle sessions can be completed.
	if archived && s.sessionIsBusy(root) {
		s.errorResponse(w, http.StatusConflict, "Session is still running; stop it before completing")
		return
	}

	var rootWorktree *storage.SessionWorktree
	if archived {
		rootWorktree, err = s.store.GetSessionWorktree(root.ID)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to load session worktree: "+err.Error())
			return
		}
	}
	// Archive the whole subtree like delete does, so child sessions do not
	// resurface as orphaned roots in the main list.
	ids := []string{root.ID}
	all, listErr := s.sessionManager.List()
	if listErr != nil {
		logging.Warn("Failed to list sessions before archive cascade for %s: %v", sessionID, listErr)
		if archived && rootWorktree != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions before archiving worktree")
			return
		}
	} else {
		ids = sessionSubtreeIDs(all, root.ID)
	}

	hasWorktree := rootWorktree != nil
	if archived && !hasWorktree {
		for _, id := range ids[1:] {
			worktree, worktreeErr := s.store.GetSessionWorktree(id)
			if worktreeErr != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to load session worktree: "+worktreeErr.Error())
				return
			}
			if worktree != nil {
				hasWorktree = true
				break
			}
		}
	}

	// Child sessions share an ancestor's worktree. Hold every lease in the subtree
	// through commit/removal so no active run can write into the checkout mid-archive.
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	if archived && hasWorktree {
		for _, id := range ids {
			candidate := root
			if id != root.ID {
				candidate, err = s.sessionManager.Get(id)
				if err != nil {
					s.errorResponse(w, http.StatusInternalServerError, "Failed to load descendant session: "+err.Error())
					return
				}
			}
			if s.sessionIsBusy(candidate) {
				s.errorResponse(w, http.StatusConflict, "A descendant session is still running; stop it before completing")
				return
			}
			_, release, leaseErr := s.sessionManager.AcquireRun(ctx, id)
			if leaseErr != nil {
				s.errorResponse(w, http.StatusConflict, "A descendant session is still running; stop it before completing")
				return
			}
			releases = append(releases, release)
		}
	}

	// Merging into the parent's checkout must not race with the parent agent writing there.
	if archived && mode == completeModeParent {
		if parentWT := s.parentSessionWorktree(root.ID); parentWT != nil {
			_, release, leaseErr := s.sessionManager.AcquireRun(ctx, parentWT.SessionID)
			if leaseErr != nil {
				s.errorResponse(w, http.StatusConflict, "The parent session is still running; stop it before merging into its worktree")
				return
			}
			releases = append(releases, release)
		}
	}

	// Deliver every owned worktree before marking any session archived. A later
	// descendant's merge failure must not leave the root falsely completed.
	if archived {
		for _, id := range ids {
			shareArchiveProgress(root.ID, id)
			// The chosen mode is about the session being completed; descendants use the default.
			idMode := ""
			if id == root.ID {
				idMode = mode
			}
			if err := s.teardownSessionWorktree(id, idMode); err != nil {
				s.errorResponse(w, http.StatusConflict, err.Error())
				return
			}
		}
	}
	now := time.Now()
	for _, id := range ids {
		sess := root
		if id != root.ID {
			child, getErr := s.sessionManager.Get(id)
			if getErr != nil {
				if hasWorktree {
					s.errorResponse(w, http.StatusInternalServerError, "Failed to load descendant session: "+getErr.Error())
					return
				}
				continue
			}
			if archived && !hasWorktree && s.sessionIsBusy(child) {
				continue
			}
			sess = child
		}
		if archived {
			if sessionArchivedAt(sess) != nil {
				continue
			}
			sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey, &now)
		} else {
			sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey, nil)
			sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchiveAnalyzedAtKey, nil)
		}
		if saveErr := s.sessionManager.Save(sess); saveErr != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to update session archive state: "+saveErr.Error())
			return
		}
		if !archived && sess.Status == session.StatusQueued {
			// The queue skipped this session while it was archived.
			s.triggerSerialSessionQueueForSession(sess)
		}
	}

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(root))
}

// sessionSubtreeIDs returns rootID followed by all of its descendants (BFS order).
func sessionSubtreeIDs(all []*session.Session, rootID string) []string {
	childrenByParent := make(map[string][]string)
	for _, item := range all {
		if item == nil || item.ParentID == nil {
			continue
		}
		parentID := strings.TrimSpace(*item.ParentID)
		if parentID != "" {
			childrenByParent[parentID] = append(childrenByParent[parentID], item.ID)
		}
	}
	ids := []string{rootID}
	seen := map[string]struct{}{rootID: {}}
	for i := 0; i < len(ids); i++ {
		for _, childID := range childrenByParent[ids[i]] {
			if _, ok := seen[childID]; ok {
				continue
			}
			seen[childID] = struct{}{}
			ids = append(ids, childID)
		}
	}
	return ids
}

// sessionDeleteCascade returns rootID plus the descendants to delete (BFS order),
// and the busy descendants that must be detached and kept. A busy descendant is
// not traversed, so its own subtree survives with it.
func sessionDeleteCascade(all []*session.Session, rootID string, isBusy func(*session.Session) bool) ([]string, []*session.Session) {
	childrenByParent := make(map[string][]*session.Session)
	for _, item := range all {
		if item == nil || item.ParentID == nil {
			continue
		}
		parentID := strings.TrimSpace(*item.ParentID)
		if parentID != "" {
			childrenByParent[parentID] = append(childrenByParent[parentID], item)
		}
	}
	ids := []string{rootID}
	var survivors []*session.Session
	seen := map[string]struct{}{rootID: {}}
	for i := 0; i < len(ids); i++ {
		for _, child := range childrenByParent[ids[i]] {
			if _, ok := seen[child.ID]; ok {
				continue
			}
			seen[child.ID] = struct{}{}
			if isBusy(child) {
				survivors = append(survivors, child)
				continue
			}
			ids = append(ids, child.ID)
		}
	}
	return ids, survivors
}
