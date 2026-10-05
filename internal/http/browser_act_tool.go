package http

import (
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// registerBrowserActTool opts the Jev-driven browser loop in. It shares the registered
// browser_chrome instance so both tools contend for the same Chrome operation gate; without that
// shared instance the loop could interleave with plain browser_chrome calls mid-sequence.
func (s *Server) registerBrowserActTool(manager *tools.Manager) {
	if s == nil || manager == nil {
		return
	}
	if s.config == nil || !s.config.Tools.BrowserActEnabled {
		manager.Unregister("browser_act")
		return
	}
	apiKey, model, baseURL := s.resolveClassifyCredentials()
	if apiKey == "" {
		// Unlike relevance_gate there is no lossless fallback: no classifier means no loop, so the
		// tool is simply absent and browser_chrome stays the way to drive the browser.
		manager.Unregister("browser_act")
		return
	}
	registered, ok := manager.Get("browser_chrome")
	if !ok {
		manager.Unregister("browser_act")
		return
	}
	chrome, ok := registered.(*integrationtools.BrowserChromeTool)
	if !ok {
		manager.Unregister("browser_act")
		return
	}
	manager.Register(integrationtools.NewBrowserActTool(chrome, jev.NewClient(apiKey, model, baseURL)))
}
