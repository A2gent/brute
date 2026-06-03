// instruction_blocks.go keeps instruction-block parsing and resolution helpers together after splitting server.go.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	skillsLoader "github.com/A2gent/brute/internal/skills"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const thinkingJobIDSettingKey = "A2GENT_THINKING_JOB_ID"

const thinkingSourceSettingKey = "A2GENT_THINKING_SOURCE"

const thinkingTextSettingKey = "A2GENT_THINKING_TEXT"

const thinkingFilePathSettingKey = "A2GENT_THINKING_FILE_PATH"

const thinkingInstructionBlocksSettingKey = "A2GENT_THINKING_INSTRUCTION_BLOCKS"

const agentInstructionBlocksSettingKey = "A2GENT_AGENT_INSTRUCTION_BLOCKS"

const agentBaseSystemPromptSettingKey = "A2GENT_AGENT_BASE_SYSTEM_PROMPT"

const projectInstructionBlocksSettingKey = "A2GENT_PROJECT_INSTRUCTION_BLOCKS"

const projectBranchTaskDocDirectorySettingKey = "A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY"

const projectBranchTaskDocModeSettingKey = "A2GENT_PROJECT_BRANCH_TASK_DOC_MODE"

const legacyBranchTaskDocDirectorySettingPrefix = "A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY."

const legacyBranchTaskDocModeSettingPrefix = "A2GENT_PROJECT_BRANCH_TASK_DOC_MODE."

const builtInToolsInstructionBlockType = "builtin_tools"

const integrationSkillsInstructionBlockType = "integration_skills"

const externalMarkdownSkillsInstructionBlockType = "external_markdown_skills"

const mcpServersInstructionBlockType = "mcp_servers"

const branchTaskDocInstructionBlockType = "branch_task_doc"

const skillsFolderSettingKey = "AAGENT_SKILLS_FOLDER"

const externalMarkdownDisabledSkillsSettingKey = "A2GENT_EXTERNAL_MARKDOWN_DISABLED_SKILLS"

const defaultDynamicInstructionFile = "AGENTS.md"

const maxDynamicInstructionBytes = 32 * 1024

type configuredInstructionBlock struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"`
}

func absoluteCleanPath(path string, base string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if strings.TrimSpace(base) == "" {
			base = "."
		}
		path = filepath.Join(base, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func filterGlobalInstructionBlocks(blocks []configuredInstructionBlock) []configuredInstructionBlock {
	if len(blocks) == 0 {
		return []configuredInstructionBlock{}
	}
	filtered := make([]configuredInstructionBlock, 0, len(blocks))
	for _, block := range blocks {
		blockType := strings.TrimSpace(block.Type)

		if blockType == "project_agents_md" || blockType == branchTaskDocInstructionBlockType {
			continue
		}
		filtered = append(filtered, block)
	}
	return filtered
}

func (s *Server) resolveProjectInstructionBlocks(sess *session.Session) []configuredInstructionBlock {
	if sess == nil || sess.ProjectID == nil || strings.TrimSpace(*sess.ProjectID) == "" {
		return []configuredInstructionBlock{}
	}
	project, err := s.store.GetProject(strings.TrimSpace(*sess.ProjectID))
	if err != nil || project == nil {
		if err != nil {
			logging.Warn("Failed to load project instruction blocks: %v", err)
		}
		return []configuredInstructionBlock{}
	}
	settings := normalizeProjectSettings(project.Settings)
	blocks := []configuredInstructionBlock{}
	if rawBlocks := strings.TrimSpace(settings[projectInstructionBlocksSettingKey]); rawBlocks != "" {
		if err := json.Unmarshal([]byte(rawBlocks), &blocks); err != nil {
			logging.Warn("Failed to parse %s for project %s: %v", projectInstructionBlocksSettingKey, project.ID, err)
			blocks = []configuredInstructionBlock{}
		}
	}
	if strings.TrimSpace(settings[projectBranchTaskDocDirectorySettingKey]) != "" {
		hasBranchDocBlock := false
		for _, block := range blocks {
			if strings.TrimSpace(block.Type) == branchTaskDocInstructionBlockType {
				hasBranchDocBlock = true
				break
			}
		}
		if !hasBranchDocBlock {
			enabled := true
			blocks = append(blocks, configuredInstructionBlock{Type: branchTaskDocInstructionBlockType, Enabled: &enabled})
		}
	}
	return blocks
}

func (s *Server) resolveProjectInstructionBlockSection(sess *session.Session, blockType string, value string, blockNumber int) (string, string, int, string) {
	blockType = strings.TrimSpace(blockType)
	value = strings.TrimSpace(value)
	switch blockType {
	case "project_agents_md":
		settings, err := s.store.GetSettings()
		if err != nil {
			settings = map[string]string{}
		}
		section := s.resolveProjectAgentsMDSection(sess, settings, value, blockNumber)
		if section == "" {
			return "", value, 0, "No project instruction file content found."
		}
		return section, value, estimateTokensApprox(section), ""
	case branchTaskDocInstructionBlockType:
		return s.resolveBranchTaskDocSection(sess, value, blockNumber)
	case "text":
		if value == "" {
			return "", "", 0, "Empty project text instruction block."
		}
		section := fmt.Sprintf("Instruction block %d (project text):\n%s", blockNumber, value)
		return section, "", estimateTokensApprox(section), ""
	case "file":
		if value == "" {
			return "", "", 0, "Empty project file instruction block."
		}
		content, err := s.readInstructionFileBlock(value)
		if err != nil {
			section := fmt.Sprintf("Instruction block %d (project file):\nUnable to load file %s: %s", blockNumber, value, err.Error())
			return section, value, estimateTokensApprox(section), err.Error()
		}
		section := fmt.Sprintf("Instruction block %d (project file):\n%s", blockNumber, content)
		return section, value, estimateTokensApprox(section), ""
	default:
		if value == "" {
			return "", "", 0, "Unsupported empty project instruction block."
		}
		section := fmt.Sprintf("Instruction block %d (project text):\n%s", blockNumber, value)
		return section, "", estimateTokensApprox(section), ""
	}
}

func resolveThinkingInstructionBlocksFromSettings(settings map[string]string) []configuredInstructionBlock {
	if settings == nil {
		return []configuredInstructionBlock{}
	}

	rawBlocks := strings.TrimSpace(settings[thinkingInstructionBlocksSettingKey])
	if rawBlocks != "" {
		parsed := []configuredInstructionBlock{}
		if err := json.Unmarshal([]byte(rawBlocks), &parsed); err != nil {
			logging.Warn("Failed to parse %s: %v", thinkingInstructionBlocksSettingKey, err)
			return []configuredInstructionBlock{}
		}
		normalized := make([]configuredInstructionBlock, 0, len(parsed))
		for _, block := range parsed {
			blockType := strings.TrimSpace(block.Type)
			value := strings.TrimSpace(block.Value)
			enabled := block.Enabled == nil || *block.Enabled
			if !enabled {
				continue
			}
			if blockType == "project_agents_md" || value != "" {
				enabledCopy := true
				normalized = append(normalized, configuredInstructionBlock{
					Type:    blockType,
					Value:   value,
					Enabled: &enabledCopy,
				})
			}
		}
		return normalized
	}

	source := strings.TrimSpace(strings.ToLower(settings[thinkingSourceSettingKey]))
	textValue := strings.TrimSpace(settings[thinkingTextSettingKey])
	fileValue := strings.TrimSpace(settings[thinkingFilePathSettingKey])
	switch source {
	case "file":
		if fileValue != "" {
			enabled := true
			return []configuredInstructionBlock{{Type: "file", Value: fileValue, Enabled: &enabled}}
		}
	case "text":
		if textValue != "" {
			enabled := true
			return []configuredInstructionBlock{{Type: "text", Value: textValue, Enabled: &enabled}}
		}
	}

	if fileValue != "" {
		enabled := true
		return []configuredInstructionBlock{{Type: "file", Value: fileValue, Enabled: &enabled}}
	}
	if textValue != "" {
		enabled := true
		return []configuredInstructionBlock{{Type: "text", Value: textValue, Enabled: &enabled}}
	}
	return []configuredInstructionBlock{}
}

func isThinkingSessionWithSettings(sess *session.Session, settings map[string]string) bool {
	if sess == nil {
		return false
	}
	if sess.ProjectID != nil && strings.TrimSpace(*sess.ProjectID) == thinkingProjectID {
		return true
	}
	if sess.JobID == nil {
		return false
	}
	thinkingJobID := strings.TrimSpace(settings[thinkingJobIDSettingKey])
	if thinkingJobID == "" {
		return false
	}
	return strings.TrimSpace(*sess.JobID) == thinkingJobID
}

func (s *Server) resolveBuiltInToolsGuidance(settings map[string]string) (string, bool) {
	definitions := s.resolveEnabledToolDefinitions(settings)
	if len(definitions) == 0 {
		return "", true
	}

	lines := make([]string, 0, len(definitions)+1)
	lines = append(lines, "Available tools allow you to:")
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		description := strings.TrimSpace(definition.Description)
		if name == "" {
			continue
		}
		if description == "" {
			lines = append(lines, fmt.Sprintf("- %s", name))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", name, description))
	}
	if len(lines) <= 1 {
		return "", true
	}
	return strings.Join(lines, "\n"), true
}

func (s *Server) resolveEnabledToolDefinitions(settings map[string]string) []llm.ToolDefinition {
	definitions := s.toolManager.GetDefinitions()
	if len(definitions) == 0 {
		return []llm.ToolDefinition{}
	}

	disabledTools := resolveDisabledToolNames(settings)
	filtered := make([]llm.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		if _, disabled := disabledTools[name]; disabled {
			continue
		}
		filtered = append(filtered, definition)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(filtered[i].Name)) < strings.ToLower(strings.TrimSpace(filtered[j].Name))
	})
	return filtered
}

func builtInToolsEstimatedTokens(enabled bool, usingBuiltInDefault bool, renderedSection string) int {
	if !enabled {
		return 0
	}
	if strings.TrimSpace(renderedSection) != "" {
		return estimateTokensApprox(renderedSection)
	}
	if usingBuiltInDefault {
		return estimateTokensApprox(agent.DefaultBuiltInToolsGuidance())
	}
	return 0
}

func estimateTokensApprox(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := utf8.RuneCountInString(trimmed)
	if runes <= 0 {
		return 0
	}
	return int(math.Ceil(float64(runes) / 4.0))
}

func (s *Server) readInstructionFileBlock(path string) (string, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "", fmt.Errorf("empty file path")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("file is empty")
	}
	if len(content) > maxDynamicInstructionBytes {
		content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
	}
	return content, nil
}

func (s *Server) resolveProjectAgentsMDSection(sess *session.Session, settings map[string]string, rawFilename string, blockNumber int) string {
	folders := make([]string, 0, 4)
	if sess != nil && sess.ProjectID != nil && strings.TrimSpace(*sess.ProjectID) != "" {
		project, err := s.store.GetProject(strings.TrimSpace(*sess.ProjectID))
		if err == nil && project != nil && project.Folder != nil {
			folders = append(folders, *project.Folder)
		}
	}
	if len(folders) == 0 {
		mindRoot := strings.TrimSpace(settings[mindRootFolderSettingKey])
		if mindRoot != "" {
			folders = append(folders, mindRoot)
		}
	}
	if len(folders) == 0 {
		return ""
	}

	filename := strings.TrimSpace(rawFilename)
	if filename == "" {
		filename = defaultDynamicInstructionFile
	}

	pathsToTry := []string{filename}
	lower := strings.ToLower(filename)
	if lower != filename {
		pathsToTry = append(pathsToTry, lower)
	}

	collected := make([]string, 0, len(folders))
	for _, folder := range folders {
		base := strings.TrimSpace(folder)
		if base == "" {
			continue
		}
		for _, rel := range pathsToTry {
			candidate := rel
			if !filepath.IsAbs(rel) {
				candidate = filepath.Join(base, rel)
			}
			data, readErr := os.ReadFile(candidate)
			if readErr != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			if len(content) > maxDynamicInstructionBytes {
				content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
			}
			collected = append(collected, fmt.Sprintf("Project instruction file (%s):\n%s", candidate, content))
			break
		}
	}

	if len(collected) == 0 {
		return ""
	}

	return fmt.Sprintf("Instruction block %d (dynamic project file):\n%s", blockNumber, strings.Join(collected, "\n\n"))
}

type branchTaskDocConfig struct {
	Directory string `json:"directory"`
	Mode      string `json:"mode"`
}

func parseBranchTaskDocConfig(raw string) branchTaskDocConfig {
	trimmed := strings.TrimSpace(raw)
	config := branchTaskDocConfig{Mode: "content"}
	if trimmed == "" {
		return config
	}
	if err := json.Unmarshal([]byte(trimmed), &config); err != nil {
		config.Directory = trimmed
		config.Mode = "content"
		return config
	}
	config.Directory = strings.TrimSpace(config.Directory)
	if strings.TrimSpace(config.Mode) != "path" {
		config.Mode = "content"
	}
	return config
}

func (s *Server) resolveBranchTaskDocSection(sess *session.Session, _ string, blockNumber int) (string, string, int, string) {
	if sess == nil || sess.ProjectID == nil || strings.TrimSpace(*sess.ProjectID) == "" {
		return "", "", 0, "No project is associated with this session."
	}

	project, err := s.store.GetProject(strings.TrimSpace(*sess.ProjectID))
	if err != nil || project == nil || project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		if err != nil {
			return "", "", 0, "Failed to load session project: " + err.Error()
		}
		return "", "", 0, "Session project has no folder."
	}

	projectRoot := absoluteCleanPath(strings.TrimSpace(*project.Folder), strings.TrimSpace(s.config.WorkDir))
	branch, err := currentGitBranch(projectRoot)
	if err != nil {
		return "", "", 0, "Failed to resolve current git branch: " + err.Error()
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		return "", "", 0, "Failed to load settings: " + err.Error()
	}
	projectID := strings.TrimSpace(*sess.ProjectID)
	config := branchTaskDocConfig{
		Directory: strings.TrimSpace(settings["A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY."+projectID]),
		Mode:      strings.TrimSpace(settings["A2GENT_PROJECT_BRANCH_TASK_DOC_MODE."+projectID]),
	}
	if config.Mode != "path" {
		config.Mode = "content"
	}
	if config.Directory == "" {
		return "", "", 0, "Branch task documentation directory is not configured for this project."
	}
	if branch == "" || branch == "master" || branch == "main" {
		return "", "", 0, "Branch task documentation is skipped on main/master or detached HEAD."
	}

	baseDir := absoluteCleanPath(config.Directory, strings.TrimSpace(s.config.WorkDir))
	relPath, err := branchTaskDocRelativePath(branch)
	if err != nil {
		return "", "", 0, "Invalid branch name for documentation path: " + err.Error()
	}
	expectedPath := filepath.Join(baseDir, relPath)

	var rendered string
	if data, readErr := os.ReadFile(expectedPath); readErr != nil {
		rendered = fmt.Sprintf("Instruction block %d (branch task documentation):\nCurrent git branch: %s\nExpected task documentation path: %s\nDocumentation for the current branch task is expected at the path above, but the file does not exist yet.", blockNumber, branch, expectedPath)
	} else if config.Mode == "path" {
		rendered = fmt.Sprintf("Instruction block %d (branch task documentation reference):\nCurrent git branch: %s\nTask documentation file path: %s\nLoad and use this file as task/session documentation reference when needed.", blockNumber, branch, expectedPath)
	} else {
		content := strings.TrimSpace(string(data))
		if content == "" {
			content = "[file is empty]"
		}
		if len(content) > maxDynamicInstructionBytes {
			content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
		}
		rendered = fmt.Sprintf("Instruction block %d (branch task documentation):\nCurrent git branch: %s\nTask documentation file: %s\n\n%s", blockNumber, branch, expectedPath, content)
	}

	return rendered, expectedPath, estimateTokensApprox(rendered), ""
}

func currentGitBranch(projectRoot string) (string, error) {
	out, err := runGitCommand(projectRoot, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func branchTaskDocRelativePath(branch string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(branch), "/\\")
	if trimmed == "" || strings.Contains(trimmed, "\x00") {
		return "", errors.New("empty branch name")
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return "", errors.New("empty branch name")
	}

	fileStem := strings.TrimSpace(parts[len(parts)-1])
	if fileStem == "" || fileStem == "." || fileStem == ".." || strings.ContainsAny(fileStem, `/\\`) {
		return "", fmt.Errorf("unsafe branch filename %q", fileStem)
	}
	return fileStem + ".md", nil
}

func (s *Server) resolveIntegrationSkillsSection(blockNumber int) (string, string) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return "", "Failed to list integrations: " + err.Error()
	}

	type skillEntry struct {
		name     string
		provider string
		mode     string
	}
	entries := make([]skillEntry, 0, len(integrations))
	for _, integration := range integrations {
		if integration == nil || !integration.Enabled {
			continue
		}
		entries = append(entries, skillEntry{
			name:     strings.TrimSpace(integration.Name),
			provider: strings.TrimSpace(integration.Provider),
			mode:     strings.TrimSpace(integration.Mode),
		})
	}

	if len(entries) == 0 {
		return "", "No enabled integrations are configured."
	}

	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].provider + "|" + entries[i].name + "|" + entries[i].mode)
		right := strings.ToLower(entries[j].provider + "|" + entries[j].name + "|" + entries[j].mode)
		return left < right
	})

	lines := make([]string, 0, len(entries)+2)
	lines = append(lines, fmt.Sprintf("Instruction block %d (integration-backed skills):", blockNumber))
	lines = append(lines, "Enabled integrations available to the agent (integration mode controls channel behavior, not tool availability):")
	for _, entry := range entries {
		label := entry.name
		if label == "" {
			label = entry.provider
		}
		mode := entry.mode
		if mode == "" {
			mode = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s/%s)", label, entry.provider, mode))
	}

	return strings.Join(lines, "\n"), ""
}

func (s *Server) resolveMCPServersSection(blockNumber int) (string, int, string) {
	servers, err := s.store.ListMCPServers()
	if err != nil {
		return "", 0, "Failed to list MCP servers: " + err.Error()
	}

	type mcpEntry struct {
		name      string
		transport string
		tools     int
		tokens    int
	}
	entries := make([]mcpEntry, 0, len(servers))
	totalTokens := 0
	for _, server := range servers {
		if server == nil || !server.Enabled {
			continue
		}

		tokenEstimate := 0
		if server.LastEstimatedTokens != nil && *server.LastEstimatedTokens > 0 {
			tokenEstimate = *server.LastEstimatedTokens
		}
		toolCount := 0
		if server.LastToolCount != nil && *server.LastToolCount > 0 {
			toolCount = *server.LastToolCount
		}
		totalTokens += tokenEstimate
		entries = append(entries, mcpEntry{
			name:      strings.TrimSpace(server.Name),
			transport: strings.TrimSpace(server.Transport),
			tools:     toolCount,
			tokens:    tokenEstimate,
		})
	}

	if len(entries) == 0 {
		return "", 0, "No enabled MCP servers are configured."
	}

	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].name + "|" + entries[i].transport)
		right := strings.ToLower(entries[j].name + "|" + entries[j].transport)
		return left < right
	})

	lines := make([]string, 0, len(entries)+4)
	lines = append(lines, fmt.Sprintf("Instruction block %d (MCP servers):", blockNumber))
	lines = append(lines, "Enabled MCP servers available to the agent. Manage these in MCP section: /mcp")
	for _, entry := range entries {
		label := entry.name
		if label == "" {
			label = "Unnamed MCP server"
		}
		transport := entry.transport
		if transport == "" {
			transport = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, %d tools, %d tokens)", label, transport, entry.tools, entry.tokens))
	}
	lines = append(lines, fmt.Sprintf("Total MCP servers estimated tokens: %d", totalTokens))

	return strings.Join(lines, "\n"), totalTokens, ""
}

func (s *Server) resolveExternalMarkdownSkillsSection(settings map[string]string, blockNumber int) (string, int, string) {
	folder := s.getSkillsFolder(settings)
	if folder == "" {
		return "", 0, "Skills folder is not configured."
	}

	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		return "", 0, "Invalid skills folder path."
	}

	info, err := os.Stat(resolvedFolder)
	if err != nil {
		return "", 0, "Skills folder is not accessible: " + err.Error()
	}
	if !info.IsDir() {
		return "", 0, "Skills folder path is not a directory."
	}

	config, configErr := skillsLoader.LoadConfig(resolvedFolder)
	if configErr != nil {

		config = skillsLoader.DefaultConfig()
	}

	allSkills, loadErr := skillsLoader.LoadSkillsFromDirectory(resolvedFolder, config)
	if loadErr != nil {
		return "", 0, "Failed to load skills: " + loadErr.Error()
	}

	if len(allSkills) == 0 {
		return "", 0, "No markdown skills discovered in configured skills folder."
	}

	disabledSkills := resolveDisabledExternalMarkdownSkillPaths(settings, resolvedFolder)

	alwaysSkills := make([]*skillsLoader.Skill, 0)
	onDemandSkills := make([]*skillsLoader.Skill, 0)

	for _, skill := range allSkills {
		if _, isDisabled := disabledSkills[filepath.Clean(strings.TrimSpace(skill.Path))]; isDisabled {
			continue
		}
		if skill.Strategy == skillsLoader.StrategyDisabled {
			continue
		}
		if skill.Strategy == skillsLoader.StrategyAlways {
			alwaysSkills = append(alwaysSkills, skill)
		} else {
			onDemandSkills = append(onDemandSkills, skill)
		}
	}

	sort.Slice(alwaysSkills, func(i, j int) bool {
		return alwaysSkills[i].Priority < alwaysSkills[j].Priority
	})
	sort.Slice(onDemandSkills, func(i, j int) bool {
		return strings.ToLower(onDemandSkills[i].Name) < strings.ToLower(onDemandSkills[j].Name)
	})

	// Build prompt sections
	var builder strings.Builder
	totalEstimatedTokens := 0

	builder.WriteString(fmt.Sprintf("Instruction block %d (external markdown skills):\n", blockNumber))
	builder.WriteString(fmt.Sprintf("Connected skills folder: %s\n\n", resolvedFolder))

	if len(alwaysSkills) > 0 {
		builder.WriteString("Loaded skills (always available):\n\n")
		for _, skill := range alwaysSkills {
			content := skill.Body
			if len(content) > maxDynamicInstructionBytes {
				content = content[:maxDynamicInstructionBytes] + "\n\n[truncated]"
			}

			section := fmt.Sprintf("Instructions from: %s\n%s\n\n", skill.RelativePath, content)
			tokens := estimateTokensApprox(section)
			totalEstimatedTokens += tokens
			builder.WriteString(section)
		}
	}

	if len(onDemandSkills) > 0 {
		builder.WriteString("Available skills (use Read tool to load):\n\n")
		for _, skill := range onDemandSkills {
			var line string
			if skill.Description != "" {
				line = fmt.Sprintf("- %s: %s [%s]\n", skill.Name, skill.Description, skill.RelativePath)
			} else {
				line = fmt.Sprintf("- %s [%s]\n", skill.Name, skill.RelativePath)
			}
			tokens := estimateTokensApprox(line)
			totalEstimatedTokens += tokens
			builder.WriteString(line)
		}
	}

	if config.MaxAutoLoadTokens > 0 && totalEstimatedTokens > config.MaxAutoLoadTokens {
		warningMsg := fmt.Sprintf(
			"\n⚠️  Warning: Auto-loaded skills exceed token budget (%d > %d)\n",
			totalEstimatedTokens, config.MaxAutoLoadTokens,
		)
		builder.WriteString(warningMsg)
		totalEstimatedTokens += estimateTokensApprox(warningMsg)
	}

	return builder.String(), totalEstimatedTokens, ""
}

func resolveDisabledExternalMarkdownSkillPaths(settings map[string]string, skillsFolder string) map[string]struct{} {
	disabled := make(map[string]struct{})
	if settings == nil {
		return disabled
	}

	raw := strings.TrimSpace(settings[externalMarkdownDisabledSkillsSettingKey])
	if raw == "" {
		return disabled
	}

	entries := make([]string, 0)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		entries = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n'
		})
	}

	for _, entry := range entries {
		candidate := strings.TrimSpace(entry)
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(skillsFolder, candidate)
		}
		resolved, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		disabled[filepath.Clean(resolved)] = struct{}{}
	}
	return disabled
}
