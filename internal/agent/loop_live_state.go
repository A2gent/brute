package agent

import (
	"strings"

	"github.com/A2gent/brute/internal/session"
)

func (a *Agent) mergeFreshSessionState(sess *session.Session) bool {
	_, changed := a.mergeFreshSessionLiveState(sess)
	return changed
}

func (a *Agent) mergeFreshSessionLiveState(sess *session.Session) (*session.Session, bool) {
	if a == nil || a.sessionManager == nil || sess == nil || strings.TrimSpace(sess.ID) == "" {
		return nil, false
	}
	tailID := ""
	if tail := sess.GetLastMessage(); tail != nil {
		tailID = tail.ID
	}
	fresh, err := a.sessionManager.GetLiveState(sess.ID, tailID)
	if err != nil || fresh == nil {
		return nil, false
	}
	return fresh, mergeSessionLiveState(sess, fresh)
}

func mergeSessionLiveState(sess, fresh *session.Session) bool {
	changed := false
	// WHY: injected user notes must never be lost when Save reconciles transcript
	// membership. The lightweight read includes even notes before an unsaved local
	// tail; deduplicate by ID and append them after local tool results to keep valid
	// assistant-tool ordering. Other DB messages are only the unseen suffix.
	if len(fresh.Messages) > 0 {
		existing := make(map[string]struct{}, len(sess.Messages))
		for _, msg := range sess.Messages {
			existing[msg.ID] = struct{}{}
		}
		for _, msg := range fresh.Messages {
			if _, ok := existing[msg.ID]; ok && msg.ID != "" {
				continue
			}
			existing[msg.ID] = struct{}{}
			sess.Messages = append(sess.Messages, msg)
			changed = true
		}
	}
	if fresh.Metadata != nil {
		merged := make(map[string]interface{}, len(fresh.Metadata)+len(sess.Metadata))
		for key, value := range fresh.Metadata {
			merged[key] = value
		}
		// In-flight token/provider metadata wins, but approval_audit is written by
		// the HTTP server during the turn, so the DB copy MUST win for that key.
		for key, value := range sess.Metadata {
			if key != "approval_audit" {
				merged[key] = value
			}
		}
		sess.Metadata = merged
	}
	sess.TaskProgress = fresh.TaskProgress
	if fresh.Status == session.StatusInputRequired || fresh.Status == session.StatusWaitingExternal {
		sess.Status = fresh.Status
	}
	if fresh.UpdatedAt.After(sess.UpdatedAt) {
		sess.UpdatedAt = fresh.UpdatedAt
	}
	return changed
}
