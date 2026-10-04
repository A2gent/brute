package http

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Rod uses the non-RFC key "nil", which Chrome accepts but nhooyr rejects.
// Adapt only the test server handshake: nhooyr validates a standard key while
// the wire response retains the accept hash for Rod's original key.
type previewTransportHandshakeWriter struct {
	http.ResponseWriter
	accept string
}

func (w previewTransportHandshakeWriter) WriteHeader(status int) {
	if status == http.StatusSwitchingProtocols {
		w.Header().Set("Sec-WebSocket-Accept", w.accept)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w previewTransportHandshakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func TestBrowserChromePreviewWebSocketTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	image := previewTestJPEG(t)
	ackReplied := make(chan struct{})
	stopReplied := make(chan struct{})
	serverResult := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"id":"page-1","type":"page","url":"https://example.com","title":"Example"}]`)
		case "/devtools/page/page-1":
			// Report handler errors to the test goroutine; all socket operations
			// share a deadline so failures cannot strand a hijacked connection.
			serverResult <- func() error {
				if key := r.Header.Get("Sec-WebSocket-Key"); key == "nil" {
					hash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
					r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
					w = previewTransportHandshakeWriter{w, base64.StdEncoding.EncodeToString(hash[:])}
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return fmt.Errorf("accept WebSocket: %w", err)
				}
				defer conn.CloseNow()
				lastID := 0
				readCommand := func(method string) (previewCDPMessage, error) {
					var cmd previewCDPMessage
					if err := wsjson.Read(ctx, conn, &cmd); err != nil {
						return cmd, fmt.Errorf("read %s: %w", method, err)
					}
					if cmd.Method != method || cmd.ID <= lastID {
						return cmd, fmt.Errorf("want %s with ID > %d, got %+v", method, lastID, cmd)
					}
					lastID = cmd.ID
					return cmd, nil
				}
				reply := func(cmd previewCDPMessage) error {
					return wsjson.Write(ctx, conn, struct {
						previewCDPMessage
						Result json.RawMessage `json:"result"`
					}{previewCDPMessage{ID: cmd.ID}, json.RawMessage(`{}`)})
				}

				start, err := readCommand("Page.startScreencast")
				if err != nil {
					return err
				}
				var options struct {
					Format        string `json:"format"`
					Quality       int    `json:"quality"`
					MaxWidth      int    `json:"maxWidth"`
					MaxHeight     int    `json:"maxHeight"`
					EveryNthFrame int    `json:"everyNthFrame"`
				}
				if err := json.Unmarshal(start.Params, &options); err != nil {
					return fmt.Errorf("decode start options: %w", err)
				}
				if options.Format != "jpeg" || options.Quality != 50 || options.MaxWidth != 1280 || options.MaxHeight != 800 || options.EveryNthFrame != 2 {
					return fmt.Errorf("unexpected start options: %s", start.Params)
				}
				if err := reply(start); err != nil {
					return fmt.Errorf("reply to start: %w", err)
				}
				params, err := json.Marshal(map[string]any{"data": image, "sessionId": 42})
				if err != nil {
					return err
				}
				if err := wsjson.Write(ctx, conn, previewCDPMessage{Method: "Page.screencastFrame", Params: params}); err != nil {
					return fmt.Errorf("send frame: %w", err)
				}
				ack, err := readCommand("Page.screencastFrameAck")
				if err != nil {
					return err
				}
				var ackParams struct {
					SessionID int `json:"sessionId"`
				}
				if err := json.Unmarshal(ack.Params, &ackParams); err != nil {
					return fmt.Errorf("decode ack: %w", err)
				}
				if ackParams.SessionID != 42 {
					return fmt.Errorf("unexpected ack: %s", ack.Params)
				}
				if err := reply(ack); err != nil {
					return fmt.Errorf("reply to ack: %w", err)
				}
				close(ackReplied)
				stop, err := readCommand("Page.stopScreencast")
				if err != nil {
					return err
				}
				if err := reply(stop); err != nil {
					return fmt.Errorf("reply to stop: %w", err)
				}
				close(stopReplied)
				// The client, not the server, must close the transport after stop.
				_, _, closeErr := conn.Read(ctx)
				if closeErr == nil {
					return fmt.Errorf("received another message instead of socket closure")
				}
				if err := ctx.Err(); err != nil {
					return fmt.Errorf("socket closure timed out: %w", err)
				}
				// Rod closes TCP directly rather than sending a WebSocket close
				// frame. Accept EOF, but not protocol errors or cancellation.
				if !errors.Is(closeErr, io.EOF) && !errors.Is(closeErr, io.ErrUnexpectedEOF) && websocket.CloseStatus(closeErr) != websocket.StatusNormalClosure {
					return fmt.Errorf("unexpected socket closure error: %w", closeErr)
				}
				return nil
			}()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(cancel)

	p := newBrowserChromePreview()
	p.origin = server.URL
	// Keep the production dialPreviewSocket: no fake socket or dial injection.
	t.Cleanup(p.client.CloseIdleConnections)
	frames, unsubscribe, err := p.subscribe(ctx, "page-1")
	if err != nil {
		t.Fatalf("subscribe over real WebSocket: %v", err)
	}
	t.Cleanup(unsubscribe)
	select {
	case frame, ok := <-frames:
		if !ok || frame.Image != image || frame.Format != "jpeg" || frame.Width != 4 || frame.Height != 3 || frame.TS <= 0 {
			t.Fatalf("unexpected frame: open=%t, frame=%+v", ok, frame)
		}
	case err := <-serverResult:
		t.Fatalf("CDP server ended before frame delivery: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for frame")
	}
	wait := func(done <-chan struct{}, operation string) {
		t.Helper()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", operation)
		}
	}
	wait(ackReplied, "frame acknowledgement reply")
	unsubscribed := make(chan struct{})
	go func() {
		unsubscribe()
		close(unsubscribed)
	}()
	wait(unsubscribed, "unsubscribe")
	wait(stopReplied, "stop reply")
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for client socket closure")
	}
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("frame channel still open after unsubscribe")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for frame channel closure")
	}
	p.mu.Lock()
	streams, gates := len(p.streams), len(p.gates)
	p.mu.Unlock()
	if streams != 0 || gates != 0 {
		t.Fatalf("unsubscribe leaked %d streams and %d gates", streams, gates)
	}
}
