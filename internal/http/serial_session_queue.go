package http

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const serialQueueGlobalScope = "__global__"

// Project setting that raises how many queued sessions may run at once within a project.
const (
	projectMaxParallelSessionsKey = "max_parallel_sessions"
	maxParallelSessionsLimit      = 10
)

// serialQueueLimitForScope returns the number of concurrent queue workers allowed for a scope.
// Defaults to 1 (strictly serial) for the global scope and for projects without the setting.
// A named queue uses its own limit and falls back to the default queue limit once deleted.
func (s *Server) serialQueueLimitForScope(scope string) int {
	if scope == serialQueueGlobalScope || s.store == nil {
		return 1
	}
	projectID, queueName := splitSerialQueueScope(scope)
	project, err := s.store.GetProject(projectID)
	if err != nil || project == nil {
		return 1
	}
	if queueName != "" {
		if limit, ok := sessionQueueLimit(project.Settings, queueName); ok {
			return limit
		}
	}
	return parseMaxParallelSessions(project.Settings[projectMaxParallelSessionsKey])
}

// triggerProjectSessionQueues tops up every queue of a project, including named ones.
func (s *Server) triggerProjectSessionQueues(project *storage.Project) {
	if project == nil {
		return
	}
	s.triggerSerialSessionQueue(project.ID)
	queues, err := parseSessionQueues(project.Settings[projectSessionQueuesKey])
	if err != nil {
		logging.Warn("Skipped named session queues of project %s: %v", project.ID, err)
	}
	for _, queue := range queues {
		s.triggerSerialSessionQueue(serialQueueScope(project.ID, queue.Name))
	}
	// Always run the safety net: sessions may sit in scopes that settings no longer declare.
	s.triggerQueuedSessionScopes(project.ID)
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

// triggerQueuedSessionScopes tops up every queue that already holds queued sessions of a
// project, including queues removed from settings: those sessions fall back to the project
// default limit instead of waiting forever in a scope nobody triggers.
func (s *Server) triggerQueuedSessionScopes(projectID string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || s.sessionManager == nil {
		return
	}
	sessions, err := s.sessionManager.List()
	if err != nil {
		logging.Warn("Failed to list queued session scopes for project %s: %v", projectID, err)
		return
	}
	scopes := make(map[string]struct{})
	for _, sess := range sessions {
		if sess == nil || sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) || sessionArchivedAt(sess) != nil {
			continue
		}
		if sess.ProjectID == nil || strings.TrimSpace(*sess.ProjectID) != projectID {
			continue
		}
		scopes[serialQueueScopeForSession(sess)] = struct{}{}
	}
	for scope := range scopes {
		s.triggerSerialSessionQueue(scope)
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
		if sess == nil || sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) || sessionArchivedAt(sess) != nil {
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
		// A lowered limit must take effect while the queue is still draining, so surplus
		// workers retire before claiming more work instead of running out the old limit.
		if s.retireSurplusSerialQueueWorker(scope) {
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

		shouldContinue := s.runSerialQueuedSession(ctx, next.ID)
		s.releaseSerialQueueClaim(next.ID)
		if !shouldContinue {
			s.clearSerialSessionQueueWorker(scope)
			return
		}
	}
}

// retireSurplusSerialQueueWorker drops this worker when the scope has more workers than its
// current limit. Lowering a limit must apply while the queue drains, so surplus workers exit
// instead of running out the old limit. The check and the decrement share one lock so two
// finished workers cannot both retire and leave the scope with no worker at all.
func (s *Server) retireSurplusSerialQueueWorker(scope string) bool {
	limit := s.serialQueueLimitForScope(scope)
	s.serialQueueMu.Lock()
	defer s.serialQueueMu.Unlock()
	workers := s.serialQueueWorkers[scope]
	if workers <= limit {
		return false
	}
	if workers <= 1 {
		delete(s.serialQueueWorkers, scope)
		return true
	}
	s.serialQueueWorkers[scope] = workers - 1
	return true
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
		// Archive cascades to queued descendants: a completed tree stays dormant
		// until unarchived, instead of running hidden from the session list.
		if sessionArchivedAt(sess) != nil {
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
			return serialQueueScope(projectID, sessionQueueName(sess))
		}
	}
	return serialQueueGlobalScope
}

// runSerialQueuedSession returns whether the worker may continue.
func (s *Server) runSerialQueuedSession(ctx context.Context, sessionID string) bool {
	ctx, release, leaseErr := s.sessionManager.AcquireRun(ctx, sessionID)
	if leaseErr != nil {
		return true
	}
	defer release()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		logging.Warn("Serial session queue could not load session %s: %v", sessionID, err)
		return true
	}
	if sess.Status != session.StatusQueued || !sessionIsSerialQueuedAutoRun(sess) || sessionIsQueuePaused(sess) || sessionPromptEditing(sess) || sessionArchivedAt(sess) != nil {
		return true
	}

	prompt, _, ok := firstQueuedUserMessage(sess)
	if !ok {
		sess.AddAssistantMessage("Queued serial session could not start because it has no initial user message.", nil)
		sess.SetStatus(session.StatusFailed)
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Serial session queue failed to mark session %s failed: %v", sess.ID, err)
			return true
		}
		return true
	}

	// The worktree is created only now, at start time.
	if sess.ProjectID != nil && (sess.ParentID == nil || *sess.ParentID == "") && s.sessionWorktree(sess) == nil {
		branch, _ := sess.Metadata["worktree_branch"].(string)
		baseRef, _ := sess.Metadata["worktree_base_ref"].(string)
		if err := s.createSessionWorktree(sess.ID, *sess.ProjectID, branch, baseRef); err != nil {
			logging.Warn("Serial session queue could not create worktree for %s: %v", sess.ID, err)
			sess.AddAssistantMessage("Queued session could not start: failed to create worktree: "+err.Error(), nil)
			sess.SetStatus(session.StatusFailed)
			if saveErr := s.sessionManager.Save(sess); saveErr != nil {
				logging.Warn("Serial session queue failed to mark session %s failed: %v", sess.ID, saveErr)
			}
			return true
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
		return true
	}

	runCtx, finishRun := s.beginBackgroundSessionRun(ctx, sess.ID)
	logging.LogSession("serial_queue_started", sess.ID, "queued serial session")
	s.runBackgroundSession(runCtx, finishRun, sess, prompt)

	fresh, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		logging.Warn("Serial session queue failed to reload session %s: %v", sess.ID, err)
		return true
	}
	return serialQueueCanAdvanceAfterStatus(fresh.Status)
}

func (s *Server) beginBackgroundSessionRun(ctx context.Context, sessionID string) (context.Context, func()) {
	runCtx, cancelRun := context.WithCancel(ctx)
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	return runCtx, func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}
}

func (s *Server) runBackgroundSession(runCtx context.Context, finishRun func(), sess *session.Session, prompt string) {
	defer s.queueTelegramSessionMessageSync(sess.ID)
	defer finishRun()

	result, runErr := s.runSessionWithoutStreaming(runCtx, sess, prompt)
	if finalizeErr := s.finalizeSessionRunWithoutStreaming(runCtx, sess, result, runErr); finalizeErr != nil {
		if runErr != nil {
			_, message := s.sessionRunHTTPError(result, runErr)
			logging.Warn("Background session run failed for session %s: %s", sess.ID, message)
		} else {
			logging.Warn("Background session finalize failed for session %s: %v", sess.ID, finalizeErr)
		}
	}
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
