// instruction_blocks.go keeps core instruction-block parsing and project-file resolution helpers together.
package http

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
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
		sourcePath := value
		if !filepath.IsAbs(sourcePath) && sess != nil && sess.ProjectID != nil && strings.TrimSpace(*sess.ProjectID) != "" {
			if project, err := s.store.GetProject(strings.TrimSpace(*sess.ProjectID)); err == nil && project != nil && project.Folder != nil {
				// WHY: project-scoped file blocks should be portable between projects.
				// WHAT: relative paths are resolved from the associated project folder.
				sourcePath = filepath.Join(strings.TrimSpace(*project.Folder), sourcePath)
			}
		}
		content, err := s.readInstructionFileBlock(sourcePath)
		if err != nil {
			section := fmt.Sprintf("Instruction block %d (project file):\nUnable to load file %s: %s", blockNumber, sourcePath, err.Error())
			return section, sourcePath, estimateTokensApprox(section), err.Error()
		}
		section := fmt.Sprintf("Instruction block %d (project file):\n%s", blockNumber, content)
		return section, sourcePath, estimateTokensApprox(section), ""
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
