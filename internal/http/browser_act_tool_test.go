package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestBrowserActRegistration(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)

	// Off by default: the prototype must not appear in tool definitions unless opted in.
	if _, ok := server.toolManager.Get("browser_act"); ok {
		t.Fatal("browser_act must not be registered without the config flag")
	}

	server.config.Tools.BrowserActEnabled = true
	server.registerBrowserActTool(server.toolManager)
	if _, ok := server.toolManager.Get("browser_act"); !ok {
		t.Fatal("browser_act must register when enabled with credentials")
	}

	// Without a classifier there is no loop, so the tool is absent rather than degraded.
	server.config.Providers["jev"] = config.Provider{}
	server.registerBrowserActTool(server.toolManager)
	if _, ok := server.toolManager.Get("browser_act"); ok {
		t.Fatal("browser_act must unregister without Jev credentials")
	}
	if _, ok := server.toolManager.Get("browser_chrome"); !ok {
		t.Fatal("browser_act registration must never remove browser_chrome")
	}
}
