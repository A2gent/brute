// subagent_prompt.go keeps sub-agent prompt composition separate from generic server handlers.
package http

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"strings"
)

func (s *Server) resolveSubAgentsSection() (string, int) {
	agents, err := s.store.ListSubAgents()
	if err != nil {
		logging.Warn("Failed to list sub-agents for system prompt: %v", err)
		return "", 0
	}
	if len(agents) == 0 {
		return "", 0
	}

	lines := make([]string, 0, len(agents)+4)
	lines = append(lines, "Available sub-agents for delegation:")
	lines = append(lines, "Use the delegate_to_subagent tool to delegate tasks to these sub-agents. Each sub-agent runs in its own session with its own context window.")
	lines = append(lines, "")
	for _, sa := range agents {
		providerLabel := strings.TrimSpace(sa.Provider)
		if providerLabel == "" {
			providerLabel = "default"
		}
		modelLabel := strings.TrimSpace(sa.Model)
		if modelLabel == "" {
			modelLabel = "default"
		}
		toolsLabel := "all tools"
		if len(sa.EnabledTools) > 0 {
			toolsLabel = fmt.Sprintf("%d tools", len(sa.EnabledTools))
		}
		lines = append(lines, fmt.Sprintf("- ID: %s | Name: %s | Provider: %s | Model: %s | Tools: %s",
			sa.ID, sa.Name, providerLabel, modelLabel, toolsLabel))
	}

	section := strings.Join(lines, "\n")
	return section, estimateTokensApprox(section)
}

// composeSubAgentSystemPromptSnapshot builds a system prompt snapshot for a
// sub-agent using its own instruction blocks configuration. It follows the same
// block-resolution logic as the main agent but uses a sub-agent-specific base
// prompt and omits thinking blocks, project_agents_md, and the sub-agents
// listing (to prevent recursion).
func (s *Server) composeSubAgentSystemPromptSnapshot(sa *storage.SubAgent, sess *session.Session) *systemPromptSnapshot {
	rawBlocks := strings.TrimSpace(sa.InstructionBlocks)
	blocks := []configuredInstructionBlock{}
	if rawBlocks != "" && rawBlocks != "[]" {
		if err := json.Unmarshal([]byte(rawBlocks), &blocks); err != nil {
			logging.Warn("Failed to parse sub-agent instruction_blocks for %s: %v", sa.ID, err)
			blocks = []configuredInstructionBlock{}
		}
	}

	if len(blocks) == 0 {
		enabled := true
		blocks = []configuredInstructionBlock{
			{Type: builtInToolsInstructionBlockType, Value: "", Enabled: &enabled},
		}
	}

	hasBuiltInBlock := false
	hasIntegrationBlock := false
	hasExternalMarkdownBlock := false
	hasMCPServersBlock := false
	for _, block := range blocks {
		switch strings.TrimSpace(block.Type) {
		case builtInToolsInstructionBlockType:
			hasBuiltInBlock = true
		case integrationSkillsInstructionBlockType:
			hasIntegrationBlock = true
		case externalMarkdownSkillsInstructionBlockType:
			hasExternalMarkdownBlock = true
		case mcpServersInstructionBlockType:
			hasMCPServersBlock = true
		}
	}
	prefixedBlocks := make([]configuredInstructionBlock, 0, 4+len(blocks))
	if !hasBuiltInBlock {
		disabled := false
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: builtInToolsInstructionBlockType, Value: "", Enabled: &disabled})
	}
	if !hasIntegrationBlock {
		disabled := false
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: integrationSkillsInstructionBlockType, Value: "", Enabled: &disabled})
	}
	if !hasExternalMarkdownBlock {
		disabled := false
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: externalMarkdownSkillsInstructionBlockType, Value: "", Enabled: &disabled})
	}
	if !hasMCPServersBlock {
		disabled := false
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: mcpServersInstructionBlockType, Value: "", Enabled: &disabled})
	}
	blocks = append(prefixedBlocks, blocks...)

	builtInToolsEnabled := false
	for _, block := range blocks {
		if strings.TrimSpace(block.Type) != builtInToolsInstructionBlockType {
			continue
		}
		builtInToolsEnabled = block.Enabled == nil || *block.Enabled
		break
	}

	identityPrompt := fmt.Sprintf(`You are a sub-agent named "%s". You have been delegated a specific task by the main agent.

Guidelines:
- Focus exclusively on completing the delegated task
- Be concise and efficient — your output will be returned to the main agent
- Use available tools to accomplish the task
- If the task is unclear, do your best to complete it based on context
- When done, provide a clear summary of what you accomplished`, sa.Name)

	var basePrompt string
	if builtInToolsEnabled {
		basePrompt = identityPrompt + "\n\n" + agent.DefaultSystemPrompt()
	} else {
		basePrompt = identityPrompt
	}

	resolvedBlocks := make([]systemPromptBlockSnapshot, 0, len(blocks))
	resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
		Type:            builtInToolsInstructionBlockType,
		Value:           "",
		Enabled:         builtInToolsEnabled,
		ResolvedContent: "Controls whether built-in tool guidance is included in the base system prompt.",
		EstimatedTokens: subAgentBuiltInToolsEstimatedTokens(builtInToolsEnabled),
	})
	appendSections := make([]string, 0, len(blocks))
	sectionNumber := 0

	settings, _ := s.store.GetSettings()
	if settings == nil {
		settings = map[string]string{}
	}

	environmentContext, environmentContextTokens := s.resolveEnvironmentContextSection(sess)
	if environmentContext != "" {
		appendSections = append(appendSections, environmentContext)
		resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
			Type:            "environment_context",
			Value:           "",
			Enabled:         true,
			ResolvedContent: environmentContext,
			EstimatedTokens: environmentContextTokens,
		})
	}

	for _, block := range blocks {
		blockType := strings.TrimSpace(block.Type)
		if blockType == builtInToolsInstructionBlockType {
			continue
		}
		sectionNumber++

		enabled := block.Enabled == nil || *block.Enabled
		blockSnapshot := systemPromptBlockSnapshot{
			Type:    blockType,
			Value:   strings.TrimSpace(block.Value),
			Enabled: enabled,
		}
		if !enabled {
			resolvedBlocks = append(resolvedBlocks, blockSnapshot)
			continue
		}

		value := blockSnapshot.Value
		switch blockType {
		case integrationSkillsInstructionBlockType:
			section, resolveErr := s.resolveIntegrationSkillsSection(sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimateTokensApprox(section)
			appendSections = append(appendSections, section)
		case externalMarkdownSkillsInstructionBlockType:
			section, estimatedTokens, resolveErr := s.resolveExternalMarkdownSkillsSection(settings, sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimatedTokens
			appendSections = append(appendSections, section)
		case mcpServersInstructionBlockType:
			section, estimatedTokens, resolveErr := s.resolveMCPServersSection(sectionNumber)
			blockSnapshot.ResolvedContent = section
			blockSnapshot.Error = resolveErr
			if section == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.EstimatedTokens = estimatedTokens
			appendSections = append(appendSections, section)
		case "text":
			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.ResolvedContent = value
			rendered := fmt.Sprintf("Instruction block %d (text):\n%s", sectionNumber, blockSnapshot.ResolvedContent)
			blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
			appendSections = append(appendSections, rendered)
		case "file":
			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.SourcePath = value
			content, readErr := s.readInstructionFileBlock(value)
			if readErr != nil {
				blockSnapshot.Error = readErr.Error()
				rendered := fmt.Sprintf("Instruction block %d (file):\nUnable to load file %s: %s", sectionNumber, value, readErr.Error())
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			} else {
				blockSnapshot.ResolvedContent = content
				rendered := fmt.Sprintf("Instruction block %d (file):\n%s", sectionNumber, content)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			}
		default:

			if value == "" {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			blockSnapshot.Type = "text"
			blockSnapshot.ResolvedContent = value
			rendered := fmt.Sprintf("Instruction block %d (text):\n%s", sectionNumber, value)
			blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
			appendSections = append(appendSections, rendered)
		}
		resolvedBlocks = append(resolvedBlocks, blockSnapshot)
	}

	// Build combined prompt
	var combinedPrompt string
	if len(appendSections) == 0 {
		combinedPrompt = basePrompt
	} else {
		combinedPrompt = strings.TrimSpace(basePrompt) + "\n\nApply these additional instructions in order:\n\n" + strings.Join(appendSections, "\n\n")
	}

	return &systemPromptSnapshot{
		BasePrompt:        basePrompt,
		CombinedPrompt:    combinedPrompt,
		BaseEstimated:     estimateTokensApprox(basePrompt),
		CombinedEstimated: estimateTokensApprox(combinedPrompt),
		Blocks:            resolvedBlocks,
	}
}

// subAgentBuiltInToolsEstimatedTokens returns the estimated token cost of the
// built-in tool guidance section for sub-agents.
func subAgentBuiltInToolsEstimatedTokens(enabled bool) int {
	if !enabled {
		return 0
	}
	return estimateTokensApprox(agent.DefaultSystemPrompt())
}
