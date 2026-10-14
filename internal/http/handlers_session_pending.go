package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

// Queued messages are delivered by the server, not the browser: they live in their
// own table, and the last active run ending (unregisterActiveSessionRun) promotes the
// oldest one into a new run. Closing the UI therefore never strands them.

func (s *Server) pendingMessagesResponse(sessionID string) []PendingMessageResponse {
	out := []PendingMessageResponse{}
	pending, err := s.sessionManager.ListPendingMessages(sessionID)
	if err != nil {
		logging.Warn("Failed to list queued messages: session=%s error=%v", sessionID, err)
		return out
	}
	for _, msg := range pending {
		item := PendingMessageResponse{ID: msg.ID, Content: msg.Content, CreatedAt: msg.CreatedAt.Format(time.RFC3339)}
		for _, img := range msg.Images {
			item.Images = append(item.Images, MessageImagePayload{
				Name: img.Name, MediaType: img.MediaType, DataBase64: img.DataBase64, URL: img.URL,
			})
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) publishPendingMessages(sessionID string) []PendingMessageResponse {
	pending := s.pendingMessagesResponse(sessionID)
	s.publishSessionEvent(sessionID, ChatStreamEvent{Type: "queue_updated", PendingMessages: pending})
	return pending
}

func (s *Server) handleListPendingMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if _, err := s.sessionManager.Get(sessionID); err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"messages": s.pendingMessagesResponse(sessionID)})
}

func (s *Server) handleQueuePendingMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	images, err := normalizeIncomingImages(req.Images)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid images payload: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(images) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Message or images are required")
		return
	}

	if _, err := s.sessionManager.QueuePendingMessage(sessionID, req.Message, images); err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to queue message: "+err.Error())
		return
	}
	logging.LogSession("message_queued", sessionID, "via HTTP")
	pending := s.publishPendingMessages(sessionID)
	// If the run already ended between the client's check and now, don't strand it.
	go s.drainSessionPendingMessages(sessionID)
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "messages": pending})
}

func (s *Server) handleDeletePendingMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	messageID := chi.URLParam(r, "messageID")

	if _, err := s.sessionManager.TakePendingMessage(sessionID, messageID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete queued message: "+err.Error())
		return
	}
	pending := s.publishPendingMessages(sessionID)
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "messages": pending})
}

// handleSteerPendingMessage promotes a queued message into the active run. The agent
// loop reads injected notes before building its next LLM request, so the message takes
// effect after the in-flight model call/tool batch instead of waiting for the run to end.
// If the run already ended, it simply starts a new run with the message.
func (s *Server) handleSteerPendingMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	messageID := chi.URLParam(r, "messageID")

	s.pendingDrainMu.Lock()
	msg, err := s.sessionManager.TakePendingMessage(sessionID, messageID)
	if err != nil {
		s.pendingDrainMu.Unlock()
		s.errorResponse(w, http.StatusInternalServerError, "Failed to steer queued message: "+err.Error())
		return
	}
	if msg == nil {
		s.pendingDrainMu.Unlock()
		s.errorResponse(w, http.StatusNotFound, "Queued message not found (it may have already been delivered)")
		return
	}

	if s.activeSessionRunCount(sessionID) > 0 {
		s.pendingDrainMu.Unlock()
		sess, injectErr := s.sessionManager.InjectUserMessage(sessionID, msg.Content, msg.Images)
		if injectErr != nil {
			// Put it back at the tail rather than losing the user's text.
			_, _ = s.sessionManager.QueuePendingMessage(sessionID, msg.Content, msg.Images)
			s.publishPendingMessages(sessionID)
			s.errorResponse(w, http.StatusInternalServerError, "Failed to steer message: "+injectErr.Error())
			return
		}
		logging.LogSession("message_steered", sessionID, "queued message promoted into active run")
		s.publishSessionEvent(sessionID, s.sessionSnapshotStreamEvent(sess))
		defer s.queueTelegramSessionMessageSync(sessionID)
	} else if !s.startPendingRunLocked(sessionID, msg) {
		// startPendingRunLocked released the lock; restore the message.
		_, _ = s.sessionManager.QueuePendingMessage(sessionID, msg.Content, msg.Images)
		s.publishPendingMessages(sessionID)
		s.errorResponse(w, http.StatusConflict, "Session could not start a new run")
		return
	}

	pending := s.publishPendingMessages(sessionID)
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "messages": pending})
}

// drainSessionPendingMessages starts a run for the oldest queued message when the
// session is idle after a normal completion. Failed, paused (user stop) and
// input-required sessions keep their queue so nothing runs behind the user's back.
func (s *Server) drainSessionPendingMessages(sessionID string) {
	if s == nil || s.sessionManager == nil || sessionID == "" {
		return
	}
	s.pendingDrainMu.Lock()
	if s.activeSessionRunCount(sessionID) > 0 {
		s.pendingDrainMu.Unlock()
		return
	}
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil || sess.Status != session.StatusCompleted {
		s.pendingDrainMu.Unlock()
		return
	}
	msg, err := s.sessionManager.TakeNextPendingMessage(sessionID)
	if err != nil || msg == nil {
		s.pendingDrainMu.Unlock()
		return
	}
	if !s.startPendingRunLocked(sessionID, msg) {
		_, _ = s.sessionManager.QueuePendingMessage(sessionID, msg.Content, msg.Images)
	}
	s.publishPendingMessages(sessionID)
}

// startPendingRunLocked appends msg as a user turn and starts a background run.
// Caller must hold pendingDrainMu; it is always released before returning.
func (s *Server) startPendingRunLocked(sessionID string, msg *session.PendingMessage) bool {
	leaseCtx, release, err := s.sessionManager.AcquireRun(s.sessionRunParentContext(), sessionID)
	if err != nil {
		s.pendingDrainMu.Unlock()
		return false
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.pendingDrainMu.Unlock()
		return false
	}
	if t := session.CurrentProviderTransition(sess); t != nil && !t.Terminal() {
		s.pendingDrainMu.Unlock()
		return false
	}
	sess.AddUserMessageWithImages(msg.Content, msg.Images)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.pendingDrainMu.Unlock()
		logging.Warn("Failed to start queued message run: session=%s error=%v", sessionID, err)
		return false
	}

	runCtx, cancelRun := context.WithCancel(leaseCtx)
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	// Registered while still holding the lock, so a concurrent drain sees a live run.
	s.pendingDrainMu.Unlock()

	s.publishSessionCatalog("session_updated", sess)
	s.publishSessionEvent(sessionID, s.sessionSnapshotStreamEvent(sess))
	logging.LogSession("queued_message_started", sessionID, "queued message promoted to a new run")

	transferred = true
	go func() {
		defer release()
		defer s.queueTelegramSessionMessageSync(sess.ID)
		defer func() {
			cancelRun()
			s.unregisterActiveSessionRun(sessionID, runID)
		}()
		result, runErr := s.runSessionWithoutStreaming(runCtx, sess, msg.Content)
		if finalizeErr := s.finalizeSessionRunWithoutStreaming(runCtx, sess, result, runErr); finalizeErr != nil {
			logging.Warn("Queued message run failed: session=%s error=%v", sessionID, finalizeErr)
		}
	}()
	return true
}
