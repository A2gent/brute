package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"nhooyr.io/websocket/wsjson"
)

func TestChromeExtensionTabCommandsRoundTrip(t *testing.T) {
	for _, tc := range []struct{ action, url string }{
		{"open_tab", "https://www.linkedin.com/messaging/"},
		{"navigate", "https://www.linkedin.com/messaging/"},
		{"open_tab", "HTTPS://www.linkedin.com/messaging/"},
		{"navigate", "HTTPS://www.linkedin.com/messaging/"},
	} {
		action := tc.action
		t.Run(action+"/"+tc.url, func(t *testing.T) {
			server, httpServer := newChromeExtensionWSTestServer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := registerChromeExtensionWSTestPage(t, ctx, server, httpServer, chromeExtensionRegisterRequest{
				PageID: "source", ClientID: "client", Page: chromeExtensionPageSnapshot{URL: "https://source.test", VisibilityState: "visible"},
			})
			tool := newChromeExtensionTool(server)
			results := make(chan *tools.Result, 1)
			errors := make(chan error, 1)
			go func() {
				params, _ := json.Marshal(map[string]any{"action": action, "page_id": "source", "url": tc.url})
				result, err := tool.Execute(ctx, params)
				if err != nil {
					errors <- err
					return
				}
				results <- result
			}()
			var frame chromeExtensionWSCommand
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Command == nil || frame.Command.Action != action || frame.Command.Params["url"] != "https://www.linkedin.com/messaging/" {
				t.Fatalf("unexpected navigation command: %+v", frame)
			}
			if err := server.chromeExtensionBridge.complete(frame.Command.ID, chromeExtensionResultRequest{
				PageID: "source", OK: true, Result: json.RawMessage(`{"tab_id":42,"window_id":7,"url":"https://www.linkedin.com/messaging/"}`),
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-errors:
				t.Fatal(err)
			case result := <-results:
				if !result.Success || !strings.Contains(result.Output, `"tab_id": 42`) {
					t.Fatalf("unexpected result: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestChromeExtensionTabCommandsRejectInvalidURLs(t *testing.T) {
	tool := newChromeExtensionTool(nil)
	for _, action := range []string{"open_tab", "navigate"} {
		for _, url := range []any{nil, 42, "", "/relative", "https://", "javascript:alert(1)", "data:text/html,hi", "file:///tmp/a", "chrome://extensions"} {
			params, _ := json.Marshal(map[string]any{"action": action, "url": url})
			result, err := tool.Execute(context.Background(), params)
			if err != nil || result == nil || result.Success || !strings.Contains(result.Error, "absolute HTTP(S)") {
				t.Fatalf("%s url=%v: result=%+v err=%v", action, url, result, err)
			}
		}
	}
}

func TestChromeExtensionTabCommandSchema(t *testing.T) {
	tool := newChromeExtensionTool(nil)
	props := tool.Schema()["properties"].(map[string]interface{})
	actions := props["action"].(map[string]interface{})["enum"].([]string)
	for _, action := range []string{"open_tab", "navigate", "close_tab"} {
		found := false
		for _, candidate := range actions {
			if candidate == action {
				found = true
			}
		}
		if !found || !chromeExtensionAllowedAction(action) {
			t.Fatalf("missing action %s", action)
		}
	}
	if props["url"] == nil {
		t.Fatal("missing URL property")
	}
}

func TestChromeExtensionCloseTabCommandRoundTrip(t *testing.T) {
	server, httpServer := newChromeExtensionWSTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := registerChromeExtensionWSTestPage(t, ctx, server, httpServer, chromeExtensionRegisterRequest{
		PageID: "source", ClientID: "client", Page: chromeExtensionPageSnapshot{URL: "https://source.test", VisibilityState: "visible"},
	})
	tool := newChromeExtensionTool(server)
	results := make(chan *tools.Result, 1)
	errors := make(chan error, 1)
	go func() {
		result, err := tool.Execute(ctx, json.RawMessage(`{"action":"close_tab","page_id":"source"}`))
		if err != nil {
			errors <- err
			return
		}
		results <- result
	}()
	var frame chromeExtensionWSCommand
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Command == nil || frame.Command.Action != "close_tab" {
		t.Fatalf("unexpected close command: %+v", frame)
	}
	if len(frame.Command.Params) != 0 {
		t.Fatalf("close_tab should not need parameters: %#v", frame.Command.Params)
	}
	if err := server.chromeExtensionBridge.complete(frame.Command.ID, chromeExtensionResultRequest{
		PageID: "source", OK: true, Result: json.RawMessage(`{"tab_id":17,"closed":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	case result := <-results:
		if !result.Success || !strings.Contains(result.Output, `"closed": true`) {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
