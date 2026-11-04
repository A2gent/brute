package http

// Slack Socket Mode transport: a supervisor keeps one WebSocket per enabled duplex integration.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"nhooyr.io/websocket"
)

const (
	slackReconcileInterval = 5 * time.Second
	slackMinBackoff        = time.Second
	slackMaxBackoff        = 60 * time.Second
	slackSocketReadLimit   = 4 << 20
	slackSocketDialTimeout = 20 * time.Second
	slackSocketAckTimeout  = 5 * time.Second
	slackSocketPingEvery   = 30 * time.Second
	slackSocketPingTimeout = 10 * time.Second
	slackEventDedupeTTL    = time.Hour
	slackDedupeSweepSize   = 1024
)

type slackRuntime struct {
	baseURL string // overridable for tests

	mu      sync.Mutex
	seen    map[string]time.Time
	threads map[string]*slackThreadLock
	names   map[string]slackCachedName

	runs sync.WaitGroup
}

type slackThreadLock struct {
	mu   sync.Mutex
	refs int
}

// markEvent reports whether the event is new. Slack redelivers around reconnects.
func (r *slackRuntime) markEvent(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if seenAt, ok := r.seen[key]; ok && now.Sub(seenAt) < slackEventDedupeTTL {
		return false
	}
	if r.seen == nil {
		r.seen = make(map[string]time.Time)
	}
	if len(r.seen) >= slackDedupeSweepSize {
		for candidate, seenAt := range r.seen {
			if now.Sub(seenAt) >= slackEventDedupeTTL {
				delete(r.seen, candidate)
			}
		}
	}
	r.seen[key] = now
	return true
}

func (r *slackRuntime) lockThread(key string) func() {
	r.mu.Lock()
	if r.threads == nil {
		r.threads = make(map[string]*slackThreadLock)
	}
	lock := r.threads[key]
	if lock == nil {
		lock = &slackThreadLock{}
		r.threads[key] = lock
	}
	lock.refs++
	r.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		r.mu.Lock()
		defer r.mu.Unlock()
		if lock.refs--; lock.refs == 0 {
			delete(r.threads, key)
		}
	}
}

type slackConnectionSpec struct {
	integrationID string
	botToken      string
	appToken      string
	fingerprint   string
}

type slackActiveConnection struct {
	fingerprint string
	cancel      context.CancelFunc
}

func slackTokenFingerprint(botToken, appToken string) string {
	sum := sha256.Sum256([]byte(botToken + "\x00" + appToken))
	return hex.EncodeToString(sum[:])
}

func (s *Server) runSlackSocketSupervisor(ctx context.Context) {
	active := make(map[string]*slackActiveConnection)
	ticker := time.NewTicker(slackReconcileInterval)
	defer ticker.Stop()

	for {
		s.reconcileSlackConnections(ctx, active)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reconcileSlackConnections(ctx context.Context, active map[string]*slackActiveConnection) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		logging.Warn("Slack reconcile skipped: failed to list integrations: %v", err)
		return
	}

	wanted := make(map[string]slackConnectionSpec)
	for _, integration := range integrations {
		if integration == nil || !integration.Enabled || integration.Provider != "slack" || integration.Mode != "duplex" {
			continue
		}
		botToken := strings.TrimSpace(integration.Config["bot_token"])
		appToken := strings.TrimSpace(integration.Config["app_token"])
		if botToken == "" || appToken == "" {
			continue
		}
		wanted[integration.ID] = slackConnectionSpec{
			integrationID: integration.ID,
			botToken:      botToken,
			appToken:      appToken,
			fingerprint:   slackTokenFingerprint(botToken, appToken),
		}
	}

	for id, connection := range active {
		if spec, ok := wanted[id]; !ok || spec.fingerprint != connection.fingerprint {
			connection.cancel()
			delete(active, id)
		}
	}
	for id, spec := range wanted {
		if _, ok := active[id]; ok {
			continue
		}
		connCtx, cancel := context.WithCancel(ctx)
		active[id] = &slackActiveConnection{fingerprint: spec.fingerprint, cancel: cancel}
		go s.runSlackConnection(connCtx, ctx, spec)
	}
}

// runSlackConnection reconnects until connCtx ends. runCtx is the server context: agent runs
// outlive any single connection, so a reconnect must not cancel them.
func (s *Server) runSlackConnection(connCtx, runCtx context.Context, spec slackConnectionSpec) {
	backoff := slackMinBackoff
	for connCtx.Err() == nil {
		connected, err := s.serveSlackSocket(connCtx, runCtx, spec)
		if connCtx.Err() != nil {
			return
		}
		if connected {
			backoff = slackMinBackoff
		}
		if err != nil {
			logging.Warn("Slack socket for integration %s dropped, retrying in %s: %v", spec.integrationID, backoff, err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-connCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, slackMaxBackoff)
	}
}

type slackSocketFrame struct {
	Type       string          `json:"type"`
	Reason     string          `json:"reason"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
}

// connected reports that Slack said hello, which is what resets the backoff.
func (s *Server) serveSlackSocket(connCtx, runCtx context.Context, spec slackConnectionSpec) (connected bool, err error) {
	api := s.slackAPI()
	identity, err := api.authTest(connCtx, spec.botToken)
	if err != nil {
		return false, err
	}
	socketURL, err := api.openSocketURL(connCtx, spec.appToken)
	if err != nil {
		return false, err
	}

	dialCtx, cancelDial := context.WithTimeout(connCtx, slackSocketDialTimeout)
	conn, _, err := websocket.Dial(dialCtx, socketURL, nil)
	cancelDial()
	if err != nil {
		return false, fmt.Errorf("socket dial failed: %w", slackSafeError(err))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(slackSocketReadLimit)

	readCtx, cancelRead := context.WithCancel(connCtx)
	defer cancelRead()
	go slackKeepAlive(readCtx, cancelRead, conn)

	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			return connected, fmt.Errorf("socket read failed: %w", slackSafeError(err))
		}
		var frame slackSocketFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			logging.Warn("Slack socket for integration %s sent an unreadable frame: %v", spec.integrationID, err)
			continue
		}
		if frame.EnvelopeID != "" {
			if err := slackAckEnvelope(readCtx, conn, frame.EnvelopeID); err != nil {
				return connected, fmt.Errorf("socket ack failed: %w", slackSafeError(err))
			}
			s.dispatchSlackEnvelope(runCtx, spec.integrationID, identity, frame)
			continue
		}
		switch frame.Type {
		case "hello":
			connected = true
			logging.Info("Slack socket connected: integration=%s team=%s", spec.integrationID, identity.TeamID)
		case "disconnect":
			logging.Info("Slack requested reconnect: integration=%s reason=%s", spec.integrationID, frame.Reason)
			return connected, nil
		}
	}
}

// slackKeepAlive detects half-open connections, which would otherwise block Read forever.
func slackKeepAlive(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	ticker := time.NewTicker(slackSocketPingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, slackSocketPingTimeout)
			err := conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

func slackAckEnvelope(ctx context.Context, conn *websocket.Conn, envelopeID string) error {
	ack, err := json.Marshal(map[string]string{"envelope_id": envelopeID})
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, slackSocketAckTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, ack)
}

// dispatchSlackEnvelope must stay cheap: it runs in the read loop, and all slow work belongs in
// the goroutine under the server context.
func (s *Server) dispatchSlackEnvelope(runCtx context.Context, integrationID string, identity slackIdentity, frame slackSocketFrame) {
	if frame.Type != "events_api" {
		return
	}
	var payload struct {
		Event slackEvent `json:"event"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		logging.Warn("Slack integration %s sent an unreadable event: %v", integrationID, err)
		return
	}
	event := payload.Event
	if slackEventIgnored(event, identity) {
		return
	}
	// Keyed by message, not event_id: the same message can arrive again under a new event_id.
	if !s.slack.markEvent(integrationID + ":" + event.Channel + ":" + event.TS) {
		return
	}
	s.slack.runs.Add(1)
	go func() {
		defer s.slack.runs.Done()
		s.handleSlackMention(runCtx, integrationID, identity, event)
	}()
}

// waitForSlackRuns gives runs canceled by shutdown time to post their last message and clean up.
func (s *Server) waitForSlackRuns(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.slack.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logging.Warn("Slack runs still active after %s, exiting anyway", timeout)
	}
}
