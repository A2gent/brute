// instruction_blocks_tool_sections.go renders tool and skill related instruction blocks.
package http

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/llm"
	skillsLoader "github.com/A2gent/brute/internal/skills"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) resolveBuiltInToolsGuidance(settings map[string]string) (string, bool) {
	definitions := s.resolveEnabledToolDefinitions(settings)
	if len(definitions) == 0 {
		return "", true
	}

	lines := make([]string, 0, len(definitions)+3)
	lines = append(lines, "Available built-in tools:")
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s", name))
	}
	if len(lines) <= 1 {
		return "", true
	}
	lines = append(lines, "", "Use `man` with a tool name to read detailed usage and input schema only when needed.")
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

func (s *Server) resolveMCPServersSection(blockNumber int, projectID string) (string, int, string) {
	servers, err := s.store.ListMCPServers()
	if err != nil {
		return "", 0, "Failed to list MCP servers: " + err.Error()
	}
	servers = filterMCPServersForProject(servers, projectID, true)

	type mcpEntry struct {
		name      string
		transport string
		scope     string
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
			scope:     mcpServerScopeLabel(server, projectID),
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
	lines = append(lines, "Enabled MCP servers available to the agent. Use `mcp_list_tools` to inspect callable MCP tools, then `mcp_call` to invoke the selected MCP tool. Manage server configuration in MCP section: /mcp")
	for _, entry := range entries {
		label := entry.name
		if label == "" {
			label = "Unnamed MCP server"
		}
		transport := entry.transport
		if transport == "" {
			transport = "unknown"
		}
		scope := entry.scope
		if scope == "" {
			scope = "global"
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, %s, %d tools, %d tokens)", label, transport, scope, entry.tools, entry.tokens))
	}
	lines = append(lines, fmt.Sprintf("Total MCP servers estimated tokens: %d", totalTokens))

	return strings.Join(lines, "\n"), totalTokens, ""
}

func mcpServerScopeLabel(server *storage.MCPServer, currentProjectID string) string {
	projectID := mcpServerProjectID(server)
	if projectID == "" {
		return "global"
	}
	if projectID == strings.TrimSpace(currentProjectID) {
		return "project"
	}
	return "project:" + projectID
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
