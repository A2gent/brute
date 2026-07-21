package http

import (
	"os"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const toolResultCompressionSettingKey = "A2GENT_TOOL_RESULT_COMPRESSION_ENABLED"
const runtimeReasoningPersistenceSettingKey = "A2GENT_CLAUDE_RUNTIME_REASONING_PERSISTENCE_ENABLED"

func (s *Server) agentConfigFromTarget(sess *session.Session, target *executionTarget, systemPrompt string, maxSteps int, temperature float64) agent.Config {
	name := ""
	if sess != nil {
		name = sess.AgentID
	}
	cfg := agent.Config{
		Name:                    name,
		Provider:                string(target.ProviderType),
		Model:                   target.Model,
		SystemPrompt:            systemPrompt,
		MaxSteps:                maxSteps,
		Temperature:             temperature,
		ContextWindow:           target.ContextWindow,
		UsePreviousResponse:     target.StatefulResponses,
		UseProviderSession:      target.ProviderSessions,
		ProviderSessionIdentity: target.ProviderSessionIdentity,
	}
	if target.ProviderType == config.ProviderAnthropic {
		cfg.PersistRuntimeReasoning = s.runtimeReasoningPersistenceEnabled()
	}
	return cfg
}

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

func (s *Server) runtimeReasoningPersistenceEnabled() bool {
	if raw := strings.TrimSpace(os.Getenv(runtimeReasoningPersistenceSettingKey)); raw != "" {
		return !strings.EqualFold(raw, "false")
	}
	if s == nil || s.store == nil {
		return false
	}
	settings, err := s.store.GetSettings()
	if err != nil || settings == nil {
		return false
	}
	raw := strings.TrimSpace(settings[runtimeReasoningPersistenceSettingKey])
	if raw == "" {
		return false
	}
	return !strings.EqualFold(raw, "false")
}
