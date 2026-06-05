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

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestChromeExtensionToolRoundTripQueuesCommandForRegisteredPage(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	postJSON(t, server, http.MethodPost, "/browser-extension/pages/register", map[string]any{
		"page_id":           "page-1",
		"client_id":         "client-1",
		"extension_version": "0.1.0",
		"page": map[string]any{
			"url":              "https://example.test/app",
			"title":            "Example App",
			"visibility_state": "visible",
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

	pollRec := postJSON(t, server, http.MethodPost, "/browser-extension/pages/page-1/poll", map[string]any{
		"client_id": "client-1",
		"page": map[string]any{
			"url":              "https://example.test/app",
			"title":            "Example App",
			"visibility_state": "visible",
		},
	})

	var pollResp struct {
		Command *struct {
			ID     string         `json:"id"`
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		} `json:"command"`
	}
	if err := json.Unmarshal(pollRec.Body.Bytes(), &pollResp); err != nil {
		t.Fatalf("failed to decode poll response: %v", err)
	}
	if pollResp.Command == nil {
		t.Fatalf("expected queued command in poll response, got %s", pollRec.Body.String())
	}
	if pollResp.Command.Action != "eval" {
		t.Fatalf("expected eval command, got %q", pollResp.Command.Action)
	}
	if pollResp.Command.Params["script"] != "document.title" {
		t.Fatalf("expected script to be forwarded, got %#v", pollResp.Command.Params)
	}

	postJSON(t, server, http.MethodPost, "/browser-extension/commands/"+pollResp.Command.ID+"/result", map[string]any{
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
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	postJSON(t, server, http.MethodPost, "/browser-extension/pages/register", map[string]any{
		"page_id":   "visible-page",
		"client_id": "client-1",
		"page": map[string]any{
			"url":              "https://example.test/visible",
			"title":            "Visible",
			"visibility_state": "visible",
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
