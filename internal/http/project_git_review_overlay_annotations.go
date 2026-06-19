package http

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

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
