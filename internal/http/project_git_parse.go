package http

import (
	"sort"
	"strconv"
	"strings"
)

func parseGitAheadBehind(track string) (int, int) {
	trimmed := strings.TrimSpace(track)
	if trimmed == "" {
		return 0, 0
	}

	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")

	ahead := 0
	behind := 0
	parts := strings.Split(trimmed, ",")
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if strings.HasPrefix(item, "ahead ") {
			if value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(item, "ahead "))); err == nil {
				ahead = value
			}
		}
		if strings.HasPrefix(item, "behind ") {
			if value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(item, "behind "))); err == nil {
				behind = value
			}
		}
	}

	return ahead, behind
}

func parseProjectGitHistoryCommits(output string, currentBranch string) []ProjectGitHistoryCommit {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	commits := make([]ProjectGitHistoryCommit, 0, len(lines))

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\x1f")
		if len(parts) < 7 {
			continue
		}

		refs := parseProjectGitDecorations(parts[5])
		branch := pickProjectGitPrimaryRef(refs, currentBranch)

		parents := make([]string, 0)
		for _, parent := range strings.Fields(strings.TrimSpace(parts[6])) {
			if parent != "" {
				parents = append(parents, parent)
			}
		}

		commits = append(commits, ProjectGitHistoryCommit{
			Hash:       strings.TrimSpace(parts[0]),
			ShortHash:  strings.TrimSpace(parts[1]),
			Subject:    strings.TrimSpace(parts[2]),
			AuthorName: strings.TrimSpace(parts[3]),
			AuthoredAt: strings.TrimSpace(parts[4]),
			Refs:       refs,
			Parents:    parents,
			Branch:     branch,
		})
	}

	return commits
}

func parseProjectGitDecorations(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	segments := strings.Split(trimmed, ",")
	refs := make([]string, 0, len(segments))
	for _, segment := range segments {
		part := strings.TrimSpace(segment)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "HEAD -> ") {
			branch := strings.TrimSpace(strings.TrimPrefix(part, "HEAD -> "))
			if branch != "" {
				refs = append(refs, branch)
			}
			continue
		}
		if strings.HasPrefix(part, "tag: ") {
			tagName := strings.TrimSpace(strings.TrimPrefix(part, "tag: "))
			if tagName != "" {
				refs = append(refs, tagName)
			}
			continue
		}
		refs = append(refs, part)
	}

	return refs
}

func pickProjectGitPrimaryRef(refs []string, currentBranch string) string {
	trimmedCurrent := strings.TrimSpace(currentBranch)
	if trimmedCurrent != "" {
		for _, ref := range refs {
			if strings.TrimSpace(ref) == trimmedCurrent {
				return trimmedCurrent
			}
		}
	}
	for _, ref := range refs {
		trimmedRef := strings.TrimSpace(ref)
		if trimmedRef == "" {
			continue
		}
		if strings.HasPrefix(trimmedRef, "origin/") || strings.Contains(trimmedRef, "/") {
			return trimmedRef
		}
		return trimmedRef
	}
	return ""
}

func parseProjectGitCommitFileStatuses(output string) map[string]string {
	statuses := make(map[string]string)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\t")
		if len(parts) < 2 {
			continue
		}
		status := strings.TrimSpace(parts[0])
		if status == "" {
			continue
		}
		path := strings.TrimSpace(parts[len(parts)-1])
		if path == "" {
			continue
		}
		path = decodeGitPath(path)
		if path == "" {
			continue
		}
		statuses[path] = status
	}
	return statuses
}

func mergeProjectGitCommitFiles(statuses map[string]string, statsOutput string) []ProjectGitCommitFile {
	merged := make(map[string]*ProjectGitCommitFile, len(statuses))

	for path, status := range statuses {
		merged[path] = &ProjectGitCommitFile{
			Path:   path,
			Status: status,
		}
	}

	lines := strings.Split(statsOutput, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\t")
		if len(parts) < 3 {
			continue
		}

		path := strings.TrimSpace(parts[2])
		if path == "" {
			continue
		}
		path = normalizeGitNumstatPath(path)
		if path == "" {
			continue
		}

		file := merged[path]
		if file == nil {
			file = &ProjectGitCommitFile{
				Path:   path,
				Status: "M",
			}
			merged[path] = file
		}

		additionsRaw := strings.TrimSpace(parts[0])
		deletionsRaw := strings.TrimSpace(parts[1])
		if additionsRaw == "-" || deletionsRaw == "-" {
			file.Binary = true
			file.Additions = 0
			file.Deletions = 0
			continue
		}
		if additions, err := strconv.Atoi(additionsRaw); err == nil {
			file.Additions = additions
		}
		if deletions, err := strconv.Atoi(deletionsRaw); err == nil {
			file.Deletions = deletions
		}
	}

	files := make([]ProjectGitCommitFile, 0, len(merged))
	for _, file := range merged {
		files = append(files, *file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files
}

func normalizeGitNumstatPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "=>") {
		parts := strings.Split(trimmed, "=>")
		if len(parts) > 1 {
			candidate := strings.TrimSpace(parts[len(parts)-1])
			candidate = strings.TrimPrefix(candidate, "{")
			candidate = strings.TrimSuffix(candidate, "}")
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				return decodeGitPath(candidate)
			}
		}
	}
	return decodeGitPath(trimmed)
}

func parseGitPorcelain(output string) []ProjectGitChangedFile {
	lines := strings.Split(output, "\n")
	files := make([]ProjectGitChangedFile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || len(line) < 3 {
			continue
		}

		statusCode := line[:2]
		pathStart := 3
		// Be tolerant if a caller accidentally trimmed leading whitespace in a " M path" line.
		// In that case git line may look like "M path", where path starts at index 2.
		if len(line) > 2 && statusCode[1] == ' ' && line[2] != ' ' {
			pathStart = 2
		}
		if len(line) <= pathStart {
			continue
		}
		pathPart := strings.TrimSpace(line[pathStart:])
		if strings.Contains(pathPart, " -> ") {
			parts := strings.SplitN(pathPart, " -> ", 2)
			pathPart = strings.TrimSpace(parts[1])
		}
		if pathPart == "" {
			continue
		}
		pathPart = decodeGitPath(pathPart)

		indexStatus := string(statusCode[0])
		worktreeStatus := string(statusCode[1])
		if pathStart == 2 && statusCode[1] == ' ' {
			// Reconstruct original unstaged-only status from a trimmed " M path" line.
			indexStatus = " "
			worktreeStatus = string(statusCode[0])
		}
		untracked := statusCode == "??"
		staged := !untracked && indexStatus != " "
		// Git reports unresolved merge conflicts with at least one unmerged (U)
		// side, plus both-added/both-deleted combinations like AA/DD.
		hasConflict := indexStatus == "U" || worktreeStatus == "U" || statusCode == "AA" || statusCode == "DD"

		files = append(files, ProjectGitChangedFile{
			Path:           pathPart,
			Status:         strings.TrimSpace(statusCode),
			IndexStatus:    indexStatus,
			WorktreeStatus: worktreeStatus,
			Staged:         staged,
			Untracked:      untracked,
			HasConflict:    hasConflict,
		})
	}
	return files
}

func decodeGitPath(pathPart string) string {
	trimmed := strings.TrimSpace(pathPart)
	if trimmed == "" {
		return trimmed
	}

	// Git may return paths in C-style quoted format with octal escapes,
	// e.g. "00-\320\230...". Decode those so UI gets readable UTF-8 names.
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		if decoded, err := strconv.Unquote(trimmed); err == nil {
			return decoded
		}
	}

	// Fallback for edge cases where git produced escapes without outer quotes.
	if strings.Contains(trimmed, "\\") {
		quoted := "\"" + strings.ReplaceAll(trimmed, "\"", "\\\"") + "\""
		if decoded, err := strconv.Unquote(quoted); err == nil {
			return decoded
		}
	}

	return trimmed
}
