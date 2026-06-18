package claudecli

import (
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

func claudeToolsArgs(request *llm.ChatRequest) (string, string, bool) {
	if request == nil {
		return "", "", true
	}
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		name := strings.TrimSpace(tool.Name)
		if name != "" {
			toolNames[name] = struct{}{}
		}
	}
	if len(toolNames) == 0 {
		return "", "", true
	}

	// Map A2gent tool availability to Claude Code's native tool names. We do not
	// include web/notification/sub-agent tools because Claude CLI cannot execute
	// A2gent server-backed integrations; those remain available through other
	// providers that support A2gent tool calls.
	allowed := make([]string, 0, 10)
	if hasAnyTool(toolNames, "bash") {
		allowed = append(allowed, "Bash")
	}
	if hasAnyTool(toolNames, "read", "grep", "glob", "find_files", "filter") {
		allowed = append(allowed, "Glob", "Grep", "LS", "Read")
	}
	if hasAnyTool(toolNames, "edit", "replace_lines", "insert_lines") {
		allowed = append(allowed, "Edit", "MultiEdit")
	}
	if hasAnyTool(toolNames, "write") {
		allowed = append(allowed, "Write")
	}
	if len(allowed) == 0 {
		return "", "", true
	}
	allowed = uniqueSorted(allowed)
	joined := strings.Join(allowed, ",")
	return joined, joined, true
}

func hasAnyTool(names map[string]struct{}, candidates ...string) bool {
	for _, candidate := range candidates {
		if _, ok := names[candidate]; ok {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	// Keep output deterministic for tests and easier CLI debugging.
	order := map[string]int{"Bash": 1, "Edit": 2, "Glob": 3, "Grep": 4, "LS": 5, "MultiEdit": 6, "Read": 7, "Write": 8}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if order[out[j]] < order[out[i]] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
