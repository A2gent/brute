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
	"github.com/A2gent/brute/internal/storage"
)

const defaultGitPRDescriptionPromptTemplate = `Generate a very condensed pull request description from the git context below.
Return markdown only. Do not include code fences, prefaces, or commentary.
Use exactly this structure:

## Why
One short paragraph explaining why this change is needed.

## Changes
- 3-6 concise bullets covering the main changes.

## Testing
One short paragraph or a short markdown list describing relevant verification. If no test run is evident, say so directly.

Current branch: {{branch}}
Base branch: {{base_branch}}

Changed files:
{{files}}

Commit history:
{{history}}

Diff stat:
{{stat}}

Diff snippets:
{{diffs}}`

func (s *Server) handleGetProjectGitPRDescription(w http.ResponseWriter, r *http.Request) {
	projectID, repoPath, _, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if !ok {
		return
	}

	response := ProjectGitPRDescriptionResponse{
		ProjectID:     projectID,
		RepoPath:      repoPath,
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Available:     target.Available,
		Content:       "",
	}
	if !target.Available {
		s.jsonResponse(w, http.StatusOK, response)
		return
	}

	description, err := s.store.GetProjectPRDescription(projectID, repoPath, target.CurrentBranch, target.BaseBranch)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load PR description: "+err.Error())
		return
	}
	if description != nil {
		response.Content = description.Content
		response.CreatedAt = description.CreatedAt.Format(time.RFC3339)
		response.UpdatedAt = description.UpdatedAt.Format(time.RFC3339)
	}

	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleSaveProjectGitPRDescription(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	var req ProjectGitPRDescriptionSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if len(req.Content) > 65536 {
		s.errorResponse(w, http.StatusBadRequest, "PR description is too large")
		return
	}

	_, repoPath, _, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}
	if !target.Available {
		s.errorResponse(w, http.StatusBadRequest, "Branch comparison is not available for this repository")
		return
	}

	now := time.Now()
	description := &storage.ProjectPRDescription{
		ProjectID:  projectID,
		RepoPath:   repoPath,
		Branch:     target.CurrentBranch,
		BaseBranch: target.BaseBranch,
		Content:    req.Content,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.store.SaveProjectPRDescription(description); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save PR description: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectPRDescriptionToResponse(description, target.Available))
}

func (s *Server) handleGenerateProjectGitPRDescription(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	var req ProjectGitPRDescriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	_, repoPath, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}
	if !target.Available {
		s.errorResponse(w, http.StatusBadRequest, "Branch comparison is not available for this repository")
		return
	}

	files, err := loadProjectGitBranchChangedFiles(targetRepoRoot, target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changes: "+err.Error())
		return
	}

	content := s.generateProjectGitPRDescription(r.Context(), targetRepoRoot, target, files)
	now := time.Now()
	description := &storage.ProjectPRDescription{
		ProjectID:  projectID,
		RepoPath:   repoPath,
		Branch:     target.CurrentBranch,
		BaseBranch: target.BaseBranch,
		Content:    content,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.store.SaveProjectPRDescription(description); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save generated PR description: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectPRDescriptionToResponse(description, target.Available))
}

func (s *Server) resolveProjectGitPRDescriptionTarget(w http.ResponseWriter, r *http.Request, repoPathParam string) (string, string, string, projectGitBranchChangesTargetInfo, bool) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return "", "", "", projectGitBranchChangesTargetInfo{}, false
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return "", "", "", projectGitBranchChangesTargetInfo{}, false
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPathParam)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return "", "", "", projectGitBranchChangesTargetInfo{}, false
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return "", "", "", projectGitBranchChangesTargetInfo{}, false
	}

	target := projectGitBranchChangesTarget(targetRepoRoot)
	return projectID, normalizeProjectGitPRDescriptionRepoPath(repoPathParam), targetRepoRoot, target, true
}

func projectPRDescriptionToResponse(description *storage.ProjectPRDescription, available bool) ProjectGitPRDescriptionResponse {
	if description == nil {
		return ProjectGitPRDescriptionResponse{Available: available}
	}
	return ProjectGitPRDescriptionResponse{
		ProjectID:     description.ProjectID,
		RepoPath:      description.RepoPath,
		CurrentBranch: description.Branch,
		BaseBranch:    description.BaseBranch,
		Available:     available,
		Content:       description.Content,
		CreatedAt:     description.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     description.UpdatedAt.Format(time.RFC3339),
	}
}

func normalizeProjectGitPRDescriptionRepoPath(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "." {
		return ""
	}
	return strings.Trim(strings.ReplaceAll(trimmed, "\\", "/"), "/")
}

func loadProjectGitBranchChangedFiles(repoRoot string, target projectGitBranchChangesTargetInfo) ([]ProjectGitCommitFile, error) {
	if !target.Available {
		return []ProjectGitCommitFile{}, nil
	}

	statusOutput, err := runGitCommandPreserveLeading(repoRoot, "diff", "--name-status", "--find-renames", target.BaseRef+"...HEAD")
	if err != nil {
		return nil, err
	}
	statuses := parseProjectGitCommitFileStatuses(statusOutput)

	statsOutput, err := runGitCommandPreserveLeading(repoRoot, "diff", "--numstat", "--find-renames", target.BaseRef+"...HEAD")
	if err != nil {
		return nil, err
	}
	return mergeProjectGitCommitFiles(statuses, statsOutput), nil
}

func (s *Server) generateProjectGitPRDescription(ctx context.Context, repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile) string {
	history := readProjectGitPRDescriptionHistory(repoRoot, target)
	fallback := buildFallbackProjectGitPRDescription(target, files, history)

	stat, _ := runGitCommandPreserveLeading(repoRoot, "diff", "--stat=80", "--find-renames", target.BaseRef+"...HEAD")
	diff, _ := runGitCommandPreserveLeading(repoRoot, "diff", "--no-color", "--find-renames", "--unified=3", target.BaseRef+"...HEAD")
	if strings.TrimSpace(diff) == "" {
		return fallback
	}

	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for PR description generation: %v", settingsErr)
		settings = map[string]string{}
	}
	templates := serverPromptTemplatesFromSettings(settings)
	prompt := buildGitPRDescriptionPrompt(
		templates.GitPRDescriptionPromptTemplate,
		target.CurrentBranch,
		target.BaseBranch,
		projectGitCommitFilesMarkdown(files),
		history,
		stat,
		truncateText(diff, 14000),
	)

	providerRef := strings.TrimSpace(settings[gitCommitProviderSettingKey])
	if providerRef == "" {
		providerRef = s.config.ActiveProvider
	}
	configuredProviderType := config.ProviderType(config.NormalizeProviderRef(providerRef))
	activeProviderType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))

	generationCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	response, err := s.generateGitPRDescriptionWithProvider(generationCtx, configuredProviderType, prompt)
	if err != nil && configuredProviderType != activeProviderType {
		logging.Warn("PR description generation failed with configured provider %s: %v. Retrying active provider %s", configuredProviderType, err, activeProviderType)
		response, err = s.generateGitPRDescriptionWithProvider(generationCtx, activeProviderType, prompt)
	}
	if err != nil {
		logging.Warn("PR description generation failed: %v", err)
		return fallback
	}

	description := sanitizeGeneratedPRDescription(response.Content)
	if description == "" {
		return fallback
	}
	return description
}

func (s *Server) generateGitPRDescriptionWithProvider(ctx context.Context, providerType config.ProviderType, prompt string) (*llm.ChatResponse, error) {
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
		MaxTokens:   700,
	})
}

func readProjectGitPRDescriptionHistory(repoRoot string, target projectGitBranchChangesTargetInfo) string {
	if !target.Available {
		return ""
	}
	history, err := runGitCommandPreserveLeading(
		repoRoot,
		"log",
		"--no-merges",
		"--date=short",
		"--pretty=format:- %h %ad %s",
		"-n",
		"30",
		target.BaseRef+"..HEAD",
	)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(history)
}

func buildGitPRDescriptionPrompt(template string, branch string, baseBranch string, files string, history string, stat string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{branch}}", strings.TrimSpace(branch))
	prompt = strings.ReplaceAll(prompt, "{{base_branch}}", strings.TrimSpace(baseBranch))
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{history}}", fallbackText(strings.TrimSpace(history), "- No branch-only commits found."))
	prompt = strings.ReplaceAll(prompt, "{{stat}}", fallbackText(strings.TrimSpace(stat), "No diff stat available."))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	return strings.TrimSpace(prompt)
}

func sanitizeGeneratedPRDescription(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.Trim(trimmed, "\"'`")
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "markdown"))
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "Markdown"))
	trimmed = strings.Trim(trimmed, "\"'`")
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")

	lines := strings.Split(trimmed, "\n")
	filteredLines := make([]string, 0, len(lines))
	for _, line := range lines {
		candidate := strings.TrimSpace(strings.Trim(line, "\"'`"))
		if candidate == "```" || strings.EqualFold(candidate, "```markdown") {
			continue
		}
		filteredLines = append(filteredLines, strings.TrimRight(line, " \t"))
	}
	for len(filteredLines) > 0 && strings.TrimSpace(filteredLines[0]) == "" {
		filteredLines = filteredLines[1:]
	}
	for len(filteredLines) > 0 && strings.TrimSpace(filteredLines[len(filteredLines)-1]) == "" {
		filteredLines = filteredLines[:len(filteredLines)-1]
	}
	content := strings.TrimSpace(strings.Join(filteredLines, "\n"))
	if !hasMarkdownSection(content, "Why") || !hasMarkdownSection(content, "Changes") || !hasMarkdownSection(content, "Testing") {
		return ""
	}
	return truncateText(content, 5000)
}

func hasMarkdownSection(content string, title string) bool {
	needle := "## " + strings.ToLower(strings.TrimSpace(title))
	for _, line := range strings.Split(content, "\n") {
		if strings.ToLower(strings.TrimSpace(line)) == needle {
			return true
		}
	}
	return false
}

func buildFallbackProjectGitPRDescription(target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile, history string) string {
	branch := fallbackText(target.CurrentBranch, "this branch")
	base := fallbackText(target.BaseBranch, "the base branch")
	why := fmt.Sprintf("This branch prepares the current implementation work on `%s` for review against `%s`.", branch, base)
	if strings.TrimSpace(history) != "" {
		why = fmt.Sprintf("This branch collects the committed work on `%s` for review against `%s`.", branch, base)
	}

	changes := projectGitPRDescriptionFallbackChanges(files)
	return strings.TrimSpace(fmt.Sprintf(`## Why
%s

## Changes
%s

## Testing
Not run (generated from git diff and history only).`, why, strings.Join(changes, "\n")))
}

func projectGitPRDescriptionFallbackChanges(files []ProjectGitCommitFile) []string {
	if len(files) == 0 {
		return []string{"- No file changes were detected."}
	}
	limit := len(files)
	if limit > 6 {
		limit = 6
	}
	changes := make([]string, 0, limit+1)
	for _, file := range files[:limit] {
		stats := ""
		if !file.Binary {
			stats = fmt.Sprintf(" (+%d/-%d)", file.Additions, file.Deletions)
		}
		changes = append(changes, fmt.Sprintf("- %s `%s`%s.", projectGitPRDescriptionStatusVerb(file.Status), file.Path, stats))
	}
	if len(files) > limit {
		changes = append(changes, fmt.Sprintf("- Includes %d additional changed file(s).", len(files)-limit))
	}
	return changes
}

func projectGitCommitFilesMarkdown(files []ProjectGitCommitFile) string {
	if len(files) == 0 {
		return "- No changed files."
	}
	lines := make([]string, 0, len(files))
	for _, file := range files {
		stats := "binary"
		if !file.Binary {
			stats = fmt.Sprintf("+%d -%d", file.Additions, file.Deletions)
		}
		lines = append(lines, fmt.Sprintf("- %s (%s, %s)", file.Path, fallbackText(file.Status, "changed"), stats))
	}
	return strings.Join(lines, "\n")
}

func projectGitPRDescriptionStatusVerb(status string) string {
	normalized := strings.ToUpper(strings.TrimSpace(status))
	switch {
	case strings.HasPrefix(normalized, "A"):
		return "Add"
	case strings.HasPrefix(normalized, "D"):
		return "Remove"
	case strings.HasPrefix(normalized, "R"):
		return "Rename"
	case strings.HasPrefix(normalized, "C"):
		return "Copy"
	default:
		return "Update"
	}
}

func fallbackText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
