package http

import (
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/tools"
)

func (s *Server) registerRelevanceGateTool(manager *tools.Manager) {
	if s == nil || manager == nil {
		return
	}
	if s.config != nil && s.config.Tools.RelevanceGateDisabled {
		manager.Unregister("relevance_gate")
		return
	}
	apiKey, model, baseURL := s.resolveClassifyCredentials()
	var client *jev.Client
	if apiKey != "" {
		client = jev.NewClient(apiKey, model, baseURL)
	}
	// No credentials must be lossless as well: keep the gate available, using
	// full inclusion (except secrets), rather than requiring workflow rewrites.
	manager.Register(tools.NewRelevanceGateTool(manager.WorkDir(), client))
}
