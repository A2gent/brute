package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const gitCommitProviderSettingKey = "AAGENT_GIT_COMMIT_PROVIDER"

const gitCommitPromptTemplateSettingKey = "AAGENT_GIT_COMMIT_PROMPT_TEMPLATE"

const defaultGitCommitPromptTemplate = "Generate a descriptive Git commit message based on provided files and diffs.\nReturn plain text only (no markdown, no code fences).\nFormat:\n1) First line: imperative summary (max 72 chars).\n2) Blank line.\n3) 2-4 bullet points with specific technical changes.\n\nChanged files:\n{{files}}\n\nDiff snippets:\n{{diffs}}"

func (s *Server) handleProjectGitCommitMessageSuggestion(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitCommitMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}
	files := parseGitPorcelain(porcelainOutput)
	if len(files) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	targetFiles := make([]ProjectGitChangedFile, 0, len(files))
	for _, file := range files {
		if file.Staged {
			targetFiles = append(targetFiles, file)
		}
	}
	if len(targetFiles) == 0 {
		targetFiles = files
	}
	fallbackMessage := buildFallbackCommitMessage(targetFiles)

	diffSections := make([]string, 0, len(targetFiles))
	for _, file := range targetFiles {
		preview, previewErr := buildGitFileDiffPreview(targetRepoRoot, file.Path)
		if previewErr != nil {
			continue
		}
		diffSections = append(diffSections, fmt.Sprintf("File: %s\n%s", file.Path, truncateText(preview, 1600)))
	}
	if len(diffSections) == 0 {
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	fileList := make([]string, 0, len(targetFiles))
	for _, file := range targetFiles {
		fileList = append(fileList, fmt.Sprintf("- %s (%s)", file.Path, file.Status))
	}

	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for git commit generation: %v", settingsErr)
		settings = map[string]string{}
	}
	template := strings.TrimSpace(settings[gitCommitPromptTemplateSettingKey])
	if template == "" {
		template = defaultGitCommitPromptTemplate
	}
	prompt := buildGitCommitPrompt(template, strings.Join(fileList, "\n"), strings.Join(diffSections, "\n\n"))

	providerRef := strings.TrimSpace(settings[gitCommitProviderSettingKey])
	if providerRef == "" {
		providerRef = s.config.ActiveProvider
	}
	configuredProviderType := config.ProviderType(config.NormalizeProviderRef(providerRef))
	activeProviderType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	response, err := s.generateGitCommitMessageWithProvider(ctx, configuredProviderType, prompt)
	if err != nil && configuredProviderType != activeProviderType {
		logging.Warn("Commit message generation failed with configured provider %s: %v. Retrying active provider %s", configuredProviderType, err, activeProviderType)
		response, err = s.generateGitCommitMessageWithProvider(ctx, activeProviderType, prompt)
	}
	if err != nil {
		logging.Warn("Commit message generation failed: %v", err)
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	message := sanitizeGeneratedCommitMessage(response.Content)
	if message == "" {
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: message})
}

func (s *Server) generateGitCommitMessageWithProvider(ctx context.Context, providerType config.ProviderType, prompt string) (*llm.ChatResponse, error) {
	model := s.resolveModelForProvider(providerType)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, prompt, nil)
	if err != nil {
		return nil, err
	}
	return target.Client.Chat(ctx, &llm.ChatRequest{
		Model: target.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   220,
	})
}

func sanitizeGeneratedCommitMessage(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	trimmed = strings.Trim(trimmed, "\"'`")
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	lines := strings.Split(trimmed, "\n")
	filteredLines := make([]string, 0, len(lines))
	for _, line := range lines {
		candidate := strings.TrimSpace(strings.Trim(line, "\"'`"))
		if candidate == "" {
			if len(filteredLines) > 0 && filteredLines[len(filteredLines)-1] == "" {
				continue
			}
			filteredLines = append(filteredLines, "")
			continue
		}
		if candidate == "```" {
			continue
		}
		filteredLines = append(filteredLines, candidate)
	}
	for len(filteredLines) > 0 && filteredLines[0] == "" {
		filteredLines = filteredLines[1:]
	}
	for len(filteredLines) > 0 && filteredLines[len(filteredLines)-1] == "" {
		filteredLines = filteredLines[:len(filteredLines)-1]
	}
	if len(filteredLines) == 0 {
		return ""
	}
	message := strings.Join(filteredLines, "\n")

	lowered := strings.ToLower(strings.TrimSpace(message))
	if strings.HasPrefix(lowered, "commit message:") {
		message = strings.TrimSpace(message[len("commit message:"):])
		lowered = strings.ToLower(strings.TrimSpace(message))
	}
	if strings.HasPrefix(lowered, "message:") {
		message = strings.TrimSpace(message[len("message:"):])
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return truncateText(message, 480)
}

func buildGitCommitPrompt(template string, files string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	return strings.TrimSpace(prompt)
}

func buildFallbackCommitMessage(files []ProjectGitChangedFile) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return fmt.Sprintf("Update %s", files[0].Path)
	}

	paths := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		paths = append(paths, file.Path)
	}
	if len(paths) == 0 {
		return fmt.Sprintf("Update %d files", len(files))
	}
	if len(paths) == 2 {
		return fmt.Sprintf("Update %s and %s", paths[0], paths[1])
	}
	return fmt.Sprintf("Update %d files (%s, %s, ...)", len(paths), paths[0], paths[1])
}
