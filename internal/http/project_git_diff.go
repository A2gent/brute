package http

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func buildGitFileDiffPreview(repoRoot string, relPath string) (string, error) {
	statusOutput, _, statusErr := runGitCommandWithExitCode(repoRoot, "status", "--porcelain=v1", "--", relPath)
	if statusErr == nil {
		for _, line := range strings.Split(statusOutput, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if len(line) >= 3 && strings.TrimSpace(line[0:2]) == "??" {
				return buildNewFileDiffPreview(repoRoot, relPath), nil
			}
		}
	}

	preview, _, err := runGitCommandWithExitCode(repoRoot, "diff", "--", relPath)
	if err != nil {
		return "", errors.New(strings.TrimSpace(preview))
	}
	if strings.TrimSpace(preview) != "" {
		return preview, nil
	}

	preview, _, err = runGitCommandWithExitCode(repoRoot, "diff", "--cached", "--", relPath)
	if err != nil {
		return "", errors.New(strings.TrimSpace(preview))
	}
	if strings.TrimSpace(preview) != "" {
		return preview, nil
	}

	return buildNewFileDiffPreview(repoRoot, relPath), nil
}

func buildNewFileDiffPreview(repoRoot string, relPath string) string {
	fullPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return ""
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	maxLines := 140
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, line := range lines {
		lines[i] = "+" + line
	}
	return strings.Join(lines, "\n")
}
