package jobs

import (
	"fmt"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

const (
	TaskPromptSourceText = "text"
	TaskPromptSourceFile = "file"
	maxTaskPromptBytes   = 32 * 1024
)

func NormalizeTaskPromptSource(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), TaskPromptSourceFile) {
		return TaskPromptSourceFile
	}
	return TaskPromptSourceText
}

func BuildTaskPromptForFile(path string) string {
	return fmt.Sprintf("Load and follow instructions from this file path: %s", strings.TrimSpace(path))
}

func ResolveTaskPrompt(job *storage.RecurringJob) (string, error) {
	if job == nil {
		return "", fmt.Errorf("job is required")
	}

	if NormalizeTaskPromptSource(job.TaskPromptSource) == TaskPromptSourceFile {
		path := strings.TrimSpace(job.TaskPromptFile)
		if path == "" {
			return "", fmt.Errorf("task prompt file is required when source is file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("failed to read task prompt file %q: %w", path, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return "", fmt.Errorf("task prompt file %q is empty", path)
		}
		if len(content) > maxTaskPromptBytes {
			content = content[:maxTaskPromptBytes] + "\n\n[truncated]"
		}
		return content, nil
	}

	prompt := strings.TrimSpace(job.TaskPrompt)
	if prompt == "" {
		return "", fmt.Errorf("task prompt is required")
	}
	return prompt, nil
}
