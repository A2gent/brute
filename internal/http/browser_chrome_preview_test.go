package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake page socket tests CDP behavior without launching or controlling Chrome.
type previewFakeSocket struct {
	messages   chan []byte
	commands   chan previewCDPMessage
	closed     chan struct{}
	once       sync.Once
	failMethod string
	omitReply  string
}

func newPreviewFakeSocket() *previewFakeSocket {
	return &previewFakeSocket{messages: make(chan []byte, 128), commands: make(chan previewCDPMessage, 128), closed: make(chan struct{})}
}
func (f *previewFakeSocket) Send(b []byte) error {
	var cmd previewCDPMessage
	if err := json.Unmarshal(b, &cmd); err != nil {
		return err
	}
	select {
	case <-f.closed:
		return io.EOF
	default:
	}
	f.commands <- cmd
	if cmd.Method == f.omitReply {
		return nil
	}
	reply := previewCDPMessage{ID: cmd.ID}
	if cmd.Method == f.failMethod {
		reply.Error = json.RawMessage(`{"code":-32000,"message":"fake failure"}`)
	}
	b, _ = json.Marshal(reply)
	select {
	case f.messages <- b:
		return nil
	case <-f.closed:
		return io.EOF
	}
}
func (f *previewFakeSocket) Read() ([]byte, error) {
	select {
	case b := <-f.messages:
		return b, nil
	case <-f.closed:
		return nil, io.EOF
	}
}
func (f *previewFakeSocket) Close() error { f.once.Do(func() { close(f.closed) }); return nil }
func (f *previewFakeSocket) command(t *testing.T, method string) previewCDPMessage {
	t.Helper()
	select {
	case c := <-f.commands:
		if c.Method != method {
			t.Fatalf("want %s, got %s", method, c.Method)
		}
		return c
	case <-time.After(time.Second):
		t.Fatalf("missing %s", method)
		return previewCDPMessage{}
	}
}
func (f *previewFakeSocket) frame(t *testing.T, id int, data string) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"sessionId": id, "data": data, "metadata": map[string]any{"deviceWidth": 2000, "deviceHeight": 1200, "timestamp": 1}})
	b, _ := json.Marshal(previewCDPMessage{Method: "Page.screencastFrame", Params: params})
	f.messages <- b
}
func previewTestJPEG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
func previewTestManager(t *testing.T, f *previewFakeSocket) *browserChromePreview {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			fmt.Fprint(w, `[{"id":"page-1","type":"page","url":"https://example.com","title":"Example","webSocketDebuggerUrl":"ws://malicious.test/steal"},{"id":"worker","type":"service_worker"}]`)
		case "/json/version":
			fmt.Fprint(w, `{"Browser":"HeadlessChrome/133.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	p := newBrowserChromePreview()
	p.origin = server.URL
	p.dial = func(ctx context.Context, u string) (previewSocket, error) {
		if u != "ws"+strings.TrimPrefix(server.URL, "http")+"/devtools/page/page-1" {
			t.Errorf("unsafe page socket %s", u)
		}
		return f, nil
	}
	return p
}
func TestBrowserChromePreviewStatusAndSelection(t *testing.T) {
	f := newPreviewFakeSocket()
	p := previewTestManager(t, f)
	s := &Server{browserChromePreview: p}
	w := httptest.NewRecorder()
	s.handleBrowserChromePreviewStatus(w, httptest.NewRequest("GET", "/browser-chrome/preview/status", nil))
	var status struct {
		Available bool
		Headless  bool
		Targets   []struct{ ID, URL, Title string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Available || !status.Headless || len(status.Targets) != 1 || status.Targets[0].ID != "page-1" {
		t.Fatalf("bad status %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Debugger") {
		t.Fatal("debugger socket leaked")
	}
	for _, target := range []string{"", "worker", "ws://evil.test/socket", "page-1/../other"} {
		w = httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/browser-chrome/preview/frames", nil)
		q := r.URL.Query()
		q.Set("target", target)
		r.URL.RawQuery = q.Encode()
		s.handleBrowserChromePreviewFrames(w, r)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatalf("target %q status %d", target, w.Code)
		}
	}
	select {
	case cmd := <-f.commands:
		t.Fatalf("invalid selection sent %s", cmd.Method)
	default:
	}
}
func TestBrowserChromePreviewRefcountsDeliveryAndAck(t *testing.T) {
	f := newPreviewFakeSocket()
	p := previewTestManager(t, f)
	a, offA, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer offA()
	start := f.command(t, "Page.startScreencast")
	var options map[string]any
	json.Unmarshal(start.Params, &options)
	if options["format"] != "jpeg" || options["quality"] != float64(50) || options["maxWidth"] != float64(1280) || options["maxHeight"] != float64(800) || options["everyNthFrame"] != float64(2) {
		t.Fatalf("wrong screencast options %s", start.Params)
	}
	b, offB, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer offB()
	data := previewTestJPEG(t)
	for i := 1; i <= 8; i++ {
		f.frame(t, i, data)
		ack := f.command(t, "Page.screencastFrameAck")
		var params struct {
			SessionID int `json:"sessionId"`
		}
		json.Unmarshal(ack.Params, &params)
		if params.SessionID != i {
			t.Fatalf("wrong ack %s", ack.Params)
		}
	}
	// Neither subscriber was drained: every frame still received an ack.
	for _, ch := range []<-chan previewFrame{a, b} {
		select {
		case frame := <-ch:
			if frame.Image != data || frame.Format != "jpeg" || frame.Width != 4 || frame.Height != 3 || frame.TS <= 0 {
				t.Fatalf("bad frame %+v", frame)
			}
		case <-time.After(time.Second):
			t.Fatal("no frame")
		}
	}
	offA()
	select {
	case cmd := <-f.commands:
		t.Fatalf("stopped with remaining subscriber: %s", cmd.Method)
	default:
	}
	offB()
	f.command(t, "Page.stopScreencast")
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("socket not closed")
	}
}
func TestBrowserChromePreviewErrors(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		p := newBrowserChromePreview()
		p.origin = "http://127.0.0.1:1"
		s := &Server{browserChromePreview: p}
		w := httptest.NewRecorder()
		s.handleBrowserChromePreviewStatus(w, httptest.NewRequest("GET", "/", nil))
		if !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"targets":[]`) {
			t.Fatal(w.Body.String())
		}
	})
	t.Run("dial", func(t *testing.T) {
		f := newPreviewFakeSocket()
		p := previewTestManager(t, f)
		p.dial = func(context.Context, string) (previewSocket, error) { return nil, errors.New("dial failed") }
		if _, _, err := p.subscribe(context.Background(), "page-1"); err == nil {
			t.Fatal("expected dial failure")
		}
	})
	t.Run("start", func(t *testing.T) {
		f := newPreviewFakeSocket()
		f.failMethod = "Page.startScreencast"
		p := previewTestManager(t, f)
		if _, _, err := p.subscribe(context.Background(), "page-1"); err == nil {
			t.Fatal("expected start failure")
		}
		select {
		case <-f.closed:
		case <-time.After(time.Second):
			t.Fatal("leaked failed socket")
		}
	})
	t.Run("command timeout", func(t *testing.T) {
		f := newPreviewFakeSocket()
		f.omitReply = "Page.startScreencast"
		p := previewTestManager(t, f)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, _, err := p.subscribe(ctx, "page-1"); err == nil {
			t.Fatal("expected timeout")
		}
		select {
		case <-f.closed:
		case <-time.After(time.Second):
			t.Fatal("timeout leaked socket")
		}
	})
	t.Run("disconnect", func(t *testing.T) {
		f := newPreviewFakeSocket()
		p := previewTestManager(t, f)
		frames, off, err := p.subscribe(context.Background(), "page-1")
		if err != nil {
			t.Fatal(err)
		}
		defer off()
		f.Close()
		select {
		case _, ok := <-frames:
			if ok {
				t.Fatal("expected closed stream")
			}
		case <-time.After(time.Second):
			t.Fatal("stream not closed")
		}
	})
	t.Run("ack failure", func(t *testing.T) {
		f := newPreviewFakeSocket()
		f.failMethod = "Page.screencastFrameAck"
		p := previewTestManager(t, f)
		frames, off, err := p.subscribe(context.Background(), "page-1")
		if err != nil {
			t.Fatal(err)
		}
		defer off()
		f.frame(t, 1, previewTestJPEG(t))
		timer := time.After(time.Second)
		for {
			select {
			case _, ok := <-frames:
				if !ok {
					return
				}
			case <-timer:
				t.Fatal("ack error didn't close stream")
			}
		}
	})
}
func TestBrowserChromePreviewSSECancellation(t *testing.T) {
	f := newPreviewFakeSocket()
	p := previewTestManager(t, f)
	s := &Server{browserChromePreview: p}
	s.EnableHTTPAccessLog(io.Discard)
	h := httptest.NewServer(s.httpAccessLogMiddleware(timeoutExceptEventStreams(http.HandlerFunc(s.handleBrowserChromePreviewFrames))))
	defer h.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.URL+"/browser-chrome/preview/frames?target=page-1", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("bad SSE response %+v", response)
	}
	f.command(t, "Page.startScreencast")
	f.frame(t, 1, previewTestJPEG(t))
	f.command(t, "Page.screencastFrameAck")
	buf := make([]byte, 4096)
	var received string
	for !strings.Contains(received, "data: ") {
		n, err := response.Body.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		received += string(buf[:n])
	}
	if !strings.Contains(received, `"format":"jpeg"`) {
		t.Fatal(received)
	}
	cancel()
	f.command(t, "Page.stopScreencast")
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("cancel leaked socket")
	}
}

func TestBrowserChromePreviewLateSubscriberAndRestart(t *testing.T) {
	first := newPreviewFakeSocket()
	p := previewTestManager(t, first)
	frames, offFirst, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer offFirst()
	first.command(t, "Page.startScreencast")
	data := previewTestJPEG(t)
	first.frame(t, 1, data)
	first.command(t, "Page.screencastFrameAck")
	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("no first frame")
	}
	late, offLate, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer offLate()
	select {
	case f := <-late:
		if f.Image != data {
			t.Fatal("wrong cached frame")
		}
	case <-time.After(time.Second):
		t.Fatal("late subscriber missed static page")
	}
	offFirst()
	offLate()
	first.command(t, "Page.stopScreencast")
	second := newPreviewFakeSocket()
	p.dial = func(context.Context, string) (previewSocket, error) { return second, nil }
	_, offSecond, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	second.command(t, "Page.startScreencast")
	offSecond()
	second.command(t, "Page.stopScreencast")
}

func TestBrowserChromePreviewInvalidFrameAckAndStopError(t *testing.T) {
	f := newPreviewFakeSocket()
	f.failMethod = "Page.stopScreencast"
	p := previewTestManager(t, f)
	_, off, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer off()
	f.command(t, "Page.startScreencast")
	f.frame(t, 1, "not-base64")
	f.command(t, "Page.screencastFrameAck")
	off()
	f.command(t, "Page.stopScreencast")
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("stop error leaked socket")
	}
}

func TestBrowserChromePreviewHandshakeDeadline(t *testing.T) {
	// A local server that never answers the WebSocket handshake must not hang.
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	socket, err := dialPreviewSocket(ctx, "ws"+strings.TrimPrefix(h.URL, "http"))
	if socket != nil {
		socket.Close()
	}
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("unbounded handshake: %v", err)
	}
}

type previewFailWriter struct{ deadline time.Time }

func (w *previewFailWriter) Header() http.Header       { return http.Header{} }
func (w *previewFailWriter) WriteHeader(int)           {}
func (w *previewFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (w *previewFailWriter) Flush()                    {}
func (w *previewFailWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func TestBrowserChromePreviewSSEWriteError(t *testing.T) {
	f := newPreviewFakeSocket()
	s := &Server{browserChromePreview: previewTestManager(t, f)}
	w := &previewFailWriter{}
	s.handleBrowserChromePreviewFrames(w, httptest.NewRequest("GET", "/browser-chrome/preview/frames?target=page-1", nil))
	f.command(t, "Page.startScreencast")
	f.command(t, "Page.stopScreencast")
	if !w.deadline.IsZero() {
		t.Fatal("write deadline not restored")
	}
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("write error leaked socket")
	}
}

func TestBrowserChromePreviewHeadlessReporting(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		status        int
		headless      bool
	}{
		{"ordinary product", `{"Browser":"Chrome/133.0"}`, 200, false},
		{"headless product", `{"Browser":"HeadlessChrome/133.0"}`, 200, true},
		{"ordinary UA", `{"Browser":"Chrome/133.0","User-Agent":"Mozilla/5.0 Chrome/133.0 Safari/537.36"}`, 200, false},
		{"headless UA", `{"Browser":"Chrome/133.0","User-Agent":"Mozilla/5.0 HeadlessChrome/133.0 Safari/537.36"}`, 200, true},
		{"UA only", `{"User-Agent":"HeadlessChrome/133.0"}`, 200, true},
		{"missing fields", `{}`, 200, false},
		{"empty fields", `{"Browser":"","User-Agent":""}`, 200, false},
		{"failed version", `{"Browser":"HeadlessChrome/133.0"}`, 503, false},
		{"invalid version", `not JSON`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/json/list" {
					fmt.Fprint(w, "[]")
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.version)
			}))
			defer h.Close()
			p := newBrowserChromePreview()
			p.origin = h.URL
			s := &Server{browserChromePreview: p}
			w := httptest.NewRecorder()
			s.handleBrowserChromePreviewStatus(w, httptest.NewRequest("GET", "/", nil))
			var status struct{ Available, Headless bool }
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if !status.Available || status.Headless != tc.headless {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestBrowserChromePreviewAckBacklog(t *testing.T) {
	f := newPreviewFakeSocket()
	f.omitReply = "Page.screencastFrameAck"
	p := previewTestManager(t, f)
	_, off, err := p.subscribe(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer off()
	f.command(t, "Page.startScreencast")
	for i := 1; i <= 128; i++ {
		f.frame(t, i, "invalid-base64")
		f.command(t, "Page.screencastFrameAck")
	}
	f.frame(t, 129, "invalid-base64")
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("unbounded CDP reply backlog")
	}
}

func TestBrowserChromePreviewServerCancellation(t *testing.T) {
	f := newPreviewFakeSocket()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{browserChromePreview: previewTestManager(t, f), runParentCtx: ctx}
	h := httptest.NewServer(http.HandlerFunc(s.handleBrowserChromePreviewFrames))
	defer h.Close()
	response, err := http.Get(h.URL + "/browser-chrome/preview/frames?target=page-1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	f.command(t, "Page.startScreencast")
	cancel()
	f.command(t, "Page.stopScreencast")
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("server shutdown leaked socket")
	}
}

func TestBrowserChromePreviewIPv4Origin(t *testing.T) {
	for _, tc := range []struct{ port, want string }{
		{"", "9223"}, {"invalid", "9223"}, {"0", "9223"}, {"65536", "9223"}, {" 9333 ", "9333"},
	} {
		t.Run(tc.port, func(t *testing.T) {
			t.Setenv("CHROME_DEBUG_PORT", tc.port)
			if got := newBrowserChromePreview().origin; got != "http://127.0.0.1:"+tc.want {
				t.Fatalf("origin = %q", got)
			}
		})
	}
}
