package http

import (
	"context"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const serialQueueGlobalScope = "__global__"

func (s *Server) triggerSerialSessionQueueForSession(sess *session.Session) {
	if !sessionIsSerialQueuedAutoRun(sess) {
		return
	}
	s.triggerSerialSessionQueue(serialQueueScopeForSession(sess))
}

func (s *Server) triggerSerialSessionQueueIfTerminal(sess *session.Session) {
	if !sessionIsSerialQueuedAutoRun(sess) || !serialQueueCanAdvanceAfterStatus(sess.Status) {
		return
	}
	s.triggerSerialSessionQueueForSession(sess)
}

func (s *Server) triggerSerialSessionQueue(scope string) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = serialQueueGlobalScope
	}

	s.serialQueueMu.Lock()
	if s.serialQueueWorkers == nil {
		s.serialQueueWorkers = make(map[string]struct{})
	}
	if _, running := s.serialQueueWorkers[scope]; running {
		s.serialQueueMu.Unlock()
		return
	}
	s.serialQueueWorkers[scope] = struct{}{}
	s.serialQueueMu.Unlock()

	go s.runSerialSessionQueueWorker(s.sessionRunParentContext(), scope)
}

func (s *Server) resumeSerialSessionQueues() {
	sessions, err := s.sessionManager.List()
	if err != nil {
		logging.Warn("Failed to resume serial session queues: %v", err)
		return
	}
	scopes := make(map[string]struct{})
	for _, sess := range sessions {
		if sess == nil || sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) {
			continue
		}
		scopes[serialQueueScopeForSession(sess)] = struct{}{}
	}
	for scope := range scopes {
		s.triggerSerialSessionQueue(scope)
	}
}

func (s *Server) runSerialSessionQueueWorker(ctx context.Context, scope string) {
	for {
		if ctx != nil && ctx.Err() != nil {
			s.clearSerialSessionQueueWorker(scope)
			return
		}

		next, err := s.nextSerialQueuedSession(scope)
		if err != nil {
			logging.Warn("Serial session queue lookup failed for scope %s: %v", scope, err)
			s.clearSerialSessionQueueWorker(scope)
			return
		}
		if next == nil {
			s.clearSerialSessionQueueWorker(scope)
			if retry, retryErr := s.nextSerialQueuedSession(scope); retryErr == nil && retry != nil {
				s.triggerSerialSessionQueue(scope)
			}
			return
		}

		if shouldContinue := s.runSerialQueuedSession(ctx, next.ID); !shouldContinue {
			s.clearSerialSessionQueueWorker(scope)
			return
		}
	}
}

func (s *Server) clearSerialSessionQueueWorker(scope string) {
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	delete(s.serialQueueWorkers, scope)
}

func (s *Server) nextSerialQueuedSession(scope string) (*session.Session, error) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		return nil, err
	}
	candidates := make([]*session.Session, 0)
	for _, sess := range sessions {
		if sess == nil || sess.Status != session.StatusQueued {
			continue
		}
		if !sessionIsSerialQueuedAutoRun(sess) {
			continue
		}
		if serialQueueScopeForSession(sess) != scope {
			continue
		}
		candidates = append(candidates, sess)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	return candidates[0], nil
}

func serialQueueScopeForSession(sess *session.Session) string {
	if sess != nil && sess.ProjectID != nil {
		if projectID := strings.TrimSpace(*sess.ProjectID); projectID != "" {
			return projectID
		}
	}
	return serialQueueGlobalScope
}

func (s *Server) runSerialQueuedSession(ctx context.Context, sessionID string) bool {
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		logging.Warn("Serial session queue could not load session %s: %v", sessionID, err)
		return true
	}
	if sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) {
		return true
	}

	prompt, _, ok := firstQueuedUserMessage(sess)
	if !ok {
		sess.AddAssistantMessage("Queued serial session could not start because it has no initial user message.", nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return true
	}

	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Serial session queue failed to mark session %s running: %v", sess.ID, err)
		return true
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)

	runCtx, cancelRun := context.WithCancel(ctx)
	runID := s.registerActiveSessionRun(sess.ID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sess.ID, runID)
	}()

	logging.LogSession("serial_queue_started", sess.ID, "queued serial session")
	result, runErr := s.runSessionWithoutStreaming(runCtx, sess, prompt)
	if finalizeErr := s.finalizeSessionRunWithoutStreaming(sess, result, runErr); finalizeErr != nil {
		if runErr != nil {
			_, message := s.sessionRunHTTPError(result, runErr)
			logging.Warn("Serial session queue run failed for session %s: %s", sess.ID, message)
		} else {
			logging.Warn("Serial session queue finalize failed for session %s: %v", sess.ID, finalizeErr)
		}
	}

	fresh, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		logging.Warn("Serial session queue failed to reload session %s: %v", sess.ID, err)
		return true
	}
	return serialQueueCanAdvanceAfterStatus(fresh.Status)
}

func firstQueuedUserMessage(sess *session.Session) (string, []session.ImageAttachment, bool) {
	if sess == nil {
		return "", nil, false
	}
	for _, msg := range sess.Messages {
		if msg.Role != "user" {
			continue
		}
		if strings.TrimSpace(msg.Content) == "" && len(msg.Images) == 0 {
			continue
		}
		return msg.Content, msg.Images, true
	}
	return "", nil, false
}

func serialQueueCanAdvanceAfterStatus(status session.Status) bool {
	switch status {
	case session.StatusCompleted, session.StatusFailed:
		return true
	default:
		return false
	}
}
