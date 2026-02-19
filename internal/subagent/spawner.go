package subagent

import (
	"context"
	"fmt"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

// AgentType defines available sub-agent types
type AgentType string

const (
	AgentTypeGeneral   AgentType = "general"
	AgentTypeExplore   AgentType = "explore"
	AgentTypeDeveloper AgentType = "developer"
	AgentTypeTester    AgentType = "tester"
	AgentTypeDocs      AgentType = "docs"
)

// Spawner handles sub-agent creation and execution
type Spawner struct {
	parentSessionID string
	llmClient       llm.Client
	toolManager     *tools.Manager
	sessionManager  *session.Manager
	model           string
}

// NewSpawner creates a new sub-agent spawner
func NewSpawner(
	parentSessionID string,
	llmClient llm.Client,
	toolManager *tools.Manager,
	sessionManager *session.Manager,
	model string,
) *Spawner {
	return &Spawner{
		parentSessionID: parentSessionID,
		llmClient:       llmClient,
		toolManager:     toolManager,
		sessionManager:  sessionManager,
		model:           model,
	}
}

// Spawn creates and runs a sub-agent
func (s *Spawner) Spawn(ctx context.Context, agentType string, prompt string, parentContext []byte) (string, error) {
	// Get agent config based on type
	config := s.getAgentConfig(AgentType(agentType))

	// Create sub-session
	subSession, err := s.sessionManager.CreateWithParent(agentType, s.parentSessionID)
	if err != nil {
		return "", fmt.Errorf("failed to create sub-session: %w", err)
	}

	// Create sub-agent
	subAgent := agent.New(config, s.llmClient, s.toolManager, s.sessionManager)

	subSession.AddUserMessage(prompt)

	// Run sub-agent (we ignore token usage from sub-agents for now)
	result, _, err := subAgent.Run(ctx, subSession, prompt)
	if err != nil {
		return "", fmt.Errorf("sub-agent error: %w", err)
	}

	return result, nil
}

// getAgentConfig returns configuration for a specific agent type
func (s *Spawner) getAgentConfig(agentType AgentType) agent.Config {
	base := agent.Config{
		Name:        string(agentType),
		Model:       s.model,
		MaxSteps:    25, // Sub-agents have lower step limit
		Temperature: 0.0,
	}

	switch agentType {
	case AgentTypeGeneral:
		base.Description = "General-purpose agent for research and multi-step tasks"
		base.SystemPrompt = generalAgentPrompt

	case AgentTypeExplore:
		base.Description = "Fast read-only agent for codebase exploration"
		base.SystemPrompt = exploreAgentPrompt
		base.MaxSteps = 15 // Explore should be fast

	case AgentTypeDeveloper:
		base.Description = "Code implementation and debugging"
		base.SystemPrompt = developerAgentPrompt

	case AgentTypeTester:
		base.Description = "Code review and test writing"
		base.SystemPrompt = testerAgentPrompt

	case AgentTypeDocs:
		base.Description = "Documentation generation"
		base.SystemPrompt = docsAgentPrompt

	default:
		base.Description = "General-purpose sub-agent"
		base.SystemPrompt = generalAgentPrompt
	}

	return base
}

const generalAgentPrompt = `You are a general-purpose sub-agent. Your task is to complete the specific task assigned to you and return a clear, concise result.

Focus on:
- Completing the assigned task thoroughly
- Using tools efficiently
- Returning useful results to the parent agent

Be direct and efficient. Complete the task and summarize your findings.`

const exploreAgentPrompt = `You are a fast exploration agent. Your task is to quickly find information in the codebase.

Focus on:
- Using glob to find files by pattern
- Using grep to search file contents
- Using read to examine specific files
- Providing clear, organized summaries

Do NOT modify any files. Be fast and thorough in your exploration.`

const developerAgentPrompt = `You are a development agent. Your task is to implement code changes.

Focus on:
- Reading existing code to understand context
- Making minimal, targeted changes
- Following existing code style
- Testing changes with bash commands when possible

Implement the requested changes efficiently and verify they work.`

const testerAgentPrompt = `You are a testing and review agent. Your task is to review code and write tests.

Focus on:
- Analyzing code for potential issues
- Identifying edge cases
- Writing comprehensive tests
- Providing actionable feedback

Be thorough but constructive in your review.`

const docsAgentPrompt = `You are a documentation agent. Your task is to create or update documentation.

Focus on:
- Clear, concise explanations
- Proper formatting (Markdown)
- Code examples where helpful
- Keeping docs up-to-date with code

Write documentation that helps users understand and use the code effectively.`

// Ensure Spawner implements tools.SubAgentSpawner
var _ tools.SubAgentSpawner = (*Spawner)(nil)
