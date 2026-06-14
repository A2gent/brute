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
	if raw := strings.TrimSpace(os.Getenv(toolResultCompressionSettingKey)); raw != "" {
		return !strings.EqualFold(raw, "false")
	}
	if s == nil || s.store == nil {
		return true
	}
	settings, err := s.store.GetSettings()
	if err != nil || settings == nil {
		return true
	}
	raw := strings.TrimSpace(settings[toolResultCompressionSettingKey])
	if raw == "" {
		return true
	}
	return !strings.EqualFold(raw, "false")
}
