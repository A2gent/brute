// Package a2atunnel implements the gRPC tunnel client that connects brute to
// the Square A2A broker. It mirrors the hand-rolled gRPC service descriptor
// used by square/internal/tunnel/server.go — no protoc-generated code needed.
//
// # Protocol
//
// One persistent stream, two logical flows multiplexed by the Kind field:
//
//	Square → Agent  AgentRequest  kind="task"          inbound task from Square
//	                              kind="call_response"  result of our outgoing call
//
//	Agent  → Square AgentResponse kind="task_response" our result for an inbound task
//	                              kind="call_request"  we want to call another agent
//
// # Concurrency model
//
// One goroutine runs the recv loop. Multiple goroutines can write concurrently
// (protected by sendMu). This means:
//   - While processing an inbound task, the handler goroutine can call Call()
//     which writes a call_request on the same stream.
//   - The recv loop picks up the call_response and delivers it to the waiting
//     Call() goroutine via a channel in pendingCalls.
//   - The target agent can do the same thing — reuse its own tunnel to make
//     further calls while processing a request. The protocol is fully recursive.
package a2atunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ---- Kind constants (mirror square/internal/tunnel/server.go) ----

const (
	KindTask         = "task"
	KindCallResponse = "call_response"
	KindTaskResponse = "task_response"
	KindCallRequest  = "call_request"
)

// ---- Wire message types (must exactly match square/internal/tunnel) ----

// AgentRequest is received from Square.
type AgentRequest struct {
	Kind      string `json:"kind"`
	RequestID string `json:"request_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Payload   []byte `json:"payload"`
	Error     string `json:"error,omitempty"`
}

func (m *AgentRequest) ProtoMessage()            {}
func (m *AgentRequest) Reset()                   { *m = AgentRequest{} }
func (m *AgentRequest) String() string           { return fmt.Sprintf("%+v", *m) }
func (m *AgentRequest) Marshal() ([]byte, error) { return json.Marshal(m) }
func (m *AgentRequest) Unmarshal(b []byte) error { return json.Unmarshal(b, m) }
func (m *AgentRequest) ProtoSize() int           { b, _ := json.Marshal(m); return len(b) }

// AgentResponse is sent to Square.
type AgentResponse struct {
	Kind          string `json:"kind"`
	RequestID     string `json:"request_id,omitempty"`
	CallID        string `json:"call_id,omitempty"`
	TargetAgentID string `json:"target_agent_id,omitempty"`
	Payload       []byte `json:"payload"`
	Error         string `json:"error,omitempty"`
}

func (m *AgentResponse) ProtoMessage()            {}
func (m *AgentResponse) Reset()                   { *m = AgentResponse{} }
func (m *AgentResponse) String() string           { return fmt.Sprintf("%+v", *m) }
func (m *AgentResponse) Marshal() ([]byte, error) { return json.Marshal(m) }
func (m *AgentResponse) Unmarshal(b []byte) error { return json.Unmarshal(b, m) }
func (m *AgentResponse) ProtoSize() int           { b, _ := json.Marshal(m); return len(b) }

// ---- gRPC client stream ----

type connectClientStream struct{ grpc.ClientStream }

func (s *connectClientStream) Send(m *AgentResponse) error { return s.ClientStream.SendMsg(m) }
func (s *connectClientStream) Recv() (*AgentRequest, error) {
	m := new(AgentRequest)
	if err := s.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

const tunnelMethodPath = "/square.tunnel.v1.TunnelService/Connect"

type websocketClientStream struct {
	conn *websocket.Conn
}

func (s *websocketClientStream) Send(m *AgentResponse) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, s.conn, m)
}

func (s *websocketClientStream) Recv() (*AgentRequest, error) {
	var m AgentRequest
	if err := wsjson.Read(context.Background(), s.conn, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ---- Event log ----

// LogEntry is a single timestamped log line surfaced in the UI.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type eventLog struct {
	mu      sync.RWMutex
	entries []LogEntry
	cap     int
	subs    []chan struct{}
}

func newEventLog(capacity int) *eventLog {
	return &eventLog{cap: capacity}
}

func (l *eventLog) append(msg string) {
	entry := LogEntry{Time: time.Now(), Message: msg}
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.cap {
		l.entries = l.entries[len(l.entries)-l.cap:]
	}
	subs := make([]chan struct{}, len(l.subs))
	copy(subs, l.subs)
	l.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (l *eventLog) snapshot() []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]LogEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

func (l *eventLog) subscribe() chan struct{} {
	ch := make(chan struct{}, 8)
	l.mu.Lock()
	l.subs = append(l.subs, ch)
	l.mu.Unlock()
	return ch
}

func (l *eventLog) unsubscribe(ch chan struct{}) {
	l.mu.Lock()
	for i, s := range l.subs {
		if s == ch {
			l.subs = append(l.subs[:i], l.subs[i+1:]...)
			break
		}
	}
	l.mu.Unlock()
	close(ch)
}

// ---- State ----

// State is the current tunnel connection state.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
)

// Status is returned to the UI.
type Status struct {
	State       State      `json:"state"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	SquareAddr  string     `json:"square_addr"`
	Log         []LogEntry `json:"log"`
}

// ---- Payload types ----

// InboundPayload is the JSON inside an inbound task's AgentRequest.Payload.
// The calling agent (another brute instance) populates this.
type InboundPayload struct {
	Task            string `json:"task"`
	SourceAgentID   string `json:"source_agent_id"`
	SourceAgentName string `json:"source_agent_name"`
	ConversationID  string `json:"conversation_id,omitempty"`
	SourceSessionID string `json:"source_session_id,omitempty"`
}

// OutboundPayload is what we put in AgentResponse.Payload for a task_response.
type OutboundPayload struct {
	Result string `json:"result"`
}

// CallPayload is what we send as the Payload of a call_request.
// It is also what a target agent expects as InboundPayload.
type CallPayload = InboundPayload

// ---- Handler interface ----

// Handler processes an inbound task request. It must be safe to call
// concurrently — multiple tasks may be in-flight simultaneously.
type Handler interface {
	Handle(ctx context.Context, req *AgentRequest) ([]byte, error)
}

// ---- Pending tracking ----

type pendingCall struct {
	ch chan *AgentRequest // receives the call_response from Square
}

// ---- activeStream ----

// activeStream represents a live gRPC stream and all state scoped to it.
// It is replaced atomically on each reconnect.
type activeStream struct {
	stream interface {
		Send(*AgentResponse) error
		Recv() (*AgentRequest, error)
	}
	pendingCalls map[string]*pendingCall // call_id → waiter
	mu           sync.Mutex
	sendMu       sync.Mutex // serialises stream.Send
}

func newActiveStream(s interface {
	Send(*AgentResponse) error
	Recv() (*AgentRequest, error)
}) *activeStream {
	return &activeStream{
		stream:       s,
		pendingCalls: make(map[string]*pendingCall),
	}
}

// send serialises writes on the stream so concurrent goroutines don't
// interleave gRPC frames.
func (a *activeStream) send(msg *AgentResponse) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	return a.stream.Send(msg)
}

// registerCall adds a pending call_id waiter and returns the channel.
func (a *activeStream) registerCall(callID string) chan *AgentRequest {
	ch := make(chan *AgentRequest, 1)
	a.mu.Lock()
	a.pendingCalls[callID] = &pendingCall{ch: ch}
	a.mu.Unlock()
	return ch
}

// deregisterCall removes a pending call_id waiter.
func (a *activeStream) deregisterCall(callID string) {
	a.mu.Lock()
	delete(a.pendingCalls, callID)
	a.mu.Unlock()
}

// deliverCallResponse routes an incoming call_response to the waiting Call().
func (a *activeStream) deliverCallResponse(msg *AgentRequest) {
	a.mu.Lock()
	pc, ok := a.pendingCalls[msg.CallID]
	a.mu.Unlock()
	if ok {
		pc.ch <- msg
	}
}

// ---- TunnelClient ----

// TunnelClient manages the gRPC tunnel to Square. It:
//   - Reconnects automatically with exponential backoff.
//   - Dispatches inbound tasks (kind="task") to a Handler.
//   - Exposes Call() so that handlers — or the agent's tool system — can
//     initiate outgoing calls to other agents through the same stream.
type TunnelClient struct {
	squareAddr string
	apiKey     string
	handler    Handler
	transport  Transport
	log        *eventLog

	// current is the live stream; replaced on each successful connect.
	// nil when disconnected.
	currentMu sync.RWMutex
	current   *activeStream

	stateMu     sync.RWMutex
	state       State
	connectedAt *time.Time
}

type Transport string

const (
	TransportGRPC      Transport = "grpc"
	TransportWebSocket Transport = "websocket"
)

// New creates a TunnelClient. Call Run to start the connection loop.
func New(squareAddr, apiKey string, handler Handler) *TunnelClient {
	return NewWithTransport(squareAddr, apiKey, handler, TransportGRPC)
}

// NewWithTransport creates a TunnelClient using the selected tunnel transport.
func NewWithTransport(squareAddr, apiKey string, handler Handler, transport Transport) *TunnelClient {
	if transport != TransportWebSocket {
		transport = TransportGRPC
	}
	return &TunnelClient{
		squareAddr: squareAddr,
		apiKey:     apiKey,
		handler:    handler,
		transport:  transport,
		log:        newEventLog(200),
		state:      StateDisconnected,
	}
}

// Status returns the current connection status and log snapshot.
func (c *TunnelClient) Status() Status {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return Status{
		State:       c.state,
		ConnectedAt: c.connectedAt,
		SquareAddr:  c.squareAddr,
		Log:         c.log.snapshot(),
	}
}

// SubscribeLog returns a channel that is signalled on each new log entry.
func (c *TunnelClient) SubscribeLog() chan struct{} { return c.log.subscribe() }

// UnsubscribeLog removes a subscription.
func (c *TunnelClient) UnsubscribeLog(ch chan struct{}) { c.log.unsubscribe(ch) }

func (c *TunnelClient) setState(s State) {
	c.stateMu.Lock()
	c.state = s
	if s == StateConnected {
		now := time.Now()
		c.connectedAt = &now
	} else if s == StateDisconnected {
		c.connectedAt = nil
	}
	c.stateMu.Unlock()
}

func (c *TunnelClient) logf(format string, args ...interface{}) {
	c.log.append(fmt.Sprintf(format, args...))
}

// setActive atomically replaces the active stream reference.
func (c *TunnelClient) setActive(as *activeStream) {
	c.currentMu.Lock()
	c.current = as
	c.currentMu.Unlock()
}

// getActive returns the active stream, or nil if not connected.
func (c *TunnelClient) getActive() *activeStream {
	c.currentMu.RLock()
	defer c.currentMu.RUnlock()
	return c.current
}

// Run blocks, managing the tunnel with exponential-backoff reconnect.
// Returns only when ctx is cancelled.
func (c *TunnelClient) Run(ctx context.Context) {
	c.logf("Starting A2A tunnel to %s", c.squareAddr)

	for attempt := 0; ; attempt++ {
		select {
		case <-ctx.Done():
			c.setActive(nil)
			c.setState(StateDisconnected)
			c.logf("Tunnel stopped")
			return
		default:
		}

		if attempt > 0 {
			shift := attempt - 1
			if shift > 5 {
				shift = 5
			}
			delay := time.Duration(2<<uint(shift)) * time.Second
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			c.logf("Reconnecting in %s (attempt %d)…", delay, attempt+1)
			select {
			case <-ctx.Done():
				c.setActive(nil)
				c.setState(StateDisconnected)
				c.logf("Tunnel stopped")
				return
			case <-time.After(delay):
			}
		}

		c.setState(StateConnecting)
		c.logf("Connecting to Square at %s…", c.squareAddr)

		if err := c.runStream(ctx); err != nil {
			if ctx.Err() != nil {
				c.setActive(nil)
				c.setState(StateDisconnected)
				c.logf("Tunnel stopped")
				return
			}
			c.setState(StateDisconnected)
			c.logf("Tunnel error: %v", err)
		} else {
			c.setActive(nil)
			c.setState(StateDisconnected)
			return
		}
	}
}

// runStream opens one gRPC connection + stream and runs the recv loop until
// the stream closes or ctx is cancelled.
func (c *TunnelClient) runStream(ctx context.Context) error {
	if c.transport == TransportWebSocket {
		return c.runWebSocketStream(ctx)
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	conn, err := grpc.DialContext( //nolint:staticcheck
		dialCtx,
		c.squareAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	dialCancel()
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	md := metadata.Pairs("x-api-key", c.apiKey)
	streamCtx := metadata.NewOutgoingContext(ctx, md)

	rawStream, err := conn.NewStream(
		streamCtx,
		&grpc.StreamDesc{
			StreamName:    "Connect",
			ServerStreams: true,
			ClientStreams: true,
		},
		tunnelMethodPath,
	)
	if err != nil {
		return fmt.Errorf("stream open failed: %w", err)
	}

	as := newActiveStream(&connectClientStream{rawStream})
	c.setActive(as)
	c.setState(StateConnected)
	c.logf("gRPC stream established — agent is live on the A2A network")
	return c.runRecvLoop(ctx, as)
}

func (c *TunnelClient) runWebSocketStream(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Authorization": {"Bearer " + c.apiKey},
		},
	}
	conn, _, err := websocket.Dial(dialCtx, c.squareAddr, opts)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	as := newActiveStream(&websocketClientStream{conn: conn})
	c.setActive(as)
	c.setState(StateConnected)
	c.logf("WebSocket stream established — agent is live on the A2A network")
	return c.runRecvLoop(ctx, as)
}

func (c *TunnelClient) runRecvLoop(ctx context.Context, as *activeStream) error {
	for {
		msg, err := as.stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream recv: %w", err)
		}

		switch msg.Kind {
		case KindCallResponse:
			as.deliverCallResponse(msg)
		case KindTask, "":
			go c.dispatchTask(ctx, as, msg)
		default:
			c.logf("Unknown message kind %q (request_id=%s) — ignored", msg.Kind, msg.RequestID)
		}
	}
}

// dispatchTask runs the handler for an inbound task and sends the response.
func (c *TunnelClient) dispatchTask(ctx context.Context, as *activeStream, req *AgentRequest) {
	var p InboundPayload
	if err := json.Unmarshal(req.Payload, &p); err == nil && p.SourceAgentName != "" {
		c.logf("Inbound task from %s (request %s)", p.SourceAgentName, req.RequestID)
	} else {
		c.logf("Inbound task (request %s)", req.RequestID)
	}

	payload, err := c.handler.Handle(ctx, req)

	resp := &AgentResponse{Kind: KindTaskResponse, RequestID: req.RequestID}
	if err != nil {
		resp.Error = err.Error()
		c.logf("Task failed (request %s): %v", req.RequestID, err)
	} else {
		resp.Payload = payload
		c.logf("Task completed (request %s)", req.RequestID)
	}

	if sendErr := as.send(resp); sendErr != nil {
		c.logf("Failed to send task response (request %s): %v", req.RequestID, sendErr)
	}
}

// Call sends a call_request to Square, asking it to dispatch payload to
// targetAgentID, and blocks until Square returns the call_response.
//
// This can be called from inside a Handler.Handle goroutine — the same
// stream is used for both the inbound task recv loop and outgoing calls,
// and the pendingCalls map safely routes the response back to this call.
// The target agent can in turn call Call() on its own tunnel while handling
// the request, enabling arbitrary recursive agent-to-agent chaining.
func (c *TunnelClient) Call(ctx context.Context, targetAgentID string, payload []byte) ([]byte, error) {
	as := c.getActive()
	if as == nil {
		return nil, fmt.Errorf("tunnel not connected")
	}

	callID := uuid.New().String()
	ch := as.registerCall(callID)
	defer as.deregisterCall(callID)

	if err := as.send(&AgentResponse{
		Kind:          KindCallRequest,
		CallID:        callID,
		TargetAgentID: targetAgentID,
		Payload:       payload,
	}); err != nil {
		return nil, fmt.Errorf("failed to send call_request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("remote agent error: %s", resp.Error)
		}
		return resp.Payload, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("call timed out: %w", ctx.Err())
	}
}

// IsConnected reports whether the tunnel is currently live.
func (c *TunnelClient) IsConnected() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state == StateConnected
}
