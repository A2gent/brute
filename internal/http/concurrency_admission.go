package http

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const (
	concurrencyResumeDebounce = 200 * time.Millisecond
	concurrencyWaitPoll       = 10 * time.Second
)

type sessionSlotInfo struct {
	child     bool
	projectID string
	queueName string
}

type concurrencyUsage struct {
	Running  int            `json:"running"`
	Projects map[string]int `json:"projects"`
	Queues   map[string]int `json:"queues"`
}

type concurrencyRuntime struct {
	mu            sync.Mutex
	cfg           *concurrencyConfig
	cfgGeneration uint64
	reserved      map[string]int
	info          map[string]sessionSlotInfo
	waiters       map[string]map[*slotWaiter]struct{}
	wake          chan struct{}
	resumePending atomic.Bool

	sampleMu sync.Mutex
	sample   *resourceReading
	sampler  func() resourceReading
}

type slotWaiter struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Server) concurrencyState() *concurrencyRuntime {
	s.concurrencyOnce.Do(func() {
		s.concurrency = &concurrencyRuntime{
			reserved: make(map[string]int),
			info:     make(map[string]sessionSlotInfo),
			waiters:  make(map[string]map[*slotWaiter]struct{}),
			wake:     make(chan struct{}),
			sampler:  sampleResourceReading,
		}
	})
	return s.concurrency
}

func slotInfoForSession(sess *session.Session) sessionSlotInfo {
	info := sessionSlotInfo{queueName: sessionQueueName(sess)}
	if sess.ParentID != nil && strings.TrimSpace(*sess.ParentID) != "" {
		info.child = true
	}
	if sess.ProjectID != nil {
		info.projectID = strings.TrimSpace(*sess.ProjectID)
	}
	return info
}

func (s *Server) activeRunSessionIDs() []string {
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()
	ids := make([]string, 0, len(s.activeRuns))
	for id := range s.activeRuns {
		ids = append(ids, id)
	}
	return ids
}

// usageLocked counts running top-level sessions: active runs plus slots reserved for runs
// that are about to start or run outside the active-run registry.
func (s *Server) usageLocked(st *concurrencyRuntime, exclude string) concurrencyUsage {
	usage := concurrencyUsage{Projects: map[string]int{}, Queues: map[string]int{}}
	ids := make(map[string]struct{}, len(st.reserved))
	for id := range st.reserved {
		ids[id] = struct{}{}
	}
	for _, id := range s.activeRunSessionIDs() {
		ids[id] = struct{}{}
	}
	for id := range st.info {
		if _, live := ids[id]; !live {
			delete(st.info, id)
		}
	}
	for id := range ids {
		if id == exclude {
			continue
		}
		info, ok := st.info[id]
		if !ok {
			info = s.lookupSlotInfo(id)
			st.info[id] = info
		}
		if info.child {
			continue
		}
		usage.Running++
		if info.projectID != "" {
			usage.Projects[info.projectID]++
		}
		if info.queueName != "" {
			usage.Queues[info.queueName]++
		}
	}
	return usage
}

func (s *Server) lookupSlotInfo(sessionID string) sessionSlotInfo {
	if s.sessionManager == nil {
		return sessionSlotInfo{}
	}
	sess, err := s.sessionManager.GetSummary(sessionID)
	if err != nil || sess == nil {
		return sessionSlotInfo{}
	}
	return slotInfoForSession(sess)
}

// admissionBlockReason returns why a session may not start now, or "" when it may.
func admissionBlockReason(info sessionSlotInfo, cfg concurrencyConfig, usage concurrencyUsage, resourceBlock string) string {
	if info.child {
		return ""
	}
	if cfg.MaxSessions > 0 && usage.Running >= cfg.MaxSessions {
		return "global session limit reached"
	}
	if limit := cfg.ProjectLimits[info.projectID]; info.projectID != "" && limit > 0 && usage.Projects[info.projectID] >= limit {
		return "project session limit reached"
	}
	if info.queueName != "" {
		if limit, ok := cfg.queueLimit(info.queueName); ok && usage.Queues[info.queueName] >= limit {
			return "queue " + info.queueName + " is full"
		}
	}
	return resourceBlock
}

func (s *Server) sessionAdmissionBlockReason(sess *session.Session) string {
	info := slotInfoForSession(sess)
	if info.child {
		return ""
	}
	cfg := s.loadConcurrencyConfig()
	resourceBlock := s.resourceBlockReason(cfg)
	st := s.concurrencyState()
	st.mu.Lock()
	defer st.mu.Unlock()
	return admissionBlockReason(info, cfg, s.usageLocked(st, sess.ID), resourceBlock)
}

// reserveSessionSlot admits a session and holds its slot until releaseSessionSlot.
// Child sessions always pass: a parent waiting on its child must never deadlock.
func (s *Server) reserveSessionSlot(sess *session.Session) bool {
	info := slotInfoForSession(sess)
	cfg := s.loadConcurrencyConfig()
	resourceBlock := ""
	if !info.child {
		resourceBlock = s.resourceBlockReason(cfg)
	}
	st := s.concurrencyState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if admissionBlockReason(info, cfg, s.usageLocked(st, sess.ID), resourceBlock) != "" {
		return false
	}
	st.reserved[sess.ID]++
	st.info[sess.ID] = info
	return true
}

func (s *Server) releaseSessionSlot(sessionID string) {
	st := s.concurrencyState()
	st.mu.Lock()
	if st.reserved[sessionID] <= 1 {
		delete(st.reserved, sessionID)
	} else {
		st.reserved[sessionID]--
	}
	st.mu.Unlock()
	s.notifySessionSlotFreed()
}

func (s *Server) notifySessionSlotFreed() {
	st := s.concurrencyState()
	st.mu.Lock()
	close(st.wake)
	st.wake = make(chan struct{})
	st.mu.Unlock()
	s.scheduleSessionQueueResume()
}

// scheduleSessionQueueResume coalesces bursts of run ends into one queue sweep.
func (s *Server) scheduleSessionQueueResume() {
	st := s.concurrencyState()
	if s.sessionManager == nil || !st.resumePending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(concurrencyResumeDebounce)
		st.resumePending.Store(false)
		s.resumeSerialSessionQueues()
	}()
}

// waitForSessionSlot blocks synchronous callers (Telegram, Slack) until the session is admitted.
// While waiting the session shows as queued and Stop cancels the wait without taking a slot.
func (s *Server) waitForSessionSlot(ctx context.Context, sess *session.Session) (func(), error) {
	release := func() { s.releaseSessionSlot(sess.ID) }
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.reserveSessionSlot(sess) {
		return release, nil
	}
	waitCtx, waiter := s.registerSlotWaiter(ctx, sess.ID)
	defer s.unregisterSlotWaiter(sess.ID, waiter)
	resumeStatus := sess.Status
	s.saveSlotWaitStatus(sess, session.StatusQueued)
	for {
		st := s.concurrencyState()
		st.mu.Lock()
		wake := st.wake
		st.mu.Unlock()
		if waitCtx.Err() == nil && s.reserveSessionSlot(sess) {
			if waitCtx.Err() == nil {
				s.saveSlotWaitStatus(sess, resumeStatus)
				return release, nil
			}
			s.releaseSessionSlot(sess.ID)
		}
		if err := waitCtx.Err(); err != nil {
			s.saveSlotWaitStatus(sess, session.StatusPaused)
			return nil, err
		}
		timer := time.NewTimer(concurrencyWaitPoll)
		select {
		case <-waitCtx.Done():
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (s *Server) saveSlotWaitStatus(sess *session.Session, status session.Status) {
	sess.SetStatus(status)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to save status %s of session %s waiting for a slot: %v", status, sess.ID, err)
	}
}

func (s *Server) registerSlotWaiter(ctx context.Context, sessionID string) (context.Context, *slotWaiter) {
	waitCtx, cancel := context.WithCancel(ctx)
	waiter := &slotWaiter{cancel: cancel, done: make(chan struct{})}
	st := s.concurrencyState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.waiters[sessionID] == nil {
		st.waiters[sessionID] = make(map[*slotWaiter]struct{})
	}
	st.waiters[sessionID][waiter] = struct{}{}
	return waitCtx, waiter
}

func (s *Server) unregisterSlotWaiter(sessionID string, waiter *slotWaiter) {
	st := s.concurrencyState()
	st.mu.Lock()
	delete(st.waiters[sessionID], waiter)
	if len(st.waiters[sessionID]) == 0 {
		delete(st.waiters, sessionID)
	}
	st.mu.Unlock()
	waiter.cancel()
	close(waiter.done)
}

// cancelSessionSlotWaiters stops waits for a slot and returns once each waiter saved its status.
func (s *Server) cancelSessionSlotWaiters(sessionID string) int {
	st := s.concurrencyState()
	st.mu.Lock()
	waiters := make([]*slotWaiter, 0, len(st.waiters[sessionID]))
	for waiter := range st.waiters[sessionID] {
		waiters = append(waiters, waiter)
	}
	st.mu.Unlock()
	for _, waiter := range waiters {
		waiter.cancel()
		<-waiter.done
	}
	return len(waiters)
}

func (s *Server) currentConcurrencyUsage() concurrencyUsage {
	st := s.concurrencyState()
	st.mu.Lock()
	defer st.mu.Unlock()
	return s.usageLocked(st, "")
}
