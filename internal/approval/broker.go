package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrSessionIDRequired      = errors.New("session id required")
	ErrRequestNotFound        = errors.New("request not found")
	ErrRequestAlreadyResolved = errors.New("request already resolved")
	ErrSessionMismatch        = errors.New("session mismatch")
	ErrTooManyPending         = errors.New("too many pending requests")
	ErrInputTooLarge          = errors.New("input too large")
	ErrInvalidDecision        = errors.New("invalid decision")
	ErrTimedOut               = errors.New("approval timed out")
	ErrCancelled              = errors.New("approval cancelled")
)

type Decision string

const (
	DecisionAllowOnce    Decision = "allow_once"
	DecisionAllowSession Decision = "allow_session"
	DecisionDeny         Decision = "deny"
)

type AuditKind string

const (
	AuditRequested AuditKind = "requested"
	AuditResolved  AuditKind = "resolved"
	AuditTimedOut  AuditKind = "timed_out"
	AuditCancelled AuditKind = "cancelled"
)

type EventKind string

const (
	EventRequested EventKind = "requested"
	EventResolved  EventKind = "resolved"
	EventTimedOut  EventKind = "timed_out"
	EventCancelled EventKind = "cancelled"
)

type AskUserPayload struct {
	Question    string   `json:"question,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

type RequestParams struct {
	SessionID string
	ToolUseID string
	ToolName  string
	Input     json.RawMessage
	Reason    string
	AskUser   *AskUserPayload
	Timeout   time.Duration
}

type Request struct {
	ID        string
	SessionID string
	ToolUseID string
	ToolName  string
	Input     json.RawMessage
	Reason    string
	AskUser   *AskUserPayload
	CreatedAt time.Time
}

type Result struct {
	RequestID string
	Decision  Decision
}

type AuditEntry struct {
	Kind      AuditKind
	RequestID string
	SessionID string
	ToolUseID string
	ToolName  string
	Decision  Decision
	Timestamp time.Time
}

type Event struct {
	Kind     EventKind
	Request  Request
	Decision Decision
}

type Limits struct {
	MaxPending     int
	MaxInputBytes  int
	DefaultTimeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxPending:     64,
		MaxInputBytes:  65536,
		DefaultTimeout: 5 * time.Minute,
	}
}

type Broker struct {
	limits Limits

	mu         sync.Mutex
	pending    map[string]*pendingRequest
	sessionKey map[sessionToolKey]struct{}
	audit      []AuditEntry
	subs       []func(Event)
}

type sessionToolKey struct {
	sessionID string
	toolName  string
}

type pendingRequest struct {
	req    Request
	result chan decisionResult
	done   bool
}

type decisionResult struct {
	decision Decision
	err      error
}

func New(limits Limits) *Broker {
	if limits.MaxPending <= 0 {
		limits.MaxPending = DefaultLimits().MaxPending
	}
	if limits.MaxInputBytes <= 0 {
		limits.MaxInputBytes = DefaultLimits().MaxInputBytes
	}
	if limits.DefaultTimeout <= 0 {
		limits.DefaultTimeout = DefaultLimits().DefaultTimeout
	}
	return &Broker{
		limits:     limits,
		pending:    make(map[string]*pendingRequest),
		sessionKey: make(map[sessionToolKey]struct{}),
	}
}

func (b *Broker) Subscribe(fn func(Event)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, fn)
	idx := len(b.subs) - 1
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if idx < 0 || idx >= len(b.subs) {
			return
		}
		b.subs[idx] = nil
	}
}

func (b *Broker) Request(ctx context.Context, params RequestParams) (Result, error) {
	sessionID := strings.TrimSpace(params.SessionID)
	if sessionID == "" {
		return Result{}, ErrSessionIDRequired
	}
	toolName := strings.TrimSpace(params.ToolName)
	if len(params.Input) > b.limits.MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}

	if !isInteractiveApprovalRequest(toolName, params.AskUser) && b.SessionAllowed(sessionID, toolName) {
		return Result{Decision: DecisionAllowSession}, nil
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = b.limits.DefaultTimeout
	}

	id, err := newRequestID()
	if err != nil {
		return Result{}, err
	}

	req := Request{
		ID:        id,
		SessionID: sessionID,
		ToolUseID: strings.TrimSpace(params.ToolUseID),
		ToolName:  toolName,
		Input:     copyJSON(params.Input),
		Reason:    params.Reason,
		AskUser:   copyAskUser(params.AskUser),
		CreatedAt: time.Now().UTC(),
	}

	entry := b.registerPending(req)
	if entry == nil {
		return Result{}, ErrTooManyPending
	}
	defer b.cleanupPending(id)

	b.appendAudit(AuditEntry{
		Kind:      AuditRequested,
		RequestID: req.ID,
		SessionID: req.SessionID,
		ToolUseID: req.ToolUseID,
		ToolName:  req.ToolName,
		Timestamp: req.CreatedAt,
	})
	b.emit(Event{Kind: EventRequested, Request: req})

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case result := <-entry.result:
		return Result{RequestID: id, Decision: result.decision}, result.err
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			b.finishPending(id, decisionResult{err: ErrCancelled})
			b.appendAudit(AuditEntry{
				Kind:      AuditCancelled,
				RequestID: req.ID,
				SessionID: req.SessionID,
				ToolUseID: req.ToolUseID,
				ToolName:  req.ToolName,
				Timestamp: time.Now().UTC(),
			})
			b.emit(Event{Kind: EventCancelled, Request: req})
			return Result{RequestID: id}, ErrCancelled
		}
		b.finishPending(id, decisionResult{err: ErrTimedOut})
		b.appendAudit(AuditEntry{
			Kind:      AuditTimedOut,
			RequestID: req.ID,
			SessionID: req.SessionID,
			ToolUseID: req.ToolUseID,
			ToolName:  req.ToolName,
			Timestamp: time.Now().UTC(),
		})
		b.emit(Event{Kind: EventTimedOut, Request: req})
		return Result{RequestID: id}, ErrTimedOut
	}
}

func (b *Broker) Resolve(requestID, sessionID string, decision Decision) error {
	if !validDecision(decision) {
		return ErrInvalidDecision
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ErrSessionIDRequired
	}

	b.mu.Lock()
	entry, ok := b.pending[requestID]
	if !ok || entry == nil {
		alreadyResolved := b.auditHasResolvedRequestLocked(requestID)
		b.mu.Unlock()
		if alreadyResolved {
			return ErrRequestAlreadyResolved
		}
		return ErrRequestNotFound
	}
	if entry.done {
		b.mu.Unlock()
		return ErrRequestAlreadyResolved
	}
	if entry.req.SessionID != sessionID {
		b.mu.Unlock()
		return ErrSessionMismatch
	}

	req := entry.req
	if decision == DecisionAllowSession {
		if isInteractiveApprovalRequest(req.ToolName, req.AskUser) {
			b.mu.Unlock()
			return ErrInvalidDecision
		}
		b.sessionKey[sessionToolKey{sessionID: req.SessionID, toolName: req.ToolName}] = struct{}{}
	}
	entry.done = true
	b.mu.Unlock()

	b.appendAudit(AuditEntry{
		Kind:      AuditResolved,
		RequestID: req.ID,
		SessionID: req.SessionID,
		ToolUseID: req.ToolUseID,
		ToolName:  req.ToolName,
		Decision:  decision,
		Timestamp: time.Now().UTC(),
	})
	b.emit(Event{Kind: EventResolved, Request: req, Decision: decision})

	select {
	case entry.result <- decisionResult{decision: decision}:
	default:
	}
	return nil
}

func (b *Broker) Pending() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, entry := range b.pending {
		if entry == nil || entry.done {
			continue
		}
		out = append(out, entry.req)
	}
	return out
}

func (b *Broker) PendingForSession(sessionID string) []Request {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0)
	for _, entry := range b.pending {
		if entry == nil || entry.done || entry.req.SessionID != sessionID {
			continue
		}
		out = append(out, entry.req)
	}
	return out
}

func (b *Broker) GetPending(requestID string) (Request, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pending[requestID]
	if !ok || entry == nil || entry.done {
		return Request{}, false
	}
	return entry.req, true
}

func (b *Broker) Audit() []AuditEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AuditEntry, len(b.audit))
	copy(out, b.audit)
	return out
}

func (b *Broker) SessionAllowed(sessionID, toolName string) bool {
	sessionID = strings.TrimSpace(sessionID)
	toolName = strings.TrimSpace(toolName)
	if sessionID == "" || toolName == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.sessionKey[sessionToolKey{sessionID: sessionID, toolName: toolName}]
	return ok
}

func (b *Broker) registerPending(req Request) *pendingRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) >= b.limits.MaxPending {
		return nil
	}
	entry := &pendingRequest{
		req:    req,
		result: make(chan decisionResult, 1),
	}
	b.pending[req.ID] = entry
	return entry
}

func (b *Broker) finishPending(requestID string, result decisionResult) {
	b.mu.Lock()
	entry, ok := b.pending[requestID]
	if ok && entry != nil && !entry.done {
		entry.done = true
		select {
		case entry.result <- result:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *Broker) cleanupPending(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}

func (b *Broker) appendAudit(entry AuditEntry) {
	b.mu.Lock()
	b.audit = append(b.audit, entry)
	b.mu.Unlock()
}

func (b *Broker) auditHasResolvedRequestLocked(requestID string) bool {
	for i := len(b.audit) - 1; i >= 0; i-- {
		entry := b.audit[i]
		if entry.RequestID != requestID {
			continue
		}
		switch entry.Kind {
		case AuditResolved, AuditTimedOut, AuditCancelled:
			return true
		}
	}
	return false
}

func (b *Broker) emit(ev Event) {
	b.mu.Lock()
	subs := append([]func(Event){}, b.subs...)
	b.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(ev)
		}
	}
}

func isInteractiveApprovalRequest(toolName string, askUser *AskUserPayload) bool {
	if askUser != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(toolName), "AskUserQuestion")
}

func validDecision(decision Decision) bool {
	switch decision {
	case DecisionAllowOnce, DecisionAllowSession, DecisionDeny:
		return true
	default:
		return false
	}
}

func newRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func copyJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	return cp
}

func copyAskUser(payload *AskUserPayload) *AskUserPayload {
	if payload == nil {
		return nil
	}
	cp := *payload
	if len(payload.Suggestions) > 0 {
		cp.Suggestions = append([]string(nil), payload.Suggestions...)
	}
	return &cp
}
