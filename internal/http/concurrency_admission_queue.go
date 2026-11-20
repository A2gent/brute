package http

import (
	"encoding/json"
	"net/http"

	"github.com/A2gent/brute/internal/session"
)

// Marks a session queued only because a concurrency limit was full when it tried to start.
const sessionAwaitsAdmissionKey = "queue_admission"

func sessionAwaitsAdmission(sess *session.Session) bool {
	if sess == nil || sess.Metadata == nil {
		return false
	}
	awaiting, _ := sess.Metadata[sessionAwaitsAdmissionKey].(bool)
	return awaiting
}

// queueSessionForAdmission parks a session that could not start: it reuses the serial auto-run
// machinery, so Caesar shows it as queued and a queue worker starts it once a slot frees up.
func (s *Server) queueSessionForAdmission(sess *session.Session) error {
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[sessionQueueModeMetadataKey] = sessionQueueModeSerial
	sess.Metadata[sessionQueueAutoStartKey] = true
	sess.Metadata[sessionAwaitsAdmissionKey] = true
	sess.SetStatus(session.StatusQueued)
	if err := s.sessionManager.Save(sess); err != nil {
		return err
	}
	s.triggerSerialSessionQueue(serialQueueAdmissionScope)
	return nil
}

// clearSessionAdmissionQueue drops the queue markers once the session starts, so it behaves
// like an ordinary chat afterwards instead of re-entering a queue on its next message.
func clearSessionAdmissionQueue(sess *session.Session) {
	delete(sess.Metadata, sessionAwaitsAdmissionKey)
	delete(sess.Metadata, sessionQueueModeMetadataKey)
	delete(sess.Metadata, sessionQueueAutoStartKey)
}

func lastUserMessage(sess *session.Session) (string, bool) {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]
		if msg.Role == "user" && (msg.Content != "" || len(msg.Images) > 0) {
			return msg.Content, true
		}
	}
	return "", false
}

func (s *Server) respondChatStreamQueued(w http.ResponseWriter, sess *session.Session) {
	if err := s.queueSessionForAdmission(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to queue session: "+err.Error())
		return
	}
	event := ChatStreamEvent{Type: "done", Messages: s.messagesToResponse(sess.Messages), Status: string(sess.Status)}
	s.publishSessionEvent(sess.ID, event)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(event)
}
