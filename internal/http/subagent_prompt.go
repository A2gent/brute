// subagent_prompt.go keeps sub-agent prompt composition separate from generic server handlers.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const (
	legacyConfiguredAgentsPromptHeader      = "Available Docker-backed configured agents for delegation:"
	legacySavedConfiguredAgentsPromptHeader = "Available Docker-backed configured agents for delegation (saved locally):"
	runningConfiguredAgentsPromptHeader     = "Currently running Docker-backed configured agents for delegation:"
	availableConfiguredAgentsPromptHeader   = "Running agents for delegate_to_agent:"
	subAgentPromptDockerListTimeout         = 2 * time.Second
)

type configuredAgentPromptEntry struct {
	ID      string
	Name    string
	Metrics agentdef.AgentMetrics
}

func (s *Server) resolveSubAgentsSection(sess *session.Session) (string, int) {
	entries := []configuredAgentPromptEntry{}
	seen := map[string]struct{}{}
	containersByDefinitionID := s.dockerAgentDefinitionContainersForPrompt()
	currentProjectID := sessionProjectIDForPrompt(sess)

	agents, err := s.store.ListSubAgents()
	if err != nil {
		logging.Warn("Failed to list sub-agents for system prompt: %v", err)
	} else {
		for _, sa := range agents {
			if sa == nil {
				continue
			}
			// WHY: configured_project agents resolve workspace to their own project,
			// so a running container for project B would otherwise match in project A.
			if !agentVisibleInProjectSession(subAgentProjectID(sa), currentProjectID) {
				continue
			}
			def, defErr := agentdef.FromSubAgent(sa)
			if defErr != nil {
				logging.Warn("Failed to build sub-agent definition %s for system prompt: %v", sa.ID, defErr)
				continue
			}
			entry := configuredAgentPromptEntry{
				ID:      strings.TrimSpace(sa.ID),
				Name:    strings.TrimSpace(sa.Name),
				Metrics: def.Metrics,
			}
			if entry.ID == "" {
				continue
			}
			binding, workspaceErr := s.resolveDockerWorkspaceBinding(def, currentProjectID)
			if workspaceErr != nil {
				logging.Warn("Failed to resolve Docker workspace binding for agent %s system prompt listing: %v", entry.ID, workspaceErr)
				continue
			}
			if entry.Name == "" {
				entry.Name = entry.ID
			}
			if !configuredAgentPromptHasRunningContainer([]string{entry.ID}, binding, containersByDefinitionID) {
				continue
			}
			entries = append(entries, entry)
			seen[entry.ID] = struct{}{}
		}
	}

	definitions, err := s.store.ListAgentDefinitions()
	if err != nil {
		logging.Warn("Failed to list stored agent definitions for system prompt: %v", err)
	} else {
		for _, record := range definitions {
			if record == nil {
				continue
			}
			def, parseErr := agentdef.ParseYAML([]byte(record.DefinitionYAML))
			if parseErr != nil {
				logging.Warn("Failed to parse stored agent definition %s for system prompt: %v", record.ID, parseErr)
				continue
			}
			if def.Runtime.Type != agentdef.RuntimeDocker {
				continue
			}
			agentProjectID := agentDefinitionRecordProjectID(record)
			if agentProjectID == "" {
				agentProjectID = stringFromOptional(projectIDFromDefinition(def))
			}
			if !agentVisibleInProjectSession(agentProjectID, currentProjectID) {
				continue
			}
			defAgentID := strings.TrimSpace(def.Agent.ID)
			entry := configuredAgentPromptEntry{
				ID:      strings.TrimSpace(record.ID),
				Name:    strings.TrimSpace(def.Agent.Name),
				Metrics: def.Metrics,
			}
			if entry.ID == "" {
				entry.ID = defAgentID
			}
			if entry.ID == "" {
				continue
			}
			candidateIDs := []string{entry.ID}
			if defAgentID != "" && defAgentID != entry.ID {
				candidateIDs = append(candidateIDs, defAgentID)
			}
			alreadySeen := false
			for _, candidateID := range candidateIDs {
				if _, exists := seen[candidateID]; exists {
					alreadySeen = true
					break
				}
			}
			if alreadySeen {
				continue
			}
			binding, workspaceErr := s.resolveDockerWorkspaceBinding(def, currentProjectID)
			if workspaceErr != nil {
				logging.Warn("Failed to resolve Docker workspace binding for agent %s system prompt listing: %v", entry.ID, workspaceErr)
				continue
			}
			if entry.Name == "" {
				entry.Name = strings.TrimSpace(record.Name)
			}
			if entry.Name == "" {
				entry.Name = entry.ID
			}
			if !configuredAgentPromptHasRunningContainer(candidateIDs, binding, containersByDefinitionID) {
				continue
			}
			entries = append(entries, entry)
			for _, candidateID := range candidateIDs {
				seen[candidateID] = struct{}{}
			}
		}
	}

	if len(entries) == 0 {
		return "", 0
	}

	sort.SliceStable(entries, func(i, j int) bool {
		leftName := strings.ToLower(strings.TrimSpace(entries[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(entries[j].Name))
		if leftName == rightName {
			return strings.ToLower(strings.TrimSpace(entries[i].ID)) < strings.ToLower(strings.TrimSpace(entries[j].ID))
		}
		return leftName < rightName
	})
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, availableConfiguredAgentsPromptHeader)
	for _, entry := range entries {
		line := fmt.Sprintf("- %s - %s", entry.ID, entry.Name)
		if metrics := strings.TrimSpace(entry.Metrics.CompactString()); metrics != "" {
			line += fmt.Sprintf(" (%s)", metrics)
		}
		lines = append(lines, line)
	}

	section := strings.Join(lines, "\n")
	return section, estimateTokensApprox(section)
}

func sessionProjectIDForPrompt(sess *session.Session) string {
	if sess == nil || sess.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*sess.ProjectID)
}

func (s *Server) dockerAgentDefinitionContainersForPrompt() map[string][]LocalDockerAgent {
	ctx, cancel := context.WithTimeout(context.Background(), subAgentPromptDockerListTimeout)
	defer cancel()

	containers, err := listLocalBruteContainers(ctx)
	if err != nil {
		logging.Warn("Failed to list Docker agents for system prompt: %v", err)
		return map[string][]LocalDockerAgent{}
	}
	annotateLocalDockerAgentHealth(ctx, containers)

	byDefinitionID := map[string][]LocalDockerAgent{}
	for _, container := range containers {
		defID := strings.TrimSpace(container.Labels[dockerRuntimeAgentDefLabelKey])
		if defID == "" {
			continue
		}
		byDefinitionID[defID] = append(byDefinitionID[defID], container)
	}
	return byDefinitionID
}

func configuredAgentPromptHasRunningContainer(candidateIDs []string, binding dockerWorkspaceBinding, containersByDefinitionID map[string][]LocalDockerAgent) bool {
	for _, candidateID := range candidateIDs {
		candidateID = strings.TrimSpace(candidateID)
		if candidateID == "" {
			continue
		}
		for _, container := range containersByDefinitionID[candidateID] {
			if !localDockerAgentAvailableForUse(container) {
				continue
			}
			if !configuredAgentPromptContainerMatchesBinding(container, binding) {
				continue
			}
			return true
		}
	}
	return false
}

func configuredAgentPromptContainerMatchesBinding(container LocalDockerAgent, binding dockerWorkspaceBinding) bool {
	if strings.TrimSpace(binding.ContainerNameBinding) == "" {
		return true
	}
	labels := container.Labels
	if len(labels) == 0 {
		return false
	}
	return strings.TrimSpace(labels["a2gent.project_id"]) == strings.TrimSpace(binding.ContainerNameBinding)
}

// composeSubAgentSystemPromptSnapshot builds a system prompt snapshot for a
// sub-agent using its own instruction blocks configuration. It follows the same
// block-resolution logic as the main agent but uses a sub-agent-specific base
// prompt and omits project_agents_md and the sub-agents listing (to prevent
// recursion).
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
			section, estimatedTokens, resolveErr := s.resolveMCPServersSection(sectionNumber, sessionProjectIDForPrompt(sess))
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
