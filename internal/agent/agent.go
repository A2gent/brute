package agent

import (
	"context"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

// Agent represents an AI agent that can execute tasks
type Agent struct {
	config         Config
	llmClient      llm.Client
	toolManager    *tools.Manager
	sessionManager *session.Manager
	compressor     *contextcompress.Compressor
}

// New creates a new agent
func New(config Config, llmClient llm.Client, toolManager *tools.Manager, sessionManager *session.Manager) *Agent {
	return NewWithCompressor(config, llmClient, toolManager, sessionManager, nil)
}

// NewWithCompressor creates an agent with optional pre-send tool-result compression.
func NewWithCompressor(config Config, llmClient llm.Client, toolManager *tools.Manager, sessionManager *session.Manager, compressor *contextcompress.Compressor) *Agent {
	if config.MaxSteps == 0 {
		config.MaxSteps = 100
	}
	systemPromptExplicit := config.SystemPrompt != ""
	if config.SystemPrompt == "" {
		config.SystemPrompt = strings.TrimSpace(os.Getenv(envSystemPrompt))
		if config.SystemPrompt == "" {
			config.SystemPrompt = defaultSystemPrompt
		}
	}
	appendPrompt := strings.TrimSpace(os.Getenv(envSystemPromptAppend))
	if appendPrompt != "" && !systemPromptExplicit {
		config.SystemPrompt = strings.TrimSpace(config.SystemPrompt) + "\n\n" + appendPrompt
	}
	if !config.CompressToolResults {
		config.CompressToolResults = strings.EqualFold(strings.TrimSpace(os.Getenv(envCompressToolResults)), "true")
	}
	if compressor == nil && sessionManager != nil {
		compressor = contextcompress.NewCompressorWithSessionStore(contextcompress.Config{Enabled: true}, sessionManager)
	}
	if config.CompressToolResults && compressor != nil && toolManager != nil {
		if _, ok := toolManager.Get(contextcompress.RetrievalToolName); !ok {
			toolManager.Register(contextcompress.NewRetrieveTool(compressor))
		}
	}

	return &Agent{
		config:         config,
		llmClient:      llmClient,
		toolManager:    toolManager,
		sessionManager: sessionManager,
		compressor:     compressor,
	}
}

// Run executes the agent with the given task
// Returns the response content and total token usage
func (a *Agent) Run(ctx context.Context, sess *session.Session, task string) (string, llm.TokenUsage, error) {
	return a.RunWithEvents(ctx, sess, task, nil)
}

// RunWithEvents executes the agent and emits streaming events when available.
func (a *Agent) RunWithEvents(ctx context.Context, sess *session.Session, task string, onEvent func(Event)) (string, llm.TokenUsage, error) {
	logging.Info("Agent run started: session=%s", sess.ID)
	// Note: User message is already added by the TUI before calling Run
	// Run the agentic loop
	result, usage, err := a.loop(ctx, sess, onEvent)
	if err != nil {
		logging.Error("Agent run failed: %v", err)
	} else {
		logging.Info("Agent run completed: total_input=%d total_output=%d", usage.InputTokens, usage.OutputTokens)
	}
	return result, usage, err
}
