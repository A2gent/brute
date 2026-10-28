// system_prompt.go keeps high-level system-prompt snapshot composition separate from HTTP handlers.
package http

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const sessionSystemPromptSnapshotMetadataKey = storage.SystemPromptSnapshotMetadataKey

type systemPromptSnapshot struct {
	ConfiguredAgentsVersion int                         `json:"configured_agents_version,omitempty"`
	BasePrompt              string                      `json:"base_prompt"`
	CombinedPrompt          string                      `json:"combined_prompt"`
	BaseEstimated           int                         `json:"base_estimated_tokens"`
	CombinedEstimated       int                         `json:"combined_estimated_tokens"`
	Blocks                  []systemPromptBlockSnapshot `json:"blocks"`
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

func (s *Server) buildSystemPromptForSession(sess *session.Session) (string, error) {
	snapshot, err := s.ensureSystemPromptSnapshot(sess)
	if err != nil {
		return "", err
	}
	if snapshot == nil {
		return "", nil
	}
	return strings.TrimSpace(snapshot.CombinedPrompt), nil
}

func (s *Server) ensureSessionSystemPromptSnapshot(sess *session.Session) *systemPromptSnapshot {
	snapshot, err := s.ensureSystemPromptSnapshot(sess)
	if err != nil {
		logging.Warn("Failed to prepare system prompt snapshot: %v", err)
	}
	return snapshot
}

// Detail responses retain the legacy metadata and top-level snapshot payloads,
// but persisting a snapshot must never rewrite a partial/display session.
func (s *Server) ensureDisplaySystemPromptSnapshot(sess *session.Session) (*systemPromptSnapshot, error) {
	return s.ensureSystemPromptSnapshot(sess)
}

func (s *Server) ensureSystemPromptSnapshot(sess *session.Session) (*systemPromptSnapshot, error) {
	if sess == nil {
		return nil, nil
	}

	if sessionSystemPromptSnapshot(sess) == nil {
		if snapshots, ok := s.store.(storage.SystemPromptSnapshotStore); ok {
			raw, err := snapshots.GetSystemPromptSnapshot(sess.ID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				// A failed read is not evidence that a frozen prefix is missing.
				return nil, fmt.Errorf("load system prompt snapshot for session %s: %w", sess.ID, err)
			}
			if err == nil {
				var snapshot systemPromptSnapshot
				if err := json.Unmarshal(raw, &snapshot); err != nil {
					return nil, fmt.Errorf("decode system prompt snapshot for session %s: %w", sess.ID, err)
				}
				attachSessionSystemPromptSnapshot(sess, &snapshot)
			}
		}
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

	// Preserve existing snapshots, including legacy time labels: migrating them
	// mid-session would itself invalidate the provider's cached prefix.
	if snapshot := sessionSystemPromptSnapshot(sess); snapshot != nil && snapshotHasEnvironmentContext(snapshot) {
		if resolvedSubAgent != nil {
			subAgentIdentity := fmt.Sprintf(`You are a sub-agent named "%s".`, strings.TrimSpace(resolvedSubAgent.Name))
			if strings.Contains(snapshot.BasePrompt, subAgentIdentity) || strings.Contains(snapshot.CombinedPrompt, subAgentIdentity) {
				return snapshot, nil
			}
		} else {
			cachedPromptStillCurrent := !snapshotHasOutdatedConfiguredAgentsBlock(snapshot)
			if cachedPromptStillCurrent && snapshot.ConfiguredAgentsVersion == 0 && snapshotHasEnvironmentContextBlock(snapshot) {
				// Old snapshots omitted the entire catalog when nothing was running.
				// Migrate once if agents are now configured, without changing unrelated
				// legacy prefixes or rebuilding current empty catalogs every turn.
				entries, err := s.configuredDelegationAgents(sessionProjectIDForPrompt(sess))
				cachedPromptStillCurrent = err != nil || len(entries) == 0
			}
			if cachedPromptStillCurrent {
				return snapshot, nil
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
		return nil, nil
	}
	if resolvedSubAgent == nil {
		snapshot.ConfiguredAgentsVersion = 1
	}
	if snapshots, ok := s.store.(storage.SystemPromptSnapshotStore); ok {
		raw, err := json.Marshal(snapshot)
		if err == nil {
			err = snapshots.SaveSystemPromptSnapshot(sess.ID, raw)
		}
		if err != nil {
			return nil, fmt.Errorf("persist system prompt snapshot for session %s: %w", sess.ID, err)
		}
	}
	attachSessionSystemPromptSnapshot(sess, snapshot)
	return snapshot, nil
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
	// Keep the cached system prefix stable, including when its snapshot is rebuilt.
	// UTC also avoids changing bytes when the server's local timezone changes.
	if sess != nil && !sess.CreatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("- Session created at: %s\n", sess.CreatedAt.UTC().Format(time.RFC3339)))
	}
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
		// The branch is named before the task is known, so the agent may rename it; brute reads it back from git.
		if wt := s.sessionWorktree(sess); wt != nil {
			sb.WriteString(fmt.Sprintf("\n\nThis session works in its own git worktree at %s (your working directory). Once the task is clear, you may rename its branch with `git branch -m <name>` following the project's branch naming conventions. Never move or rename the worktree folder: the session and the project's dev environment are bound to it. Delivery means integrating the verified commit into the local checkout at the project root (the branch open there), not merely pushing to origin/main. The Complete action saves changes, merges into the branch open in the project root (or the parent worktree) without pushing, then removes the worktree and archives the session. If local integration is blocked by uncommitted work or conflicts, preserve the worktree and report the blocker; do not claim delivery or remove it manually.", wt.Path))
		}
	} else {
		sb.WriteString("\nNo project root is associated with this session. Use the server working directory as the default file scope.")
	}
	// Lets the user ask any agent to retune the automatic router. Points at the API, not
	// only the file: the server keeps config in memory and its next save would undo direct edits.
	if router, ok := s.config.Providers[string(config.ProviderAutoRouter)]; ok && router.RouterProvider != "" {
		api := fmt.Sprintf("http://127.0.0.1:%d/providers", s.Port())
		sb.WriteString(fmt.Sprintf("\n\nAutomatic router rules are stored in %s under providers.automatic_router.router_rules as [{match, provider, model, reasoning_effort}]; the router model picks the rule whose plain-text `match` best fits the request (the first rule is the default). `provider` may be a fallback-chain ref, which then must omit `model`. To change them, read the current list with GET %s, then PUT %s/automatic_router with {\"router_rules\": [...]} (the full list; it replaces the old one and is validated and saved). Do not edit the file directly: the running server would overwrite it.", config.GetConfigPath(), api, api))
	}
	// sql_query looks up connection_name in this list. Write it before the snapshot below:
	// content and the token estimate are taken from that snapshot, so a later append never reaches the agent.
	// DSN stays out: it can hold a password, and the agent only needs the name to connect.
	if projectID != "" && s.store != nil {
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
	content := strings.TrimSpace(sb.String())
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

	if globalPrompt := resolveGlobalSystemPrompt(settings); globalPrompt != "" {
		section := "Global instructions (apply to every agent):\n" + globalPrompt
		appendSections = append([]string{section}, appendSections...)
		resolvedBlocks = append([]systemPromptBlockSnapshot{{
			Type:            globalSystemPromptBlockType,
			Value:           "",
			Enabled:         true,
			ResolvedContent: globalPrompt,
			EstimatedTokens: estimateTokensApprox(section),
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
		strings.Contains(content, runningConfiguredAgentsPromptHeader) ||
		strings.Contains(content, legacyRunningAgentsPromptHeader)
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

// Custom/legacy prefixes can mention environment context as plain text. Only
// standard composed snapshots carry this block and need empty-catalog migration.
func snapshotHasEnvironmentContextBlock(snapshot *systemPromptSnapshot) bool {
	for _, block := range snapshot.Blocks {
		if strings.TrimSpace(block.Type) == "environment_context" {
			return true
		}
	}
	return false
}
