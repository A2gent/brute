package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/cdp"
)

const (
	// WHY: NEVER call Emulation.setDeviceMetricsOverride or Browser.setWindowBounds.
	// These are encoding ceilings only: Caesar resizing must never modify the agent viewport.
	previewMaxWidth       = 1280
	previewMaxHeight      = 800
	previewCommandTimeout = 3 * time.Second
	previewWriteTimeout   = 5 * time.Second
	previewKeepalive      = 15 * time.Second
)

type previewTarget struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Type  string `json:"-"`
}
type previewFrame struct {
	TS     int64  `json:"ts"`
	Format string `json:"format"`
	Image  string `json:"image"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type previewSocket interface {
	Send([]byte) error
	Read() ([]byte, error)
	Close() error
}
type previewCDPMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type previewTargetGate struct {
	busy chan struct{}
	refs int // holders and waiters, protected by browserChromePreview.mu
}
type browserChromePreview struct {
	mu      sync.Mutex
	streams map[string]*previewStream
	gates   map[string]*previewTargetGate
	origin  string
	client  *http.Client
	dial    func(context.Context, string) (previewSocket, error)
}
type previewPending struct {
	reply chan error
	timer *time.Timer
}
type previewStream struct {
	owner     *browserChromePreview
	target    string
	socket    previewSocket
	subs      map[chan previewFrame]struct{} // protected by owner.mu
	latest    *previewFrame                  // protected by owner.mu
	done      chan struct{}
	once      sync.Once
	sendMu    sync.Mutex
	pendingMu sync.Mutex
	nextID    int
	pending   map[int]*previewPending
}

func newBrowserChromePreview() *browserChromePreview {
	port, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHROME_DEBUG_PORT")))
	if err != nil || port < 1 || port > 65535 {
		port = 9223
	}
	return &browserChromePreview{
		streams: make(map[string]*previewStream),
		gates:   make(map[string]*previewTargetGate),
		origin:  fmt.Sprintf("http://127.0.0.1:%d", port),
		// Never send local debugger discovery through environment-configured proxies or redirects.
		client: &http.Client{Timeout: previewCommandTimeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		dial:   dialPreviewSocket,
	}
}

// Rod's WebSocket handshake does not observe context cancellation once TCP connects.
// Retain its connection so deadlines bound the handshake and every subsequent write.
type previewDialer struct{ conn net.Conn }

func (d *previewDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: previewCommandTimeout}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	d.conn = conn
	deadline := time.Now().Add(previewCommandTimeout)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err = conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

type previewWebSocket struct {
	*cdp.WebSocket
	conn net.Conn
}

func (w *previewWebSocket) Send(b []byte) error {
	if err := w.conn.SetWriteDeadline(time.Now().Add(previewCommandTimeout)); err != nil {
		return err
	}
	return w.WebSocket.Send(b)
}
func dialPreviewSocket(ctx context.Context, u string) (previewSocket, error) {
	ctx, cancel := context.WithTimeout(ctx, previewCommandTimeout)
	defer cancel()
	d := &previewDialer{}
	ws := &cdp.WebSocket{Dialer: d}
	err := ws.Connect(ctx, u, nil)
	if err != nil {
		if d.conn != nil {
			d.conn.Close()
		}
		return nil, err
	}
	if err = d.conn.SetDeadline(time.Time{}); err != nil {
		ws.Close()
		return nil, err
	}
	return &previewWebSocket{WebSocket: ws, conn: d.conn}, nil
}

func (p *browserChromePreview) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.origin+path, nil)
	if err != nil {
		return err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Chrome discovery returned %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(out)
}
func (p *browserChromePreview) targets(ctx context.Context) ([]previewTarget, error) {
	var raw []struct{ ID, URL, Title, Type string }
	if err := p.getJSON(ctx, "/json/list", &raw); err != nil {
		return nil, err
	}
	targets := make([]previewTarget, 0, len(raw))
	for _, target := range raw {
		if target.Type == "page" && validPreviewTargetID(target.ID) {
			targets = append(targets, previewTarget{ID: target.ID, URL: target.URL, Title: target.Title, Type: target.Type})
		}
	}
	return targets, nil
}
func validPreviewTargetID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return id != "." && id != ".."
}

var errPreviewTargetNotFound = errors.New("Chrome page target not found")

// Serialize first/last subscriber transitions only for the same target. Count
// waiters as well as holders so cancellation cannot replace a gate still in use.
func (p *browserChromePreview) lockTarget(ctx context.Context, id string) (func(), error) {
	p.mu.Lock()
	gate := p.gates[id]
	if gate == nil {
		gate = &previewTargetGate{busy: make(chan struct{}, 1)}
		p.gates[id] = gate
	}
	gate.refs++
	p.mu.Unlock()
	drop := func(held bool) {
		p.mu.Lock()
		if held {
			<-gate.busy
		}
		gate.refs--
		if gate.refs == 0 {
			delete(p.gates, id)
		}
		p.mu.Unlock()
	}
	select {
	case gate.busy <- struct{}{}:
		if err := ctx.Err(); err != nil {
			drop(true)
			return nil, err
		}
		return func() { drop(true) }, nil
	case <-ctx.Done():
		drop(false)
		return nil, ctx.Err()
	}
}

func (p *browserChromePreview) subscribe(ctx context.Context, id string) (<-chan previewFrame, func(), error) {
	if !validPreviewTargetID(id) {
		return nil, nil, errPreviewTargetNotFound
	}
	targets, err := p.targets(ctx)
	if err != nil {
		return nil, nil, err
	}
	found := false
	for _, target := range targets {
		if target.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, errPreviewTargetNotFound
	}
	unlock, err := p.lockTarget(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	p.mu.Lock()
	stream := p.streams[id]
	p.mu.Unlock()
	if stream == nil {
		// Ignore webSocketDebuggerUrl from discovery and any URL supplied by callers.
		// The only connection allowed is this trusted local page endpoint.
		socketURL := "ws" + strings.TrimPrefix(p.origin, "http") + "/devtools/page/" + url.PathEscape(id)
		socket, err := p.dial(ctx, socketURL)
		if err != nil {
			return nil, nil, err
		}
		stream = &previewStream{owner: p, target: id, socket: socket, subs: make(map[chan previewFrame]struct{}), done: make(chan struct{}), pending: make(map[int]*previewPending)}
		go stream.readLoop()
		params := map[string]any{"format": "jpeg", "quality": 50, "maxWidth": previewMaxWidth, "maxHeight": previewMaxHeight, "everyNthFrame": 2}
		if err = stream.call(ctx, "Page.startScreencast", params); err != nil {
			stream.close()
			return nil, nil, err
		}
	}
	frames := make(chan previewFrame, 1)
	p.mu.Lock()
	select {
	case <-stream.done:
		p.mu.Unlock()
		return nil, nil, errors.New("Chrome preview connection closed")
	default:
	}
	p.streams[id] = stream
	stream.subs[frames] = struct{}{}
	if stream.latest != nil {
		frames <- *stream.latest
	}
	p.mu.Unlock()
	var once sync.Once
	return frames, func() {
		once.Do(func() {
			unlock, _ := p.lockTarget(context.Background(), id)
			defer unlock()
			p.mu.Lock()
			_, exists := stream.subs[frames]
			if exists {
				delete(stream.subs, frames)
				close(frames)
			}
			last := exists && len(stream.subs) == 0
			p.mu.Unlock()
			if last {
				// Stop has a bounded command timeout, then close the independent transport.
				_ = stream.call(context.Background(), "Page.stopScreencast", nil)
				stream.close()
			}
		})
	}, nil
}

func (st *previewStream) close() {
	st.once.Do(func() {
		// done and subscriber channels are closed under the same lock used by delivery.
		st.owner.mu.Lock()
		close(st.done)
		if st.owner.streams[st.target] == st {
			delete(st.owner.streams, st.target)
		}
		for ch := range st.subs {
			close(ch)
			delete(st.subs, ch)
		}
		st.owner.mu.Unlock()
		_ = st.socket.Close()
		st.pendingMu.Lock()
		for id, pending := range st.pending {
			if pending.timer != nil {
				pending.timer.Stop()
			}
			pending.reply <- io.EOF
			delete(st.pending, id)
		}
		st.pendingMu.Unlock()
	})
}
func (st *previewStream) send(method string, params any, async bool) (int, <-chan error, error) {
	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	select {
	case <-st.done:
		return 0, nil, io.EOF
	default:
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return 0, nil, err
	}
	st.pendingMu.Lock()
	// Cap outstanding CDP replies so a stalled debugger cannot grow memory.
	if len(st.pending) >= 128 {
		st.pendingMu.Unlock()
		return 0, nil, errors.New("Chrome preview reply backlog")
	}
	st.nextID++
	id := st.nextID
	pending := &previewPending{reply: make(chan error, 1)}
	st.pending[id] = pending
	if async {
		pending.timer = time.AfterFunc(previewCommandTimeout, func() { st.close() })
	}
	st.pendingMu.Unlock()
	message, _ := json.Marshal(previewCDPMessage{ID: id, Method: method, Params: raw})
	if err = st.socket.Send(message); err != nil {
		return id, pending.reply, err
	}
	return id, pending.reply, nil
}
func (st *previewStream) call(ctx context.Context, method string, params any) error {
	ctx, cancel := context.WithTimeout(ctx, previewCommandTimeout)
	defer cancel()
	id, reply, err := st.send(method, params, false)
	if err != nil {
		return err
	}
	defer func() { st.pendingMu.Lock(); delete(st.pending, id); st.pendingMu.Unlock() }()
	select {
	case err = <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-st.done:
		return io.EOF
	}
}
func (st *previewStream) readLoop() {
	defer st.close()
	for {
		b, err := st.socket.Read()
		if err != nil {
			return
		}
		var message previewCDPMessage
		if json.Unmarshal(b, &message) != nil {
			return
		}
		if message.ID != 0 {
			st.pendingMu.Lock()
			pending := st.pending[message.ID]
			if pending != nil {
				delete(st.pending, message.ID)
				if pending.timer != nil {
					pending.timer.Stop()
				}
				var replyErr error
				if len(message.Error) > 0 && string(message.Error) != "null" {
					replyErr = fmt.Errorf("Chrome preview CDP error: %s", message.Error)
				}
				pending.reply <- replyErr
			}
			st.pendingMu.Unlock()
			if pending != nil && pending.timer != nil && len(message.Error) > 0 && string(message.Error) != "null" {
				return
			}
			continue
		}
		if message.Method != "Page.screencastFrame" {
			continue
		}
		var event struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(message.Params, &event) != nil {
			return
		}
		// Ack before decoding/delivery, including malformed images and frames dropped
		// by slow subscribers. Never wait for SSE writers on the CDP receive loop.
		if _, _, err = st.send("Page.screencastFrameAck", map[string]int{"sessionId": event.SessionID}, true); err != nil {
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			continue
		}
		dimensions, err := jpeg.DecodeConfig(bytes.NewReader(decoded))
		if err != nil {
			continue
		}
		frame := previewFrame{TS: time.Now().UnixMilli(), Format: "jpeg", Image: event.Data, Width: dimensions.Width, Height: dimensions.Height}
		st.owner.mu.Lock()
		st.latest = &frame
		for ch := range st.subs {
			select {
			case ch <- frame:
			default:
				// One-frame latest-value buffers bound slow-client memory.
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- frame:
				default:
				}
			}
		}
		st.owner.mu.Unlock()
	}
}

func (s *Server) chromePreview() *browserChromePreview {
	s.browserChromePreviewMu.Lock()
	defer s.browserChromePreviewMu.Unlock()
	if s.browserChromePreview == nil {
		s.browserChromePreview = newBrowserChromePreview()
	}
	return s.browserChromePreview
}
func (s *Server) handleBrowserChromePreviewStatus(w http.ResponseWriter, r *http.Request) {
	p := s.chromePreview()
	targets, err := p.targets(r.Context())
	if targets == nil {
		targets = []previewTarget{}
	}
	var version struct {
		Browser   string
		UserAgent string `json:"User-Agent"`
	}
	headless := false
	if err == nil && p.getJSON(r.Context(), "/json/version", &version) == nil {
		headless = strings.Contains(version.Browser, "HeadlessChrome") || strings.Contains(version.UserAgent, "HeadlessChrome")
	}
	// headless is reported evidence from Chrome's product or User-Agent, not the launch
	// setting. false also means unreported/unknown (some Chrome versions omit it).
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Available bool            `json:"available"`
		Headless  bool            `json:"headless"`
		Targets   []previewTarget `json:"targets"`
	}{err == nil, headless, targets})
}
func (s *Server) handleBrowserChromePreviewFrames(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.sessionRunParentContext(), cancel)
	defer stop()
	defer cancel()
	r = r.WithContext(ctx)
	target := r.URL.Query().Get("target")
	if !validPreviewTargetID(target) {
		s.errorResponse(w, http.StatusBadRequest, "A Chrome page target ID is required")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}
	frames, unsubscribe, err := s.chromePreview().subscribe(r.Context(), target)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, errPreviewTargetNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	write := func(payload string) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(previewWriteTimeout)); err != nil {
			return false
		}
		if _, err := io.WriteString(w, payload); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write(":ready\n\n") {
		return
	}
	ticker := time.NewTicker(previewKeepalive)
	defer ticker.Stop()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return
			}
			payload, _ := json.Marshal(frame)
			if !write("data: " + string(payload) + "\n\n") {
				return
			}
		case <-ticker.C:
			if !write(":heartbeat\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
