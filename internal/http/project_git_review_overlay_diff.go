package http

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

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
