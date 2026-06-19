package http

import (
	"fmt"
	"strings"
)

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
