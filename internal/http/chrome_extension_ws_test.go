package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func newChromeExtensionWSTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	httpServer := httptest.NewServer(server.router)
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func dialChromeExtensionWS(t *testing.T, ctx context.Context, httpServer *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/browser-extension/ws"
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
}

func TestChromeExtensionWSPushesCommandsAndFailsThemOnDisconnect(t *testing.T) {
	server, httpServer := newChromeExtensionWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := dialChromeExtensionWS(t, ctx, httpServer, "chrome-extension://abcdef")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	if err := wsjson.Write(ctx, c, map[string]any{"type": "register", "page_id": "page-ws", "page": map[string]any{"title": "WS Page"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Register is processed asynchronously; wait until the page is listed.
	deadline := time.Now().Add(2 * time.Second)
	for len(server.chromeExtensionBridge.listPages()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("page was not registered over WebSocket")
		}
		time.Sleep(10 * time.Millisecond)
	}

	tool, _ := server.toolManager.Get("chrome_extension")
	resultCh := make(chan *tools.Result, 1)
	go func() {
		result, _ := tool.Execute(ctx, json.RawMessage(`{"action":"get_text","page_id":"page-ws","timeout_ms":3000}`))
		resultCh <- result
	}()

	var frame chromeExtensionWSCommand
	if err := wsjson.Read(ctx, c, &frame); err != nil {
		t.Fatalf("read command: %v", err)
	}
	if frame.Type != "command" || frame.PageID != "page-ws" || frame.Command == nil || frame.Command.Action != "get_text" {
		t.Fatalf("unexpected command frame: %+v", frame)
	}

	// Closing the socket must fail the in-flight command instead of waiting for the tool timeout.
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case result := <-resultCh:
		if result == nil || result.Success || !strings.Contains(result.Error, "disconnected") {
			t.Fatalf("expected disconnect failure, got %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("tool did not fail after WebSocket disconnect")
	}
	if pages := server.chromeExtensionBridge.listPages(); len(pages) != 0 {
		t.Fatalf("expected page to be removed after disconnect, got %v", pages)
	}
}

func TestChromeExtensionWSRejectsWebsiteOrigins(t *testing.T) {
	_, httpServer := newChromeExtensionWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, resp, err := dialChromeExtensionWS(t, ctx, httpServer, "https://evil.example")
	if err == nil {
		t.Fatalf("expected website origin to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %+v", resp)
	}
}
