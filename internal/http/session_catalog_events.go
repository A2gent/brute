package http

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/session"
)

const (
	// Replay window for reconnecting clients; older cursors get a resync.
	sessionCatalogRingSize = 512
	// A subscriber that falls this far behind gets one resync instead of an
	// unbounded queue: the client reloads, so nothing is silently lost.
	sessionCatalogMaxBacklog = 4096
)

// SessionCatalogEvent carries the full sessions-list row so Caesar can patch
// its store without re-fetching /sessions. ID ("<epoch>-<seq>") mirrors the SSE
// id line and is the Last-Event-ID a client sends back on reconnect.
type SessionCatalogEvent struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id,omitempty"`
	Seq          uint64                      `json:"seq,omitempty"`
	At           string                      `json:"at,omitempty"`
	SessionID    string                      `json:"session_id,omitempty"`
	ProjectID    string                      `json:"project_id,omitempty"`
	Status       string                      `json:"status,omitempty"`
	Title        string                      `json:"title,omitempty"`
	Session      *SessionListItem            `json:"session,omitempty"`
	Notification *SessionCatalogNotification `json:"notification,omitempty"`
	// Full on resync means the cursor is from another server run, so
	// updated_since cannot be trusted and the client must reload everything.
	Full bool `json:"full,omitempty"`
}

// sessionCatalogMetadataKeys are the metadata keys Caesar's session lists read
// (project sessions page and chat parent/child index). Event rows equal a
// /sessions row requested with include_metadata and these metadata_keys, so
// clients can patch those lists without re-fetching them.
var sessionCatalogMetadataKeys = map[string]struct{}{
	"source": {}, "created_by": {}, "plan_spec": {}, "inline_review": {},
	"queue_mode": {}, "queue_paused": {}, "needs_feedback": {}, "team_id": {},
	"team_run_id": {}, "team_role": {}, "team_message_count": {}, "team_talking_line": {},
	"launch_target": {}, "link_type": {}, "sub_agent_id": {}, "session_action_source_key": {},
	"session_action_card_key": {}, "session_action_prompt_hash": {}, "title": {}, "severity": {},
	"files": {}, "continuation_reason": {}, "from_provider": {}, "from_model": {},
}

type sessionCatalogChange struct {
	at        time.Time
	deleted   bool
	projectID string
}

type sessionCatalogHub struct {
	mu    sync.Mutex
	epoch string
	seq   uint64
	ring  []SessionCatalogEvent
	subs  map[*sessionCatalogSubscription]struct{}
	// changed records the last change per session since boot. updated_since
	// uses it for writes that do not bump updated_at and to report deletions.
	changed     map[string]sessionCatalogChange
	lastPayload map[string][]byte
}

func newSessionCatalogHub() *sessionCatalogHub {
	return &sessionCatalogHub{
		epoch:       strconv.FormatInt(time.Now().UnixNano(), 36),
		subs:        make(map[*sessionCatalogSubscription]struct{}),
		changed:     make(map[string]sessionCatalogChange),
		lastPayload: make(map[string][]byte),
	}
}

func (s *Server) catalogHub() *sessionCatalogHub {
	s.sessionCatalogMu.Lock()
	defer s.sessionCatalogMu.Unlock()
	if s.sessionCatalog == nil {
		s.sessionCatalog = newSessionCatalogHub()
	}
	return s.sessionCatalog
}

func (h *sessionCatalogHub) cursorLocked() string {
	return h.epoch + "-" + strconv.FormatUint(h.seq, 10)
}

// controlEventLocked builds catalog_ready/resync markers positioned at the
// current cursor so the client resumes after them.
func (h *sessionCatalogHub) controlEventLocked(eventType string, full bool) SessionCatalogEvent {
	return SessionCatalogEvent{
		Type: eventType,
		ID:   h.cursorLocked(),
		Seq:  h.seq,
		At:   time.Now().UTC().Format(time.RFC3339Nano),
		Full: full,
	}
}

func (h *sessionCatalogHub) publish(event SessionCatalogEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	if event.SessionID != "" && event.Type != "webapp_notification" {
		if event.Type == "session_deleted" {
			delete(h.lastPayload, event.SessionID)
		} else if event.Session != nil {
			// Saves that do not change any list field must not wake every client.
			payload, err := json.Marshal(event.Session)
			if err == nil && bytes.Equal(payload, h.lastPayload[event.SessionID]) {
				return
			}
			h.lastPayload[event.SessionID] = payload
			// WHY: metadata values (files, plan_spec) may be shared with the live
			// session; SSE writers marshal later on other goroutines, so publish a
			// deep copy decoded from the bytes marshalled on the saving goroutine.
			var detached SessionListItem
			if err == nil && json.Unmarshal(payload, &detached) == nil {
				event.Session = &detached
			}
		}
		h.changed[event.SessionID] = sessionCatalogChange{at: now, deleted: event.Type == "session_deleted", projectID: event.ProjectID}
	}
	h.seq++
	event.Seq = h.seq
	event.ID = h.cursorLocked()
	event.At = now.Format(time.RFC3339Nano)
	if len(h.ring) == sessionCatalogRingSize {
		copy(h.ring, h.ring[1:])
		h.ring = h.ring[:len(h.ring)-1]
	}
	h.ring = append(h.ring, event)
	for sub := range h.subs {
		if sub.wants(event) {
			sub.enqueue(event)
		}
	}
}

// subscribe registers a subscriber. With resume set, it atomically queues the
// replay (or a resync) and catalog_ready, so no event can fall between the
// replayed history and live delivery.
func (h *sessionCatalogHub) subscribe(projectID, lastEventID string, resume bool) *sessionCatalogSubscription {
	sub := newSessionCatalogSubscription(projectID)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[sub] = struct{}{}
	if !resume {
		return sub
	}
	lastEventID = strings.TrimSpace(lastEventID)
	if lastEventID == "" {
		sub.enqueue(h.controlEventLocked("catalog_ready", false))
		return sub
	}
	epoch, rawSeq, ok := strings.Cut(lastEventID, "-")
	seq, err := strconv.ParseUint(rawSeq, 10, 64)
	if !ok || err != nil || epoch != h.epoch || seq > h.seq {
		sub.enqueue(h.controlEventLocked("resync", true))
		return sub
	}
	oldest := h.seq + 1 - uint64(len(h.ring))
	if seq+1 < oldest {
		sub.enqueue(h.controlEventLocked("resync", false))
		return sub
	}
	for _, event := range h.ring {
		if event.Seq > seq && sub.wants(event) {
			sub.enqueue(event)
		}
	}
	sub.enqueue(h.controlEventLocked("catalog_ready", false))
	return sub
}

func (h *sessionCatalogHub) unsubscribe(sub *sessionCatalogSubscription) {
	h.mu.Lock()
	_, ok := h.subs[sub]
	delete(h.subs, sub)
	h.mu.Unlock()
	if ok {
		sub.close()
	}
}

// changesSince reports sessions changed at or after since, keyed by ID.
func (h *sessionCatalogHub) changesSince(since time.Time) map[string]sessionCatalogChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]sessionCatalogChange)
	for id, change := range h.changed {
		if !change.at.Before(since) {
			out[id] = change
		}
	}
	return out
}

// sessionCatalogSubscription owns its backlog so a slow SSE socket cannot block
// publishers or other subscribers (same model as sessionEventSubscription).
type sessionCatalogSubscription struct {
	projectID string
	events    chan SessionCatalogEvent
	mu        sync.Mutex
	pending   []SessionCatalogEvent
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
}

func newSessionCatalogSubscription(projectID string) *sessionCatalogSubscription {
	sub := &sessionCatalogSubscription{
		projectID: projectID,
		events:    make(chan SessionCatalogEvent, 128),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go sub.deliver()
	return sub
}

func (s *sessionCatalogSubscription) wants(event SessionCatalogEvent) bool {
	return s.projectID == "" || event.ProjectID == s.projectID
}

func (s *sessionCatalogSubscription) enqueue(event SessionCatalogEvent) {
	s.mu.Lock()
	if len(s.pending) >= sessionCatalogMaxBacklog {
		// Replace the stale backlog with one resync at this event's cursor; the
		// client refetches updated_since and continues from here.
		s.pending = append(s.pending[:0], SessionCatalogEvent{Type: "resync", ID: event.ID, Seq: event.Seq, At: event.At})
	} else {
		s.pending = append(s.pending, event)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sessionCatalogSubscription) deliver() {
	defer close(s.done)
	defer close(s.events)
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-s.stop:
				return
			}
		}
		event := s.pending[0]
		s.pending = s.pending[1:]
		if len(s.pending) == 0 {
			s.pending = nil
		}
		s.mu.Unlock()
		select {
		case s.events <- event:
		case <-s.stop:
			return
		}
	}
}

func (s *sessionCatalogSubscription) close() {
	close(s.stop)
	<-s.done
}

// SubscribeSessionCatalogEvents streams live catalog events (no replay).
func (s *Server) SubscribeSessionCatalogEvents(projectID string) (<-chan SessionCatalogEvent, func()) {
	if s == nil {
		events := make(chan SessionCatalogEvent)
		close(events)
		return events, func() {}
	}
	hub := s.catalogHub()
	sub := hub.subscribe(projectID, "", false)
	return sub.events, func() { hub.unsubscribe(sub) }
}

func (s *Server) publishSessionCatalogEvent(event SessionCatalogEvent) {
	if s == nil || event.Type == "" || event.Type == "heartbeat" || event.Type == "catalog_ready" || event.Type == "resync" {
		return
	}
	s.catalogHub().publish(event)
}

// handleSessionChange is the session manager's post-commit hook and the producer
// of catalog row events. Tool results separately publish lightweight notifications.
func (s *Server) handleSessionChange(change session.Change) {
	if s == nil || change.ID == "" {
		return
	}
	if change.Kind == session.ChangeDeleted {
		s.publishSessionCatalogEvent(SessionCatalogEvent{Type: "session_deleted", SessionID: change.ID, ProjectID: change.ProjectID})
		return
	}
	sess := change.Session
	if sess == nil {
		loaded, err := s.sessionManager.GetForDisplay(change.ID, false)
		if err != nil {
			return
		}
		sess = loaded
	}
	item := s.sessionListItem(sess)
	// filterSessionListMetadata copies the selected keys, so the event never
	// shares the live metadata map with a running agent.
	item.Metadata = filterSessionListMetadata(sess.Metadata, sessionCatalogMetadataKeys)
	eventType := "session_updated"
	if change.Kind == session.ChangeCreated {
		eventType = "session_created"
	}
	s.publishSessionCatalogEvent(SessionCatalogEvent{
		Type:      eventType,
		SessionID: item.ID,
		ProjectID: item.ProjectID,
		Status:    item.Status,
		Title:     item.Title,
		Session:   &item,
	})
}
