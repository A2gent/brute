package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func TestBrowserChromeNavigatesSelfSignedHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Chrome test in short mode")
	}
	binary, found := launcher.LookPath()
	if !found {
		t.Skip("no Chrome binary installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	l := launcher.New().Context(ctx).Bin(binary).Headless(true).RemoteDebuggingPort(0).UserDataDir(t.TempDir())
	controlURL, err := l.Launch()
	if err != nil {
		t.Fatalf("launch isolated Chrome: %v", err)
	}
	t.Cleanup(func() { l.Kill(); l.Cleanup() })
	browser := rod.New().Context(ctx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		t.Fatalf("connect isolated Chrome: %v", err)
	}

	const marker = "self-signed HTTPS content"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body>"+marker+"</body></html>")
	}))
	t.Cleanup(server.Close)

	// Prove Chrome rejects the fixture before the production setup enables bypass.
	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatalf("create control page: %v", err)
	}
	if err := page.Navigate(server.URL); err == nil || !strings.Contains(err.Error(), "ERR_CERT_AUTHORITY_INVALID") {
		t.Fatalf("expected untrusted certificate rejection before setup, got %v", err)
	}

	endpoint, err := url.Parse(controlURL)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewBrowserChromeTool(t.TempDir())
	tool.debugPort = endpoint.Port()
	for _, action := range []map[string]interface{}{
		{"action": "navigate", "url": server.URL},
		{"action": "get_text"},
		{"action": "navigate", "url": server.URL + "/reconnected"},
		{"action": "get_text"},
	} {
		input, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(ctx, input)
		if err != nil {
			t.Fatalf("%s: %v", action["action"], err)
		}
		if !result.Success {
			t.Fatalf("%s failed: %s", action["action"], result.Error)
		}
		if action["action"] == "get_text" && !strings.Contains(result.Output, marker) {
			t.Fatalf("expected HTTPS page content, got %q", result.Output)
		}
	}
}
