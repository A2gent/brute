package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/A2gent/brute/internal/storage"
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
      "title": "human outcome, max 80 chars",
      "body": "1-3 concise sentences explaining WHAT the code now does and WHY that behavior matters"
    }
  ]
}
Rules:
- Pick multiple important, non-obvious changed regions per file when they exist. Prefer 2-5 annotations per complex file; use fewer only for simple files.
- line_number and end_line_number must refer to changed lines visible in the diff. Use additions for new lines and deletions for removed lines.
- Explain behavior, intent, data flow, user-visible effects, risk, or integration impact in human-readable words.
- Do NOT restate the diff, quote code, list symbols, mention +N/-N counts, or say only that code was added/removed/changed.
- Do NOT use generic titles like "Important branch change", "New file added", "Change added", or "Code updated".
- If you cannot explain WHAT the code does and WHY it matters, omit that annotation.

Current branch: {{branch}}
Base branch: {{base_branch}}

Changed files:
{{files}}

Diff snippets:
{{diffs}}`

type projectGitReviewOverlayLineIndex struct {
	Additions map[int]bool
	Deletions map[int]bool
	// Store changed-line snippets so fallback notes can still explain the diff when the model fails.
	AdditionText map[int]string
	DeletionText map[int]string
}

type projectGitReviewOverlayModelResponse struct {
	Annotations []ProjectGitReviewOverlayAnnotation `json:"annotations"`
}

type projectGitReviewOverlayDiffContext struct {
	Sections     []string
	AllowedLines map[string]projectGitReviewOverlayLineIndex
	DiffHashes   map[string]string
}

func (s *Server) handleGetProjectGitReviewOverlay(w http.ResponseWriter, r *http.Request) {
	projectID, repoPath, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if !ok {
		return
	}
	response := ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   []ProjectGitReviewOverlayAnnotation{},
	}
	if !target.Available {
		s.jsonResponse(w, http.StatusOK, response)
		return
	}

	files, err := loadProjectGitBranchChangedFiles(targetRepoRoot, target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changes: "+err.Error())
		return
	}
	diffContext := buildProjectGitReviewOverlayDiffContext(targetRepoRoot, target, files)
	annotations, err := s.loadCachedProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load review overlay: "+err.Error())
		return
	}
	response.Annotations = annotations
	s.jsonResponse(w, http.StatusOK, response)
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

	projectID, repoPath, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(req.RepoPath))
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

	annotations := s.generateProjectGitReviewOverlayAnnotations(r.Context(), projectID, repoPath, targetRepoRoot, target, files)
	s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   annotations,
	})
}

func (s *Server) generateProjectGitReviewOverlayAnnotations(ctx context.Context, projectID string, repoPath string, repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile) []ProjectGitReviewOverlayAnnotation {
	diffContext := buildProjectGitReviewOverlayDiffContext(repoRoot, target, files)
	if len(diffContext.Sections) == 0 {
		return []ProjectGitReviewOverlayAnnotation{}
	}

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
		strings.Join(diffContext.Sections, "\n\n"),
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
		return []ProjectGitReviewOverlayAnnotation{}
	}

	annotations := sanitizeProjectGitReviewOverlayResponse(response.Content, diffContext.AllowedLines)
	if len(annotations) == 0 {
		logging.Warn("Review overlay generation produced no useful annotations after sanitization")
		return []ProjectGitReviewOverlayAnnotation{}
	}
	if err := s.saveProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext.DiffHashes, annotations); err != nil {
		logging.Warn("Failed to cache review overlay annotations: %v", err)
	}
	return annotations
}

func (s *Server) loadCachedProjectGitReviewOverlayAnnotations(projectID string, repoPath string, target projectGitBranchChangesTargetInfo, diffContext projectGitReviewOverlayDiffContext) ([]ProjectGitReviewOverlayAnnotation, error) {
	rows, err := s.store.ListProjectGitReviewOverlayCache(projectID, repoPath, target.CurrentBranch, target.BaseBranch)
	if err != nil {
		return nil, err
	}
	annotations := []ProjectGitReviewOverlayAnnotation{}
	for _, row := range rows {
		filePath := normalizeProjectGitReviewOverlayPath(row.FilePath)
		if filePath == "" || diffContext.DiffHashes[filePath] == "" || diffContext.DiffHashes[filePath] != strings.TrimSpace(row.DiffHash) {
			continue
		}
		lineIndex, ok := diffContext.AllowedLines[filePath]
		if !ok {
			continue
		}
		var cached []ProjectGitReviewOverlayAnnotation
		if err := json.Unmarshal([]byte(row.AnnotationsJSON), &cached); err != nil {
			logging.Warn("Skipping invalid review overlay cache for %s: %v", filePath, err)
			continue
		}
		for _, annotation := range cached {
			annotation.FilePath = normalizeProjectGitReviewOverlayPath(annotation.FilePath)
			annotation.Side = normalizeProjectGitReviewOverlaySide(annotation.Side)
			annotation.Title = truncateText(cleanProjectGitReviewOverlayText(annotation.Title), 90)
			annotation.Body = truncateText(cleanProjectGitReviewOverlayText(annotation.Body), 520)
			if annotation.FilePath != filePath || annotation.Side == "" || annotation.LineNumber <= 0 {
				continue
			}
			if !projectGitReviewOverlayLineAllowed(lineIndex, annotation.Side, annotation.LineNumber) {
				continue
			}
			if annotation.EndLineNumber <= 0 || annotation.EndLineNumber < annotation.LineNumber || !projectGitReviewOverlayLineAllowed(lineIndex, annotation.Side, annotation.EndLineNumber) {
				annotation.EndLineNumber = annotation.LineNumber
			}
			if !isUsefulProjectGitReviewOverlayAnnotation(annotation.Title, annotation.Body) {
				continue
			}
			annotations = append(annotations, annotation)
		}
	}
	sortProjectGitReviewOverlayAnnotations(annotations)
	return annotations, nil
}

func (s *Server) saveProjectGitReviewOverlayAnnotations(projectID string, repoPath string, target projectGitBranchChangesTargetInfo, diffHashes map[string]string, annotations []ProjectGitReviewOverlayAnnotation) error {
	byFile := map[string][]ProjectGitReviewOverlayAnnotation{}
	for _, annotation := range annotations {
		filePath := normalizeProjectGitReviewOverlayPath(annotation.FilePath)
		if filePath == "" || diffHashes[filePath] == "" {
			continue
		}
		annotation.FilePath = filePath
		byFile[filePath] = append(byFile[filePath], annotation)
	}
	now := time.Now()
	for filePath, diffHash := range diffHashes {
		fileAnnotations := byFile[filePath]
		if fileAnnotations == nil {
			fileAnnotations = []ProjectGitReviewOverlayAnnotation{}
		}
		payload, err := json.Marshal(fileAnnotations)
		if err != nil {
			return err
		}
		cache := &storage.ProjectGitReviewOverlayCache{
			ProjectID:       projectID,
			RepoPath:        repoPath,
			Branch:          target.CurrentBranch,
			BaseBranch:      target.BaseBranch,
			FilePath:        filePath,
			DiffHash:        diffHash,
			AnnotationsJSON: string(payload),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.store.SaveProjectGitReviewOverlayCache(cache); err != nil {
			return err
		}
	}
	return nil
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

func buildProjectGitReviewOverlayDiffContext(repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile) projectGitReviewOverlayDiffContext {
	context := projectGitReviewOverlayDiffContext{
		Sections:     make([]string, 0, len(files)),
		AllowedLines: make(map[string]projectGitReviewOverlayLineIndex, len(files)),
		DiffHashes:   make(map[string]string, len(files)),
	}
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
		context.AllowedLines[normalizedPath] = lineIndex
		context.DiffHashes[normalizedPath] = hashProjectGitReviewOverlayDiff(preview)
		context.Sections = append(context.Sections, fmt.Sprintf(
			"File: %s (%s, +%d/-%d)\nAllowed changed lines: %s\n%s",
			normalizedPath,
			file.Status,
			file.Additions,
			file.Deletions,
			formatProjectGitReviewOverlayAllowedLines(lineIndex),
			truncateText(preview, 5000),
		))
		if len(context.Sections) >= 24 {
			break
		}
	}
	return context
}

func hashProjectGitReviewOverlayDiff(diff string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(diff, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
}

func formatProjectGitReviewOverlayAllowedLines(index projectGitReviewOverlayLineIndex) string {
	parts := []string{}
	if additions := formatProjectGitReviewOverlayLineNumbers(index.Additions); additions != "" {
		parts = append(parts, "additions="+additions)
	}
	if deletions := formatProjectGitReviewOverlayLineNumbers(index.Deletions); deletions != "" {
		parts = append(parts, "deletions="+deletions)
	}
	return strings.Join(parts, "; ")
}

func formatProjectGitReviewOverlayLineNumbers(lines map[int]bool) string {
	if len(lines) == 0 {
		return ""
	}
	values := make([]int, 0, len(lines))
	for line := range lines {
		values = append(values, line)
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, line := range values {
		parts = append(parts, fmt.Sprintf("%d", line))
	}
	return strings.Join(parts, ",")
}
func parseProjectGitReviewOverlayLineIndex(diff string) projectGitReviewOverlayLineIndex {
	index := projectGitReviewOverlayLineIndex{
		Additions:    map[int]bool{},
		Deletions:    map[int]bool{},
		AdditionText: map[int]string{},
		DeletionText: map[int]string{},
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
				index.AdditionText[newLine] = strings.TrimSpace(strings.TrimPrefix(line, "+"))
			}
			newLine++
			continue
		}
		if strings.HasPrefix(line, "-") {
			if oldLine > 0 {
				index.Deletions[oldLine] = true
				index.DeletionText[oldLine] = strings.TrimSpace(strings.TrimPrefix(line, "-"))
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
		if !isUsefulProjectGitReviewOverlayAnnotation(title, body) {
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
	sortProjectGitReviewOverlayAnnotations(annotations)
	return annotations
}

func sortProjectGitReviewOverlayAnnotations(annotations []ProjectGitReviewOverlayAnnotation) {
	sort.SliceStable(annotations, func(i, j int) bool {
		if annotations[i].FilePath != annotations[j].FilePath {
			return annotations[i].FilePath < annotations[j].FilePath
		}
		if annotations[i].LineNumber != annotations[j].LineNumber {
			return annotations[i].LineNumber < annotations[j].LineNumber
		}
		return annotations[i].Side < annotations[j].Side
	})
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
func isUsefulProjectGitReviewOverlayAnnotation(title string, body string) bool {
	title = cleanProjectGitReviewOverlayText(title)
	body = cleanProjectGitReviewOverlayText(body)
	if title == "" || body == "" {
		return false
	}
	lowerTitle := strings.ToLower(title)
	genericTitles := []string{
		"important branch change",
		"new file added",
		"file removed",
		"change added",
		"code updated",
		"changed code",
		"branch change",
	}
	for _, generic := range genericTitles {
		if lowerTitle == generic {
			return false
		}
	}
	lowerBody := strings.ToLower(body)
	obviousPhrases := []string{
		"this change adds",
		"this change removes",
		"this change replaces",
		"this changed region",
		"this new file adds",
		"this file is introduced",
		"this file removes",
		"branch diff of",
		"+%d/-%d",
	}
	for _, phrase := range obviousPhrases {
		if strings.Contains(lowerBody, phrase) {
			return false
		}
	}
	// WHY: overlay comments should explain behavior/intent, not paraphrase the patch.
	// Requiring one explanatory cue filters out generic model output while keeping concise notes.
	explanatoryCues := []string{
		"because", "so that", "so ", "ensures", "prevents", "allows", "enables", "keeps",
		"means", "instead", "when ", "before ", "after ", "user", "request", "state", "flow", "fallback", "error", "validation",
	}
	for _, cue := range explanatoryCues {
		if strings.Contains(lowerBody, cue) {
			return true
		}
	}
	return false
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
			Body:          projectGitReviewOverlayFallbackBody(file, lineIndex, lineNumber),
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

func projectGitReviewOverlayFallbackBody(file ProjectGitCommitFile, lineIndex projectGitReviewOverlayLineIndex, lineNumber int) string {
	stats := fmt.Sprintf("+%d/-%d", file.Additions, file.Deletions)
	switch {
	case strings.HasPrefix(file.Status, "A"):
		if added := projectGitReviewOverlaySnippet(lineIndex.AdditionText, lineNumber); added != "" {
			return fmt.Sprintf("This new file adds `%s` (%s). Review how this new behavior is reached and whether callers handle it.", added, stats)
		}
		return fmt.Sprintf("This file is introduced on the branch (%s). Review the new behavior and integration points before merging.", stats)
	case strings.HasPrefix(file.Status, "D"):
		if removed := projectGitReviewOverlaySnippet(lineIndex.DeletionText, lineNumber); removed != "" {
			return fmt.Sprintf("This file removes `%s` (%s). Check that callers or references were updated accordingly.", removed, stats)
		}
		return fmt.Sprintf("This file is removed on the branch (%s). Check that callers or references were updated accordingly.", stats)
	default:
		if added, removed := projectGitReviewOverlayNearbySnippets(lineIndex, lineNumber); added != "" && removed != "" {
			return fmt.Sprintf("This change replaces `%s` with `%s` (%s). Review the behavior shift at this location and confirm related callers still follow the intended flow.", removed, added, stats)
		}
		if added := projectGitReviewOverlaySnippet(lineIndex.AdditionText, lineNumber); added != "" {
			return fmt.Sprintf("This change adds `%s` in this region (%s). Review how the new behavior affects the surrounding flow.", added, stats)
		}
		if removed := projectGitReviewOverlaySnippet(lineIndex.DeletionText, lineNumber); removed != "" {
			return fmt.Sprintf("This change removes `%s` from this region (%s). Check that the removed behavior is no longer needed.", removed, stats)
		}
		return fmt.Sprintf("This changed region is worth reviewing because this file has a branch diff of %s.", stats)
	}
}

func projectGitReviewOverlayNearbySnippets(lineIndex projectGitReviewOverlayLineIndex, lineNumber int) (string, string) {
	added := projectGitReviewOverlaySnippet(lineIndex.AdditionText, lineNumber)
	removed := projectGitReviewOverlaySnippet(lineIndex.DeletionText, lineNumber)
	if added != "" && removed != "" {
		return added, removed
	}
	for distance := 1; distance <= 3; distance++ {
		if added == "" {
			added = projectGitReviewOverlaySnippet(lineIndex.AdditionText, lineNumber+distance)
		}
		if removed == "" {
			removed = projectGitReviewOverlaySnippet(lineIndex.DeletionText, lineNumber-distance)
		}
		if added != "" && removed != "" {
			return added, removed
		}
	}
	return added, removed
}

func projectGitReviewOverlaySnippet(lines map[int]string, lineNumber int) string {
	line := cleanProjectGitReviewOverlayText(lines[lineNumber])
	if line == "" {
		return ""
	}
	return truncateText(line, 180)
}

func buildGitReviewOverlayPrompt(template string, branch string, baseBranch string, files string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{branch}}", strings.TrimSpace(branch))
	prompt = strings.ReplaceAll(prompt, "{{base_branch}}", strings.TrimSpace(baseBranch))
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	return strings.TrimSpace(prompt)
}
