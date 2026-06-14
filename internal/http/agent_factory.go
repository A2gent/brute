package http

import (
	"os"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/tools"
)

const toolResultCompressionSettingKey = "A2GENT_TOOL_RESULT_COMPRESSION_ENABLED"

func (s *Server) newAgentFromConfig(cfg agent.Config, client llm.Client, manager *tools.Manager) *agent.Agent {
	cfg.CompressToolResults = s.toolResultCompressionEnabled()
	return agent.NewWithCompressor(cfg, client, manager, s.sessionManager, s.contextCompressor)
}

func (s *Server) toolResultCompressionEnabled() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(toolResultCompressionSettingKey)), "true") {
		return true
	}
	if s == nil || s.store == nil {
		return false
	}
	settings, err := s.store.GetSettings()
	if err != nil || settings == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(settings[toolResultCompressionSettingKey]), "true")
}
