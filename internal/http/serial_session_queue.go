package http

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const serialQueueGlobalScope = "__global__"

const serialQueueWorktreeRetryDelay = 3 * time.Second

// Project setting that raises how many queued sessions may run at once within a project.
const (
	projectMaxParallelSessionsKey = "max_parallel_sessions"
	maxParallelSessionsLimit      = 10
)

// serialQueueLimitForScope returns the number of concurrent queue workers allowed for a scope.
// Defaults to 1 (strictly serial) for the global scope and for projects without the setting.
func (s *Server) serialQueueLimitForScope(scope string) int {
	if scope == serialQueueGlobalScope || s.store == nil {
		return 1
	}
	project, err := s.store.GetProject(scope)
	if err != nil || project == nil {
		return 1
	}
	return parseMaxParallelSessions(project.Settings[projectMaxParallelSessionsKey])
}

func parseMaxParallelSessions(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 1
	}
	if n > maxParallelSessionsLimit {
		return maxParallelSessionsLimit
	}
	return n
}

func (s *Server) triggerSerialSessionQueueForSession(sess *session.Session) {
	if !sessionIsSerialQueuedAutoRun(sess) {
		return
	}
	s.triggerSerialSessionQueue(serialQueueScopeForSession(sess))
}

func (s *Server) triggerSerialSessionQueueIfAdvanceable(sess *session.Session) {
	if sess.WriteRequested() && sess.Status == session.StatusQueued {
		s.triggerSerialSessionQueueForSession(sess)
		return
	}
	if !sessionIsSerialQueuedAutoRun(sess) || !serialQueueCanAdvanceAfterStatus(sess.Status) {
		return
	}
	s.triggerSerialSessionQueueForSession(sess)
}

// triggerSerialSessionQueue tops up the scope's workers to its parallel limit.
// Extra workers exit immediately when there is nothing left to claim.
func (s *Server) triggerSerialSessionQueue(scope string) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = serialQueueGlobalScope
	}
	limit := s.serialQueueLimitForScope(scope)

	s.serialQueueMu.Lock()
	if s.serialQueueWorkers == nil {
		s.serialQueueWorkers = make(map[string]int)
	}
	spawn := limit - s.serialQueueWorkers[scope]
	if spawn > 0 {
		s.serialQueueWorkers[scope] += spawn
	}
	s.serialQueueMu.Unlock()

	for i := 0; i < spawn; i++ {
		go s.runSerialSessionQueueWorker(s.sessionRunParentContext(), scope)
	}
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
			// A session may have been queued between the lookup and the clear; peek without
			// claiming, otherwise the claim would leak and no worker could pick it up.
			if s.hasUnclaimedSerialQueuedSession(scope) {
				s.triggerSerialSessionQueue(scope)
			}
			return
		}

		shouldContinue, waitForSlot := s.runSerialQueuedSession(ctx, next.ID)
		s.releaseSerialQueueClaim(next.ID)
		if waitForSlot {
			// WHY here: waiting inside runSerialQueuedSession held the session's run lease,
			// so a manual Start of that queued session failed with "Session is busy".
			select {
			case <-ctx.Done():
				shouldContinue = false
			case <-time.After(serialQueueWorktreeRetryDelay):
			}
		}
		if !shouldContinue {
			s.clearSerialSessionQueueWorker(scope)
			return
		}
	}
}

func (s *Server) clearSerialSessionQueueWorker(scope string) {
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	if s.serialQueueWorkers[scope] <= 1 {
		delete(s.serialQueueWorkers, scope)
		return
	}
	s.serialQueueWorkers[scope]--
}

func (s *Server) releaseSerialQueueClaim(sessionID string) {
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	delete(s.serialQueueClaimed, sessionID)
}

func (s *Server) nextSerialQueuedSession(scope string) (*session.Session, error) {
	candidates, err := s.serialQueueCandidates(scope)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	// Parallel workers share a scope: claim atomically so two never pick the same session
	// in the window before it is marked running.
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	if s.serialQueueClaimed == nil {
		s.serialQueueClaimed = make(map[string]struct{})
	}
	for _, c := range candidates {
		if _, taken := s.serialQueueClaimed[c.ID]; taken {
			continue
		}
		s.serialQueueClaimed[c.ID] = struct{}{}
		return c, nil
	}
	return nil, nil
}

// hasUnclaimedSerialQueuedSession reports pending work without claiming it.
func (s *Server) hasUnclaimedSerialQueuedSession(scope string) bool {
	candidates, err := s.serialQueueCandidates(scope)
	if err != nil {
		return false
	}
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	for _, c := range candidates {
		if _, taken := s.serialQueueClaimed[c.ID]; !taken {
			return true
		}
	}
	return false
}

// serialQueueCandidates lists runnable queued sessions of a scope, oldest first.
func (s *Server) serialQueueCandidates(scope string) ([]*session.Session, error) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		return nil, err
	}
	candidates := make([]*session.Session, 0)
	for _, sess := range sessions {
		if sess == nil || sess.Status != session.StatusQueued {
			continue
		}
		if sessionIsQueuePaused(sess) || sessionPromptEditing(sess) {
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
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	return candidates, nil
}

func serialQueueScopeForSession(sess *session.Session) string {
	if sess != nil && sess.ProjectID != nil {
		if projectID := strings.TrimSpace(*sess.ProjectID); projectID != "" {
			return projectID
		}
	}
	return serialQueueGlobalScope
}

// runSerialQueuedSession returns whether the worker may continue and whether it should
// wait before the next attempt because all worktree slots are taken.
func (s *Server) runSerialQueuedSession(ctx context.Context, sessionID string) (bool, bool) {
	ctx, release, leaseErr := s.sessionManager.AcquireRun(ctx, sessionID)
	if leaseErr != nil {
		return true, false
	}
	defer release()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		logging.Warn("Serial session queue could not load session %s: %v", sessionID, err)
		return true, false
	}
	if sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) || sessionIsQueuePaused(sess) || sessionPromptEditing(sess) {
		return true, false
	}

	prompt, _, ok := firstQueuedUserMessage(sess)
	if !ok {
		sess.AddAssistantMessage("Queued serial session could not start because it has no initial user message.", nil)
		sess.SetStatus(session.StatusFailed)
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Serial session queue failed to mark session %s failed: %v", sess.ID, err)
			return true, false
		}
		return true, false
	}

	// The worktree is created only now, at start time. With all slots taken the session stays
	// queued; this worker waits (keeping FIFO order) and retries until a slot is freed.
	if sess.ProjectID != nil && (sess.ParentID == nil || *sess.ParentID == "") && s.sessionWorktree(sess) == nil {
		if err := s.createSessionWorktree(sess.ID, *sess.ProjectID, ""); err != nil {
			if errors.Is(err, storage.ErrNoFreeWorktreeSlot) {
				return true, true
			}
			logging.Warn("Serial session queue could not create worktree for %s: %v", sess.ID, err)
			sess.AddAssistantMessage("Queued session could not start: failed to create worktree: "+err.Error(), nil)
			sess.SetStatus(session.StatusFailed)
			if saveErr := s.sessionManager.Save(sess); saveErr != nil {
				logging.Warn("Serial session queue failed to mark session %s failed: %v", sess.ID, saveErr)
			}
			return true, false
		}
	}

	// Only the project queue owner may lift the read-only execution guard.
	if sess.WriteRequested() {
		delete(sess.Metadata, session.RunModeReadOnlyKey)
		// The agent consumes the request marker to send a transient grant prompt.
	}
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Serial session queue failed to mark session %s running: %v", sess.ID, err)
		return true, false
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
	if finalizeErr := s.finalizeSessionRunWithoutStreaming(runCtx, sess, result, runErr); finalizeErr != nil {
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
		return true, false
	}
	return serialQueueCanAdvanceAfterStatus(fresh.Status), false
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
	case session.StatusQueued, session.StatusRunning:
		return false
	default:
		// Serial queues should only wait for sessions that are still runnable or running.
		// Paused/input-required/external-waiting sessions are inactive and must not block later queued work.
		return true
	}
}
