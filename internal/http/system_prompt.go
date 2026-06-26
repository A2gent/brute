// system_prompt.go keeps high-level system-prompt snapshot composition separate from HTTP handlers.
package http

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"os"
	"runtime"
	"strings"
	"time"
)

const sessionSystemPromptSnapshotMetadataKey = "system_prompt_snapshot"

type systemPromptSnapshot struct {
	BasePrompt        string                      `json:"base_prompt"`
	CombinedPrompt    string                      `json:"combined_prompt"`
	BaseEstimated     int                         `json:"base_estimated_tokens"`
	CombinedEstimated int                         `json:"combined_estimated_tokens"`
	Blocks            []systemPromptBlockSnapshot `json:"blocks"`
}

type systemPromptBlockSnapshot struct {
	Type            string `json:"type"`
	Value           string `json:"value"`
	Enabled         bool   `json:"enabled"`
	ResolvedContent string `json:"resolved_content,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
	Error           string `json:"error,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

func (s *Server) buildSystemPromptForSession(sess *session.Session) string {
	snapshot := s.ensureSessionSystemPromptSnapshot(sess)
	if snapshot == nil {
		return ""
	}
	return strings.TrimSpace(snapshot.CombinedPrompt)
}

func (s *Server) ensureSessionSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	if sess == nil {
		return nil
	}

	var resolvedSubAgent *storage.SubAgent
	if sess.Metadata != nil {
		if rawSubAgentID, ok := sess.Metadata["sub_agent_id"].(string); ok {
			subAgentID := strings.TrimSpace(rawSubAgentID)
			if subAgentID != "" {
				sa, saErr := s.store.GetSubAgent(subAgentID)
				if saErr != nil {
					logging.Warn("Failed to load sub-agent %s for session %s system prompt composition: %v", subAgentID, sess.ID, saErr)
				} else {
					resolvedSubAgent = sa
				}
			}
		}
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for system prompt composition: %v", err)
		settings = map[string]string{}
	}
	thinkingBlocks := resolveThinkingInstructionBlocksFromSettings(settings)

	if snapshot := sessionSystemPromptSnapshot(sess); snapshot != nil && snapshotHasEnvironmentContext(snapshot) {
		if resolvedSubAgent != nil {
			subAgentIdentity := fmt.Sprintf(`You are a sub-agent named "%s".`, strings.TrimSpace(resolvedSubAgent.Name))
			if strings.Contains(snapshot.BasePrompt, subAgentIdentity) || strings.Contains(snapshot.CombinedPrompt, subAgentIdentity) {
				return snapshot
			}
		} else {
			cachedPromptStillCurrent := !snapshotHasOutdatedConfiguredAgentsBlock(snapshot) &&
				(!isThinkingSessionWithSettings(sess, settings) || len(thinkingBlocks) == 0 || snapshotHasThinkingBlocks(snapshot))
			if cachedPromptStillCurrent {
				return snapshot
			}
		}
	}

	var snapshot *systemPromptSnapshot
	if resolvedSubAgent != nil {
		snapshot = s.composeSubAgentSystemPromptSnapshot(resolvedSubAgent, sess)
	} else {
		snapshot = s.composeSystemPromptSnapshotWithSettings(sess, settings)
	}
	if snapshot == nil {
		return nil
	}
	attachSessionSystemPromptSnapshot(sess, snapshot)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist system prompt snapshot for session %s: %v", sess.ID, err)
	}
	return snapshot
}

func sessionSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	raw, ok := sess.Metadata[sessionSystemPromptSnapshotMetadataKey]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot systemPromptSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil
	}
	snapshot.CombinedPrompt = strings.TrimSpace(snapshot.CombinedPrompt)
	snapshot.BasePrompt = strings.TrimSpace(snapshot.BasePrompt)
	if snapshot.CombinedPrompt == "" {
		return nil
	}
	return &snapshot
}

func attachSessionSystemPromptSnapshot(sess *session.Session, snapshot *systemPromptSnapshot) {
	if sess == nil || snapshot == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	sess.Metadata[sessionSystemPromptSnapshotMetadataKey] = snapshot
}

func (s *Server) composeSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for system prompt composition: %v", err)
		settings = map[string]string{}
	}
	return s.composeSystemPromptSnapshotWithSettings(sess, settings)
}

func (s *Server) resolveEnvironmentContextSection(sess *session.Session) (string, int) {
	now := time.Now()
	workDir := absoluteCleanPath(strings.TrimSpace(s.config.WorkDir), ".")
	projectName := ""
	projectID := ""
	projectRoot := ""
	if sess != nil && sess.ProjectID != nil {
		projectID = strings.TrimSpace(*sess.ProjectID)
		if projectID != "" {
			if project, err := s.store.GetProject(projectID); err == nil && project != nil {
				projectName = strings.TrimSpace(project.Name)
				if project.Folder != nil {
					projectRoot = absoluteCleanPath(strings.TrimSpace(*project.Folder), workDir)
				}
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("Environment context:\n")
	sb.WriteString(fmt.Sprintf("- Operating system: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	sb.WriteString(fmt.Sprintf("- Current time: %s (%s)\n", now.Format(time.RFC3339), now.Location().String()))
	if workDir != "" {
		sb.WriteString(fmt.Sprintf("- Server working directory: %s\n", workDir))
	}
	if projectID != "" {
		sb.WriteString(fmt.Sprintf("- Project ID: %s\n", projectID))
	}
	if projectName != "" {
		sb.WriteString(fmt.Sprintf("- Project name: %s\n", projectName))
	}
	if projectRoot != "" {
		sb.WriteString(fmt.Sprintf("- Project root: %s\n", projectRoot))
		sb.WriteString("\nWhen working on files for this session, operate within the project root above unless the user explicitly asks for a different location. The project root may contain multiple components; inspect it before concluding a requested app or package is unavailable.")
	} else {
		sb.WriteString("\nNo project root is associated with this session. Use the server working directory as the default file scope.")
	}
	content := strings.TrimSpace(sb.String())
	if projectID != "" {
		dbs, err := s.store.ListProjectDatabases(projectID)
		if err == nil && len(dbs) > 0 {
			sb.WriteString("\nConfigured Project Databases:\n")
			for _, db := range dbs {
				ro := ""
				if db.IsReadOnly {
					ro = " (Read-only)"
				}
				sb.WriteString(fmt.Sprintf("- Name: %s | Environment: %s | Engine: %s%s\n", db.Name, db.Environment, db.Engine, ro))
			}
		}
	}
	return content, estimateTokensApprox(content)
}

func (s *Server) composeSystemPromptSnapshotWithSettings(sess *session.Session, settings map[string]string) *systemPromptSnapshot {
	rawBlocks := strings.TrimSpace(settings[agentInstructionBlocksSettingKey])
	blocks := []configuredInstructionBlock{}
	if rawBlocks != "" {
		if err := json.Unmarshal([]byte(rawBlocks), &blocks); err != nil {
			logging.Warn("Failed to parse %s: %v", agentInstructionBlocksSettingKey, err)
			blocks = []configuredInstructionBlock{}
		}
	}

	blocks = filterGlobalInstructionBlocks(blocks)
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
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: builtInToolsInstructionBlockType, Value: ""})
	}
	if !hasIntegrationBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: integrationSkillsInstructionBlockType, Value: ""})
	}
	if !hasExternalMarkdownBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: externalMarkdownSkillsInstructionBlockType, Value: ""})
	}
	if !hasMCPServersBlock {
		prefixedBlocks = append(prefixedBlocks, configuredInstructionBlock{Type: mcpServersInstructionBlockType, Value: ""})
	}
	blocks = append(prefixedBlocks, blocks...)

	builtInToolsEnabled := true
	for _, block := range blocks {
		if strings.TrimSpace(block.Type) != builtInToolsInstructionBlockType {
			continue
		}
		builtInToolsEnabled = block.Enabled == nil || *block.Enabled
		break
	}

	basePrompt := resolveMainAgentBasePrompt(settings)
	builtInGuidance, usingBuiltInDefaultGuidance := s.resolveBuiltInToolsGuidance(settings)
	if basePrompt == "" {
		return nil
	}

	resolvedBlocks := make([]systemPromptBlockSnapshot, 0, len(blocks))
	builtInRendered := ""
	if builtInToolsEnabled && strings.TrimSpace(builtInGuidance) != "" {
		builtInRendered = fmt.Sprintf("Built-in tools guidance:\n%s", strings.TrimSpace(builtInGuidance))
	}
	resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
		Type:            builtInToolsInstructionBlockType,
		Value:           "",
		Enabled:         builtInToolsEnabled,
		ResolvedContent: strings.TrimSpace(builtInGuidance),
		EstimatedTokens: builtInToolsEstimatedTokens(builtInToolsEnabled, usingBuiltInDefaultGuidance, builtInRendered),
	})
	appendSections := make([]string, 0, len(blocks))
	if builtInRendered != "" {
		appendSections = append(appendSections, builtInRendered)
	}
	sectionNumber := 0
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

	for _, block := range s.resolveProjectInstructionBlocks(sess) {
		sectionNumber++
		blockSnapshot := systemPromptBlockSnapshot{
			Type:    strings.TrimSpace(block.Type),
			Value:   strings.TrimSpace(block.Value),
			Enabled: block.Enabled == nil || *block.Enabled,
		}
		if !blockSnapshot.Enabled {
			resolvedBlocks = append(resolvedBlocks, blockSnapshot)
			continue
		}
		section, sourcePath, estimatedTokens, resolveErr := s.resolveProjectInstructionBlockSection(sess, blockSnapshot.Type, blockSnapshot.Value, sectionNumber)
		blockSnapshot.SourcePath = sourcePath
		blockSnapshot.ResolvedContent = section
		blockSnapshot.Error = resolveErr
		blockSnapshot.EstimatedTokens = estimatedTokens
		if section != "" {
			appendSections = append(appendSections, section)
		}
		resolvedBlocks = append(resolvedBlocks, blockSnapshot)
	}

	if isThinkingSessionWithSettings(sess, settings) {
		thinkingBlocks := resolveThinkingInstructionBlocksFromSettings(settings)
		for _, block := range thinkingBlocks {
			blockType := strings.TrimSpace(block.Type)
			enabled := block.Enabled == nil || *block.Enabled
			blockSnapshot := systemPromptBlockSnapshot{
				Type:    "thinking_" + blockType,
				Value:   strings.TrimSpace(block.Value),
				Enabled: enabled,
			}
			sectionNumber++
			if !enabled {
				resolvedBlocks = append(resolvedBlocks, blockSnapshot)
				continue
			}
			value := blockSnapshot.Value
			switch blockType {
			case "text":
				blockSnapshot.ResolvedContent = value
				rendered := fmt.Sprintf("Thinking instruction block %d (text):\n%s", sectionNumber, value)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			case "file":
				blockSnapshot.SourcePath = value
				content, readErr := s.readInstructionFileBlock(value)
				if readErr != nil {
					blockSnapshot.Error = readErr.Error()
					rendered := fmt.Sprintf("Thinking instruction block %d (file):\nUnable to load file %s: %s", sectionNumber, value, readErr.Error())
					blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
					appendSections = append(appendSections, rendered)
				} else {
					blockSnapshot.ResolvedContent = content
					rendered := fmt.Sprintf("Thinking instruction block %d (file):\n%s", sectionNumber, content)
					blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
					appendSections = append(appendSections, rendered)
				}
			case "project_agents_md":
				section := s.resolveProjectAgentsMDSection(sess, settings, value, sectionNumber)
				blockSnapshot.ResolvedContent = section
				if section == "" {
					blockSnapshot.Error = "No project/My Mind instruction file content found."
					resolvedBlocks = append(resolvedBlocks, blockSnapshot)
					continue
				}
				blockSnapshot.EstimatedTokens = estimateTokensApprox(section)
				appendSections = append(appendSections, section)
			default:
				if value == "" {
					resolvedBlocks = append(resolvedBlocks, blockSnapshot)
					continue
				}
				blockSnapshot.Type = "thinking_text"
				blockSnapshot.ResolvedContent = value
				rendered := fmt.Sprintf("Thinking instruction block %d (text):\n%s", sectionNumber, value)
				blockSnapshot.EstimatedTokens = estimateTokensApprox(rendered)
				appendSections = append(appendSections, rendered)
			}
			resolvedBlocks = append(resolvedBlocks, blockSnapshot)
		}
	}

	environmentContext, environmentContextTokens := s.resolveEnvironmentContextSection(sess)
	if environmentContext != "" {
		appendSections = append([]string{environmentContext}, appendSections...)
		resolvedBlocks = append([]systemPromptBlockSnapshot{{
			Type:            "environment_context",
			Value:           "",
			Enabled:         true,
			ResolvedContent: environmentContext,
			EstimatedTokens: environmentContextTokens,
		}}, resolvedBlocks...)
	}

	subAgentsSection, subAgentsTokens := s.resolveSubAgentsSection(sess)
	if subAgentsSection != "" {
		appendSections = append(appendSections, subAgentsSection)
		resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
			Type:            "sub_agents",
			Value:           "",
			Enabled:         true,
			ResolvedContent: subAgentsSection,
			EstimatedTokens: subAgentsTokens,
		})
	}
	externalAgentsSection, externalAgentsTokens := s.resolveExternalAgentsSection(settings)
	if externalAgentsSection != "" {
		appendSections = append(appendSections, externalAgentsSection)
		resolvedBlocks = append(resolvedBlocks, systemPromptBlockSnapshot{
			Type:            "external_agents",
			Value:           "",
			Enabled:         true,
			ResolvedContent: externalAgentsSection,
			EstimatedTokens: externalAgentsTokens,
		})
	}

	if len(appendSections) == 0 {
		if appendPrompt := strings.TrimSpace(os.Getenv("AAGENT_SYSTEM_PROMPT_APPEND")); appendPrompt != "" {
			combinedPrompt := strings.TrimSpace(basePrompt) + "\n\n" + appendPrompt
			return &systemPromptSnapshot{
				BasePrompt:        basePrompt,
				CombinedPrompt:    combinedPrompt,
				BaseEstimated:     estimateTokensApprox(basePrompt),
				CombinedEstimated: estimateTokensApprox(combinedPrompt),
				Blocks:            resolvedBlocks,
			}
		}
		return &systemPromptSnapshot{
			BasePrompt:        basePrompt,
			CombinedPrompt:    basePrompt,
			BaseEstimated:     estimateTokensApprox(basePrompt),
			CombinedEstimated: estimateTokensApprox(basePrompt),
			Blocks:            resolvedBlocks,
		}
	}
	combinedPrompt := strings.TrimSpace(basePrompt) + "\n\nApply these additional instructions in order:\n\n" + strings.Join(appendSections, "\n\n")
	return &systemPromptSnapshot{
		BasePrompt:        basePrompt,
		CombinedPrompt:    combinedPrompt,
		BaseEstimated:     estimateTokensApprox(basePrompt),
		CombinedEstimated: estimateTokensApprox(combinedPrompt),
		Blocks:            resolvedBlocks,
	}
}

func snapshotHasThinkingBlocks(snapshot *systemPromptSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, block := range snapshot.Blocks {
		if strings.HasPrefix(strings.TrimSpace(block.Type), "thinking_") {
			return true
		}
	}
	return false
}

func snapshotHasOutdatedConfiguredAgentsBlock(snapshot *systemPromptSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, block := range snapshot.Blocks {
		if strings.TrimSpace(block.Type) == "sub_agents" && configuredAgentsPromptBlockNeedsRefresh(block.ResolvedContent) {
			return true
		}
	}
	return configuredAgentsPromptBlockNeedsRefresh(snapshot.CombinedPrompt)
}

func configuredAgentsPromptBlockNeedsRefresh(content string) bool {
	return strings.Contains(content, legacyConfiguredAgentsPromptHeader) ||
		strings.Contains(content, legacySavedConfiguredAgentsPromptHeader) ||
		strings.Contains(content, runningConfiguredAgentsPromptHeader)
}

func snapshotHasEnvironmentContext(snapshot *systemPromptSnapshot) bool {
	if snapshot == nil {
		return false
	}
	for _, block := range snapshot.Blocks {
		if strings.TrimSpace(block.Type) == "environment_context" {
			return true
		}
	}
	return strings.Contains(snapshot.CombinedPrompt, "Environment context:")
}

func resolveMainAgentBasePrompt(settings map[string]string) string {
	if settings != nil {
		if configured := strings.TrimSpace(settings[agentBaseSystemPromptSettingKey]); configured != "" {
			return configured
		}
	}
	if configured := strings.TrimSpace(os.Getenv("AAGENT_SYSTEM_PROMPT")); configured != "" {
		return configured
	}
	if configured := strings.TrimSpace(os.Getenv(agentBaseSystemPromptSettingKey)); configured != "" {
		return configured
	}
	return agent.DefaultSystemPromptWithoutBuiltInTools()
}
