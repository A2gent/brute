package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

const promptEditTTL = 60 * time.Second
const promptEditTokenKey = "prompt_edit_token_hash"
const promptEditExpiryKey = "prompt_edit_expires"

func sessionPromptEditing(sess *session.Session) bool {
	expiry, _ := sess.Metadata[promptEditExpiryKey].(string)
	until, err := time.Parse(time.RFC3339Nano, expiry)
	return err == nil && time.Now().Before(until)
}

// Edits use an expiring lease, not queue_paused: closing a browser must never
// leave the queue permanently paused or clear a pause the user set earlier.
func (s *Server) handleSessionPromptEdit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "sessionID")
	var req struct {
		Token   string `json:"token"`
		Action  string `json:"action"`
		Content string `json:"content"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Token) == "" {
		s.errorResponse(w, 400, "An edit token is required")
		return
	}
	if req.Action != "begin" && req.Action != "renew" && req.Action != "cancel" && req.Action != "save" {
		s.errorResponse(w, 400, "Invalid edit action")
		return
	}
	_, release, err := s.sessionManager.AcquireRun(r.Context(), id)
	if err != nil {
		s.errorResponse(w, 409, "Session has already started or is busy")
		return
	}
	resumeScope := ""
	defer func() {
		release()
		if resumeScope != "" {
			s.triggerSerialSessionQueue(resumeScope)
		}
	}()
	sess, err := s.sessionManager.Get(id)
	if err != nil {
		s.errorResponse(w, 404, "Session not found")
		return
	}
	if sess.Status != session.StatusQueued || len(sess.Messages) != 1 || sess.Messages[0].Role != "user" {
		s.errorResponse(w, 409, "Only an unstarted session's initial message can be edited")
		return
	}
	digest := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(digest[:])
	token, _ := sess.Metadata[promptEditTokenKey].(string)
	active := sessionPromptEditing(sess)
	if (active && token != tokenHash) || (req.Action != "begin" && (token != tokenHash || !active)) {
		s.errorResponse(w, 409, "Edit lease expired or belongs to another editor")
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	if req.Action == "begin" || req.Action == "renew" {
		sess.Metadata[promptEditTokenKey] = tokenHash
		sess.Metadata[promptEditExpiryKey] = time.Now().Add(promptEditTTL).UTC().Format(time.RFC3339Nano)
	} else {
		if req.Action == "save" {
			if strings.TrimSpace(req.Content) == "" {
				s.errorResponse(w, 400, "Message cannot be empty")
				return
			}
			sess.Messages[0].Content = req.Content
			sess.SetTitle(session.SummaryFromContent(req.Content))
			sess.Summary = session.SummaryFromContent(req.Content)
		}
		delete(sess.Metadata, promptEditTokenKey)
		delete(sess.Metadata, promptEditExpiryKey)
	}
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, 500, "Failed to update prompt edit")
		return
	}
	if req.Action == "begin" || req.Action == "renew" {
		time.AfterFunc(promptEditTTL, func() {
			fresh, err := s.sessionManager.Get(id)
			if err == nil && !sessionPromptEditing(fresh) {
				s.triggerSerialSessionQueueForSession(fresh)
			}
		})
	} else {
		// The worker must acquire the run lease after this mutation completes.
		if sessionIsSerialQueuedAutoRun(sess) {
			resumeScope = serialQueueScopeForSession(sess)
		}
	}
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
