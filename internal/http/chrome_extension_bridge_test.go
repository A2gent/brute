package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestChromeExtensionToolRoundTripQueuesCommandForRegisteredPage(t *testing.T) {
	server, httpServer := newChromeExtensionWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := registerChromeExtensionWSTestPage(t, ctx, server, httpServer, chromeExtensionRegisterRequest{
		PageID: "page-1", ClientID: "client-1", ExtensionVersion: "0.1.0",
		Page: chromeExtensionPageSnapshot{
			URL: "https://example.test/app", Title: "Example App", VisibilityState: "visible",
		},
	})

	tool, ok := server.toolManager.Get("chrome_extension")
	if !ok {
		t.Fatalf("expected chrome_extension tool to be registered")
	}

	resultCh := make(chan *tools.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := tool.Execute(ctx, json.RawMessage(`{"action":"eval","page_id":"page-1","script":"document.title","timeout_ms":1500}`))
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	var frame chromeExtensionWSCommand
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatalf("read command: %v", err)
	}
	if frame.Type != "command" || frame.PageID != "page-1" || frame.Command == nil {
		t.Fatalf("unexpected command frame: %+v", frame)
	}
	if frame.Command.Action != "eval" {
		t.Fatalf("expected eval command, got %q", frame.Command.Action)
	}
	if frame.Command.Params["script"] != "document.title" {
		t.Fatalf("expected script to be forwarded, got %#v", frame.Command.Params)
	}

	postJSON(t, server, http.MethodPost, "/browser-extension/commands/"+frame.Command.ID+"/result", map[string]any{
		"page_id": "page-1",
		"ok":      true,
		"result":  map[string]any{"value": "Example App"},
	})

	select {
	case err := <-errCh:
		t.Fatalf("tool execute failed: %v", err)
	case result := <-resultCh:
		if result == nil || !result.Success {
			t.Fatalf("expected successful tool result, got %#v", result)
		}
		if !strings.Contains(result.Output, `"value": "Example App"`) {
			t.Fatalf("expected extension result in tool output, got %s", result.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool result")
	}
}

func TestChromeExtensionToolListsActivePages(t *testing.T) {
	server, httpServer := newChromeExtensionWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registerChromeExtensionWSTestPage(t, ctx, server, httpServer, chromeExtensionRegisterRequest{
		PageID: "visible-page", ClientID: "client-1",
		Page: chromeExtensionPageSnapshot{
			URL: "https://example.test/visible", Title: "Visible", VisibilityState: "visible",
		},
	})

	tool, ok := server.toolManager.Get("chrome_extension")
	if !ok {
		t.Fatalf("expected chrome_extension tool to be registered")
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"list_pages"}`))
	if err != nil {
		t.Fatalf("tool execute failed: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful list_pages result, got %#v", result)
	}
	if !strings.Contains(result.Output, `"page_id": "visible-page"`) || !strings.Contains(result.Output, `"url": "https://example.test/visible"`) {
		t.Fatalf("expected registered page in output, got %s", result.Output)
	}
}

// A successful write only sends the frame; wait for server-side registration
// before invoking a tool so the test does not race the WebSocket reader.
func registerChromeExtensionWSTestPage(t *testing.T, ctx context.Context, server *Server, httpServer *httptest.Server, req chromeExtensionRegisterRequest) *websocket.Conn {
	t.Helper()
	conn, _, err := dialChromeExtensionWS(t, ctx, httpServer, "chrome-extension://abcdef")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, chromeExtensionWSMessage{Type: "register", chromeExtensionRegisterRequest: req}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for {
		for _, page := range server.chromeExtensionBridge.listPages() {
			if page["page_id"] == req.PageID {
				return conn
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("page was not registered over WebSocket: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestChromeExtensionLegacyRoutesRemoved(t *testing.T) {
	server, _ := newChromeExtensionWSTestServer(t)
	for _, path := range []string{
		"/browser-extension/pages/register",
		"/browser-extension/pages/page-1/poll",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"page_id":"page-1"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected removed route to return 404, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if pages := server.chromeExtensionBridge.listPages(); len(pages) != 0 {
		t.Fatalf("legacy requests must not register pages, got %v", pages)
	}
}

func postJSON(t *testing.T, server *Server, method, path string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to encode request body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, rec.Code, rec.Body.String())
	}
	return rec
}
