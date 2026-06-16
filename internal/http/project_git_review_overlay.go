package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const defaultGitReviewOverlayPromptTemplate = `You explain a branch diff for a visual review overlay.
Return JSON only. No markdown fences, prefaces, or commentary.
Schema:
{
  "annotations": [
    {
      "file_path": "path/from/input",
      "side": "additions" | "deletions",
      "line_number": 123,
      "end_line_number": 125,
      "title": "short label, max 80 chars",
      "body": "1-3 concise sentences explaining what changed and why it matters"
    }
  ]
}
Rules:
- Pick only important or non-obvious changed regions. Do not annotate trivial formatting, imports, or every line.
- Prefer 1-3 annotations per changed file; use fewer if the change is simple.
- line_number and end_line_number must refer to changed lines visible in the diff. Use additions for new lines and deletions for removed lines.
- Explain code behavior and intent without suggesting adding code comments.

Current branch: {{branch}}
Base branch: {{base_branch}}

Changed files:
{{files}}

Diff snippets:
{{diffs}}`

type projectGitReviewOverlayLineIndex struct {
	Additions map[int]bool
	Deletions map[int]bool
}

type projectGitReviewOverlayModelResponse struct {
	Annotations []ProjectGitReviewOverlayAnnotation `json:"annotations"`
}

func (s *Server) handleGenerateProjectGitReviewOverlay(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	var req ProjectGitReviewOverlayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	_, _, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(req.RepoPath))
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
	if len(files) == 0 {
		s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
			CurrentBranch: target.CurrentBranch,
			BaseBranch:    target.BaseBranch,
			Annotations:   []ProjectGitReviewOverlayAnnotation{},
		})
		return
	}

	annotations := s.generateProjectGitReviewOverlayAnnotations(r.Context(), targetRepoRoot, target, files)
	s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   annotations,
	})
}

func (s *Server) generateProjectGitReviewOverlayAnnotations(ctx context.Context, repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile) []ProjectGitReviewOverlayAnnotation {
	diffSections, allowedLines := buildProjectGitReviewOverlayDiffContext(repoRoot, target, files)
	if len(diffSections) == 0 {
		return []ProjectGitReviewOverlayAnnotation{}
	}

	fallback := buildFallbackProjectGitReviewOverlayAnnotations(files, allowedLines)
	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for review overlay generation: %v", settingsErr)
		settings = map[string]string{}
	}
	prompt := buildGitReviewOverlayPrompt(
		defaultGitReviewOverlayPromptTemplate,
		target.CurrentBranch,
		target.BaseBranch,
		projectGitCommitFilesMarkdown(files),
		strings.Join(diffSections, "\n\n"),
	)

	providerRef := strings.TrimSpace(settings[gitCommitProviderSettingKey])
	if providerRef == "" {
		providerRef = s.config.ActiveProvider
	}
	configuredProviderType := config.ProviderType(config.NormalizeProviderRef(providerRef))
	activeProviderType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))

	generationCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	response, err := s.generateGitReviewOverlayWithProvider(generationCtx, configuredProviderType, prompt)
	if err != nil && configuredProviderType != activeProviderType {
		logging.Warn("Review overlay generation failed with configured provider %s: %v. Retrying active provider %s", configuredProviderType, err, activeProviderType)
		response, err = s.generateGitReviewOverlayWithProvider(generationCtx, activeProviderType, prompt)
	}
	if err != nil {
		logging.Warn("Review overlay generation failed: %v", err)
		return fallback
	}

	annotations := sanitizeProjectGitReviewOverlayResponse(response.Content, allowedLines)
	if len(annotations) == 0 {
		return fallback
	}
	return annotations
}

func (s *Server) generateGitReviewOverlayWithProvider(ctx context.Context, providerType config.ProviderType, prompt string) (*llm.ChatResponse, error) {
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
		MaxTokens:   1600,
	})
}

func buildProjectGitReviewOverlayDiffContext(repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile) ([]string, map[string]projectGitReviewOverlayLineIndex) {
	sections := make([]string, 0, len(files))
	allowedLines := make(map[string]projectGitReviewOverlayLineIndex, len(files))
	for _, file := range files {
		if file.Binary {
			continue
		}
		normalizedPath, err := resolveGitRepoFilePath(repoRoot, file.Path)
		if err != nil {
			continue
		}
		preview, err := runGitCommandPreserveLeading(repoRoot, "diff", "--no-color", "--find-renames", "--unified=8", target.BaseRef+"...HEAD", "--", normalizedPath)
		if err != nil || strings.TrimSpace(preview) == "" {
			continue
		}
		lineIndex := parseProjectGitReviewOverlayLineIndex(preview)
		if len(lineIndex.Additions) == 0 && len(lineIndex.Deletions) == 0 {
			continue
		}
		allowedLines[normalizedPath] = lineIndex
		sections = append(sections, fmt.Sprintf("File: %s (%s, +%d/-%d)\n%s", normalizedPath, file.Status, file.Additions, file.Deletions, truncateText(preview, 5000)))
		if len(sections) >= 24 {
			break
		}
	}
	return sections, allowedLines
}

func parseProjectGitReviewOverlayLineIndex(diff string) projectGitReviewOverlayLineIndex {
	index := projectGitReviewOverlayLineIndex{
		Additions: map[int]bool{},
		Deletions: map[int]bool{},
	}
	oldLine := 0
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "@@") {
			parsedOld, parsedNew, ok := parseProjectGitReviewOverlayHunkHeader(line)
			if ok {
				oldLine = parsedOld
				newLine = parsedNew
				inHunk = true
			}
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || !inHunk {
			continue
		}
		if strings.HasPrefix(line, "+") {
			if newLine > 0 {
				index.Additions[newLine] = true
			}
			newLine++
			continue
		}
		if strings.HasPrefix(line, "-") {
			if oldLine > 0 {
				index.Deletions[oldLine] = true
			}
			oldLine++
			continue
		}
		if strings.HasPrefix(line, "\\") {
			continue
		}
		oldLine++
		newLine++
	}
	return index
}

func parseProjectGitReviewOverlayHunkHeader(line string) (int, int, bool) {
	parts := strings.Fields(line)
	if len(parts) < 3 || !strings.HasPrefix(parts[1], "-") || !strings.HasPrefix(parts[2], "+") {
		return 0, 0, false
	}
	oldLine, ok := parseProjectGitReviewOverlayHunkStart(parts[1])
	if !ok {
		return 0, 0, false
	}
	newLine, ok := parseProjectGitReviewOverlayHunkStart(parts[2])
	if !ok {
		return 0, 0, false
	}
	return oldLine, newLine, true
}

func parseProjectGitReviewOverlayHunkStart(raw string) (int, bool) {
	trimmed := strings.TrimLeft(strings.TrimSpace(raw), "+-")
	if commaIndex := strings.Index(trimmed, ","); commaIndex >= 0 {
		trimmed = trimmed[:commaIndex]
	}
	var value int
	if _, err := fmt.Sscanf(trimmed, "%d", &value); err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func sanitizeProjectGitReviewOverlayResponse(raw string, allowedLines map[string]projectGitReviewOverlayLineIndex) []ProjectGitReviewOverlayAnnotation {
	payload := extractProjectGitReviewOverlayJSON(raw)
	if strings.TrimSpace(payload) == "" {
		return []ProjectGitReviewOverlayAnnotation{}
	}

	var modelResponse projectGitReviewOverlayModelResponse
	if err := json.Unmarshal([]byte(payload), &modelResponse); err != nil {
		return []ProjectGitReviewOverlayAnnotation{}
	}

	annotations := make([]ProjectGitReviewOverlayAnnotation, 0, len(modelResponse.Annotations))
	seen := map[string]bool{}
	for _, annotation := range modelResponse.Annotations {
		filePath := normalizeProjectGitReviewOverlayPath(annotation.FilePath)
		lineIndex, ok := allowedLines[filePath]
		if !ok {
			continue
		}
		side := normalizeProjectGitReviewOverlaySide(annotation.Side)
		if side == "" || annotation.LineNumber <= 0 {
			continue
		}
		if !projectGitReviewOverlayLineAllowed(lineIndex, side, annotation.LineNumber) {
			continue
		}
		endLine := annotation.EndLineNumber
		if endLine <= 0 || endLine < annotation.LineNumber || !projectGitReviewOverlayLineAllowed(lineIndex, side, endLine) {
			endLine = annotation.LineNumber
		}
		title := truncateText(cleanProjectGitReviewOverlayText(annotation.Title), 90)
		body := truncateText(cleanProjectGitReviewOverlayText(annotation.Body), 520)
		if title == "" || body == "" {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d", filePath, side, annotation.LineNumber)
		if seen[key] {
			continue
		}
		seen[key] = true
		annotations = append(annotations, ProjectGitReviewOverlayAnnotation{
			FilePath:      filePath,
			Side:          side,
			LineNumber:    annotation.LineNumber,
			EndLineNumber: endLine,
			Title:         title,
			Body:          body,
		})
		if len(annotations) >= 80 {
			break
		}
	}
	sort.SliceStable(annotations, func(i, j int) bool {
		if annotations[i].FilePath != annotations[j].FilePath {
			return annotations[i].FilePath < annotations[j].FilePath
		}
		if annotations[i].LineNumber != annotations[j].LineNumber {
			return annotations[i].LineNumber < annotations[j].LineNumber
		}
		return annotations[i].Side < annotations[j].Side
	})
	return annotations
}

func extractProjectGitReviewOverlayJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.Trim(trimmed, "\ufeff")
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if strings.TrimSpace(lines[len(lines)-1]) == "```" {
				lines = lines[:len(lines)-1]
			}
			trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end >= start {
		return trimmed[start : end+1]
	}
	return trimmed
}

func projectGitReviewOverlayLineAllowed(index projectGitReviewOverlayLineIndex, side string, lineNumber int) bool {
	if side == "additions" {
		return index.Additions[lineNumber]
	}
	if side == "deletions" {
		return index.Deletions[lineNumber]
	}
	return false
}

func normalizeProjectGitReviewOverlaySide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "addition", "additions", "new":
		return "additions"
	case "deletion", "deletions", "old", "removed":
		return "deletions"
	default:
		return ""
	}
}

func normalizeProjectGitReviewOverlayPath(path string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "a/")
	trimmed = strings.TrimPrefix(trimmed, "b/")
	trimmed = strings.Trim(trimmed, "/")
	return trimmed
}

func cleanProjectGitReviewOverlayText(value string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	trimmed = strings.Trim(trimmed, "\"'`")
	fields := strings.Fields(trimmed)
	return strings.Join(fields, " ")
}

func buildFallbackProjectGitReviewOverlayAnnotations(files []ProjectGitCommitFile, allowedLines map[string]projectGitReviewOverlayLineIndex) []ProjectGitReviewOverlayAnnotation {
	annotations := make([]ProjectGitReviewOverlayAnnotation, 0)
	for _, file := range files {
		lineIndex, ok := allowedLines[file.Path]
		if !ok || file.Binary {
			continue
		}
		side := "additions"
		lineNumber := firstProjectGitReviewOverlayLine(lineIndex.Additions)
		if lineNumber == 0 {
			side = "deletions"
			lineNumber = firstProjectGitReviewOverlayLine(lineIndex.Deletions)
		}
		if lineNumber == 0 {
			continue
		}
		annotations = append(annotations, ProjectGitReviewOverlayAnnotation{
			FilePath:      file.Path,
			Side:          side,
			LineNumber:    lineNumber,
			EndLineNumber: lineNumber,
			Title:         projectGitReviewOverlayFallbackTitle(file),
			Body:          projectGitReviewOverlayFallbackBody(file),
		})
		if len(annotations) >= 12 {
			break
		}
	}
	return annotations
}

func firstProjectGitReviewOverlayLine(lines map[int]bool) int {
	first := 0
	for line := range lines {
		if first == 0 || line < first {
			first = line
		}
	}
	return first
}

func projectGitReviewOverlayFallbackTitle(file ProjectGitCommitFile) string {
	switch {
	case strings.HasPrefix(file.Status, "A"):
		return "New file added"
	case strings.HasPrefix(file.Status, "D"):
		return "File removed"
	default:
		return "Important branch change"
	}
}

func projectGitReviewOverlayFallbackBody(file ProjectGitCommitFile) string {
	stats := fmt.Sprintf("+%d/-%d", file.Additions, file.Deletions)
	switch {
	case strings.HasPrefix(file.Status, "A"):
		return fmt.Sprintf("This file is introduced on the branch (%s). Review the new behavior and integration points before merging.", stats)
	case strings.HasPrefix(file.Status, "D"):
		return fmt.Sprintf("This file is removed on the branch (%s). Check that callers or references were updated accordingly.", stats)
	default:
		return fmt.Sprintf("This region is part of a non-trivial branch diff in this file (%s). The model fallback marks it for focused review.", stats)
	}
}

func buildGitReviewOverlayPrompt(template string, branch string, baseBranch string, files string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{branch}}", strings.TrimSpace(branch))
	prompt = strings.ReplaceAll(prompt, "{{base_branch}}", strings.TrimSpace(baseBranch))
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	return strings.TrimSpace(prompt)
}
