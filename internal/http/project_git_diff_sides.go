package http

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const maxGitDiffFileSideBytes = 1 << 20

func parseGitDiffPaths(preview string) (oldPath string, newPath string) {
	for _, line := range strings.Split(preview, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		left, right, ok := splitGitDiffPathPair(strings.TrimPrefix(line, "diff --git "))
		if !ok {
			return "", ""
		}
		return stripGitDiffPathPrefix(left), stripGitDiffPathPrefix(right)
	}
	return "", ""
}

func splitGitDiffPathPair(rest string) (string, string, bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	if strings.HasPrefix(rest, `"`) {
		left, remainder, ok := readQuotedGitPath(rest)
		if !ok {
			return "", "", false
		}
		remainder = strings.TrimSpace(remainder)
		if strings.HasPrefix(remainder, `"`) {
			right, _, ok := readQuotedGitPath(remainder)
			return left, right, ok
		}
		right, _, _ := strings.Cut(remainder, " ")
		return left, right, right != ""
	}
	left, right, ok := strings.Cut(rest, " ")
	return left, right, ok && left != "" && right != ""
}

func readQuotedGitPath(input string) (string, string, bool) {
	if !strings.HasPrefix(input, `"`) {
		return "", input, false
	}
	var builder strings.Builder
	escaped := false
	for index, char := range input[1:] {
		if escaped {
			builder.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			return builder.String(), input[index+2:], true
		}
		builder.WriteRune(char)
	}
	return "", input, false
}

func stripGitDiffPathPrefix(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	return path
}

func loadGitDiffFileSides(repoRoot, mergeBase, preview, fallbackPath string) (oldContent *string, newContent *string) {
	oldPath, newPath := parseGitDiffPaths(preview)
	if oldPath == "" {
		oldPath = fallbackPath
	}
	if newPath == "" {
		newPath = fallbackPath
	}

	oldSide, oldOK := gitBlobAtRef(repoRoot, mergeBase, oldPath)
	newSide, newOK := gitBlobAtRef(repoRoot, "HEAD", newPath)
	// WHY: @pierre/diffs indexes both sides when expanding hunks. A missing
	// new/deleted blob would become a dummy empty line and break that lookup.
	if !oldOK || !newOK {
		return nil, nil
	}
	return &oldSide, &newSide
}

func gitBlobAtRef(repoRoot, ref, path string) (string, bool) {
	path = strings.TrimSpace(path)
	ref = strings.TrimSpace(ref)
	if path == "" || ref == "" || strings.Contains(path, "\x00") || strings.Contains(ref, ":") {
		return "", false
	}

	cmd := gitCommand(repoRoot, "cat-file", "-p", ref+":"+path)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	if len(output) > maxGitDiffFileSideBytes || bytes.IndexByte(output, 0) >= 0 || !utf8.Valid(output) {
		return "", false
	}
	return string(output), true
}
