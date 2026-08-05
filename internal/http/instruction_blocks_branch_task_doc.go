// instruction_blocks_branch_task_doc.go resolves branch-scoped task documentation instruction blocks.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

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

func (s *Server) loadBranchTaskDocConfig(projectID string, projectSettings map[string]string) (branchTaskDocConfig, error) {
	projectDirectory, hasProjectDirectorySetting := projectSettings[projectBranchTaskDocDirectorySettingKey]
	config := branchTaskDocConfig{
		Directory: strings.TrimSpace(projectDirectory),
		Mode:      strings.TrimSpace(projectSettings[projectBranchTaskDocModeSettingKey]),
	}
	if !hasProjectDirectorySetting {
		// WHY: older Caesar versions stored branch-doc settings in global app settings
		// with a project-id suffix. Project settings are authoritative now, but keep a
		// fallback so existing installations continue to resolve their documentation.
		settings, err := s.store.GetSettings()
		if err != nil {
			return branchTaskDocConfig{}, err
		}
		config.Directory = strings.TrimSpace(settings[legacyBranchTaskDocDirectorySettingPrefix+projectID])
		config.Mode = strings.TrimSpace(settings[legacyBranchTaskDocModeSettingPrefix+projectID])
	}
	if config.Mode != "path" {
		config.Mode = "content"
	}
	return config, nil
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

	projectSettings := normalizeProjectSettings(project.Settings)
	projectID := strings.TrimSpace(*sess.ProjectID)
	config, err := s.loadBranchTaskDocConfig(projectID, projectSettings)
	if err != nil {
		return "", "", 0, "Failed to load legacy branch documentation settings: " + err.Error()
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
