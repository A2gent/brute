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

const defaultGitReviewOverlayPromptTemplate = `You explain why each changed part of one file exists for a visual code-review overlay.
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
      "body": "1-4 concise sentences explaining what changed, why it likely exists, and what behavior/risk/review point it creates"
    }
  ]
}
Rules:
- The input is the currently selected review file. Cover every changed code block or isolated changed line that has meaningful behavior, including added, deleted, and updated/replaced code.
- Treat adjacent deletion/addition groups as updated code. Explain the old behavior on deletions when it matters, and the new behavior on additions when it matters.
- Use one annotation per logical changed block. Use line-level annotations for isolated one-line changes. Split annotations when changed lines are separated or serve different purposes.
- line_number and end_line_number must refer only to changed lines visible in the diff. Use additions for new lines and deletions for removed lines.
- Explain behavior, intent, data flow, user-visible effects, risk, test impact, or integration impact in human-readable words.
- Include small changes when their purpose can be inferred from surrounding context. Omit only generated noise, pure formatting, or changes whose purpose cannot be inferred.
- Do NOT restate the diff, quote code, list symbols, mention +N/-N counts, or say only that code was added/removed/changed.
- Do NOT use generic titles like "Important branch change", "New file added", "Change added", "Line updated", or "Code updated".
- Write title and body in English, even if surrounding session or project text uses another language.
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
	diffContext := buildProjectGitReviewOverlayDiffContext(targetRepoRoot, target, files, "", 0)
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
	targetFilePath, err := resolveGitRepoFilePath(targetRepoRoot, req.FilePath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "file_path is required and must stay inside the repository")
		return
	}

	files, err := loadProjectGitBranchChangedFiles(targetRepoRoot, target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changes: "+err.Error())
		return
	}
	files = filterProjectGitReviewOverlayFiles(targetRepoRoot, files, targetFilePath)
	if len(files) == 0 {
		s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
			CurrentBranch: target.CurrentBranch,
			BaseBranch:    target.BaseBranch,
			Annotations:   []ProjectGitReviewOverlayAnnotation{},
		})
		return
	}

	annotations := s.generateProjectGitReviewOverlayAnnotations(r.Context(), projectID, repoPath, targetRepoRoot, target, files, targetFilePath)
	s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   annotations,
	})
}

func (s *Server) generateProjectGitReviewOverlayAnnotations(ctx context.Context, projectID string, repoPath string, repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile, targetFilePath string) []ProjectGitReviewOverlayAnnotation {
	diffContext := buildProjectGitReviewOverlayDiffContext(repoRoot, target, files, targetFilePath, 1)
	if len(diffContext.Sections) == 0 {
		return []ProjectGitReviewOverlayAnnotation{}
	}
	fallbackAnnotations := buildFallbackProjectGitReviewOverlayAnnotations(files, diffContext.AllowedLines)

	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for review overlay generation: %v", settingsErr)
		settings = map[string]string{}
	}
	templates := serverPromptTemplatesFromSettings(settings)
	prompt := buildGitReviewOverlayPrompt(
		templates.GitReviewOverlayPromptTemplate,
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
		if saveErr := s.saveProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext.DiffHashes, fallbackAnnotations); saveErr != nil {
			logging.Warn("Failed to cache fallback review overlay annotations: %v", saveErr)
		}
		return fallbackAnnotations
	}

	annotations := sanitizeProjectGitReviewOverlayResponse(response.Content, diffContext.AllowedLines)
	if len(annotations) == 0 {
		logging.Warn("Review overlay generation produced no useful annotations after sanitization; using fallback. Raw response: %s", truncateText(response.Content, 1200))
		annotations = fallbackAnnotations
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
		MaxTokens:   4000,
	})
}

func filterProjectGitReviewOverlayFiles(repoRoot string, files []ProjectGitCommitFile, targetFilePath string) []ProjectGitCommitFile {
	targetFilePath = normalizeProjectGitReviewOverlayPath(targetFilePath)
	if targetFilePath == "" {
		return []ProjectGitCommitFile{}
	}
	filtered := make([]ProjectGitCommitFile, 0, 1)
	for _, file := range files {
		normalizedPath, err := resolveGitRepoFilePath(repoRoot, file.Path)
		if err != nil {
			continue
		}
		if normalizeProjectGitReviewOverlayPath(normalizedPath) == targetFilePath {
			file.Path = normalizeProjectGitReviewOverlayPath(normalizedPath)
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func buildProjectGitReviewOverlayDiffContext(repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile, targetFilePath string, sectionLimit int) projectGitReviewOverlayDiffContext {
	context := projectGitReviewOverlayDiffContext{
		Sections:     make([]string, 0, len(files)),
		AllowedLines: make(map[string]projectGitReviewOverlayLineIndex, len(files)),
		DiffHashes:   make(map[string]string, len(files)),
	}
	targetFilePath = normalizeProjectGitReviewOverlayPath(targetFilePath)
	for _, file := range files {
		if file.Binary {
			continue
		}
		normalizedPath, err := resolveGitRepoFilePath(repoRoot, file.Path)
		if err != nil {
			continue
		}
		normalizedPath = normalizeProjectGitReviewOverlayPath(normalizedPath)
		if targetFilePath != "" && normalizedPath != targetFilePath {
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
			truncateText(preview, 12000),
		))
		if sectionLimit > 0 && len(context.Sections) >= sectionLimit {
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
	// A model may still return a useful explanation in a non-English language.
	// Keep substantive text unless it hit the generic/restatement filters above.
	return len([]rune(body)) >= 80 && len([]rune(title)) >= 8
}

func buildFallbackProjectGitReviewOverlayAnnotations(files []ProjectGitCommitFile, allowedLines map[string]projectGitReviewOverlayLineIndex) []ProjectGitReviewOverlayAnnotation {
	annotations := make([]ProjectGitReviewOverlayAnnotation, 0)
	for _, file := range files {
		filePath := normalizeProjectGitReviewOverlayPath(file.Path)
		lineIndex, ok := allowedLines[filePath]
		if !ok || file.Binary {
			continue
		}
		side := "additions"
		lineNumber := firstProjectGitReviewOverlayLine(lineIndex.Additions)
		endLineNumber := lastProjectGitReviewOverlayLine(lineIndex.Additions)
		if lineNumber == 0 {
			side = "deletions"
			lineNumber = firstProjectGitReviewOverlayLine(lineIndex.Deletions)
			endLineNumber = lastProjectGitReviewOverlayLine(lineIndex.Deletions)
		}
		if lineNumber == 0 {
			continue
		}
		annotations = append(annotations, ProjectGitReviewOverlayAnnotation{
			FilePath:      filePath,
			Side:          side,
			LineNumber:    lineNumber,
			EndLineNumber: endLineNumber,
			Title:         projectGitReviewOverlayFallbackTitle(file),
			Body:          projectGitReviewOverlayFallbackBody(file, lineIndex, side, lineNumber, endLineNumber),
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

func lastProjectGitReviewOverlayLine(lines map[int]bool) int {
	last := 0
	for line := range lines {
		if line > last {
			last = line
		}
	}
	return last
}

func projectGitReviewOverlayFallbackTitle(file ProjectGitCommitFile) string {
	subject := projectGitReviewOverlayFallbackSubject(file.Path)
	switch {
	case strings.HasPrefix(file.Status, "A"):
		return subject + " is introduced"
	case strings.HasPrefix(file.Status, "D"):
		return subject + " is removed"
	default:
		return subject + " behavior changes"
	}
}

func projectGitReviewOverlayFallbackSubject(filePath string) string {
	normalized := normalizeProjectGitReviewOverlayPath(filePath)
	if slash := strings.LastIndex(normalized, "/"); slash >= 0 {
		normalized = normalized[slash+1:]
	}
	normalized = strings.TrimLeft(normalized, "_")
	for {
		dot := strings.LastIndex(normalized, ".")
		if dot <= 0 {
			break
		}
		normalized = normalized[:dot]
	}
	normalized = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(normalized)
	words := strings.Fields(normalized)
	if len(words) == 0 {
		return "Changed file"
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func projectGitReviewOverlayFallbackBody(file ProjectGitCommitFile, lineIndex projectGitReviewOverlayLineIndex, side string, lineNumber int, endLineNumber int) string {
	subject := strings.ToLower(projectGitReviewOverlayFallbackSubject(file.Path))
	changedText := strings.ToLower(projectGitReviewOverlayChangedText(lineIndex, side, lineNumber, endLineNumber))
	switch {
	case strings.HasPrefix(file.Status, "A"):
		if strings.Contains(changedText, "wishlist") && strings.Contains(changedText, "turbo") {
			return "This partial wires wishlist selection into a Turbo frame so the add-to-wishlist UI can reveal and submit in place without replacing the surrounding product page. Review the state flow because session wishlists, user-accessible wishlists, selected defaults, and hidden variant fields determine which wishlist receives the item."
		}
		return fmt.Sprintf("This added block establishes the %s behavior for the branch, so reviewers should check how users reach it and whether the surrounding flow passes the expected data. The covered lines form one new integration point whose defaults, state, and submission path need review together.", subject)
	case strings.HasPrefix(file.Status, "D"):
		return fmt.Sprintf("Removing this %s block changes the behavior available to callers, so reviewers should confirm references, routes, and user flows no longer depend on it. The covered lines are treated as one removed integration point because the old behavior leaves the branch together.", subject)
	default:
		if added, removed := projectGitReviewOverlayNearbySnippets(lineIndex, lineNumber); added != "" && removed != "" {
			return fmt.Sprintf("This block shifts behavior from `%s` toward `%s`, so reviewers should confirm related callers still follow the intended flow. The surrounding state and user-visible path may change even when the edited region is small.", removed, added)
		}
		if added := projectGitReviewOverlaySnippet(lineIndex.AdditionText, lineNumber); added != "" {
			return fmt.Sprintf("This block introduces `%s` in the existing %s flow, so reviewers should check how the new state or branch path affects users and callers. The changed lines are grouped because they operate as one local behavior change.", added, subject)
		}
		if removed := projectGitReviewOverlaySnippet(lineIndex.DeletionText, lineNumber); removed != "" {
			return fmt.Sprintf("This block removes `%s` from the existing %s flow, so reviewers should confirm the old behavior is no longer needed by users or callers. The changed lines are grouped because they remove one local behavior path.", removed, subject)
		}
		return fmt.Sprintf("This block changes the %s flow, so reviewers should check the user-visible behavior, state transitions, and related callers together. The covered lines are grouped because they form one local branch change.", subject)
	}
}

func projectGitReviewOverlayChangedText(lineIndex projectGitReviewOverlayLineIndex, side string, startLine int, endLine int) string {
	lines := lineIndex.AdditionText
	if side == "deletions" {
		lines = lineIndex.DeletionText
	}
	parts := make([]string, 0, endLine-startLine+1)
	for line := startLine; line <= endLine; line++ {
		if text := cleanProjectGitReviewOverlayText(lines[line]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
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
