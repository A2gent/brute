// handlers_session_archive.go implements "Complete" (soft delete) for sessions:
// archived sessions are hidden from the default list and later reviewed by the
// built-in archived-sessions review loop.
package http

import (
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
	s.setSessionArchived(w, chi.URLParam(r, "sessionID"), true)
}

func (s *Server) handleUnarchiveSession(w http.ResponseWriter, r *http.Request) {
	s.setSessionArchived(w, chi.URLParam(r, "sessionID"), false)
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

func (s *Server) setSessionArchived(w http.ResponseWriter, sessionID string, archived bool) {
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

	// Archive the whole subtree like delete does, so child sessions do not
	// resurface as orphaned roots in the main list.
	ids := []string{root.ID}
	if all, listErr := s.sessionManager.List(); listErr != nil {
		logging.Warn("Failed to list sessions before archive cascade for %s: %v", sessionID, listErr)
	} else {
		ids = sessionSubtreeIDs(all, root.ID)
	}

	now := time.Now()
	for _, id := range ids {
		sess := root
		if id != root.ID {
			child, getErr := s.sessionManager.Get(id)
			if getErr != nil || (archived && s.sessionIsBusy(child)) {
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
		s.publishSessionCatalog("session_updated", sess)
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
