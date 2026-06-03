package tui

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/session"
	"github.com/charmbracelet/lipgloss"
	"path/filepath"
	"strings"
)

const asciiArt = `
         █████╗ ██████╗     ██████╗ ██████╗ ██╗   ██╗████████╗███████╗
        ██╔══██╗╚════██╗    ██╔══██╗██╔══██╗██║   ██║   ██║   ██╔════╝
        ███████║ █████╔╝    ██████╔╝██████╔╝██║   ██║   ██║   █████╗  
        ██╔══██║██╔═══╝     ██╔══██╗██╔══██╗██║   ██║   ██║   ██╔══╝  
        ██║  ██║███████╗    ██████╔╝██║  ██║╚██████╔╝   ██║   ███████╗
        ╚═╝  ╚═╝╚══════╝    ╚═════╝ ╚═╝  ╚═╝ ╚═════╝    ╚═╝   ╚══════╝
`

// Tool icons for visual distinction in the TUI
var toolIcons = map[string]string{
	"bash":          "", // Terminal icon
	"read":          "", // File read icon
	"write":         "", // File write icon
	"edit":          "", // Edit icon
	"replace_lines": "",
	"glob":          "", // Search files icon
	"find_files":    "",
	"grep":          "", // Search content icon
	"task":          "", // Sub-agent icon
}

// getToolIcon returns the icon for a tool, or a default arrow
func getToolIcon(toolName string) string {
	if icon, ok := toolIcons[toolName]; ok {
		return icon
	}
	return "" // Default icon
}

// getToolStyle returns the style for a tool
func getToolStyle(toolName string) lipgloss.Style {
	switch toolName {
	case "bash":
		return toolBashStyle
	case "read":
		return toolReadStyle
	case "write":
		return toolWriteStyle
	case "edit":
		return toolEditStyle
	case "replace_lines":
		return toolEditStyle
	case "glob":
		return toolGlobStyle
	case "find_files":
		return toolGlobStyle
	case "grep":
		return toolGrepStyle
	case "task":
		return toolTaskStyle
	default:
		return toolStyle
	}
}

// ToolCallDisplay holds parsed tool call info for display
type ToolCallDisplay struct {
	Name    string
	Icon    string
	Summary string
	Details []string
}

// parseToolCall extracts display info from a tool call
func parseToolCall(tc session.ToolCall, maxWidth int) ToolCallDisplay {
	display := ToolCallDisplay{
		Name: tc.Name,
		Icon: getToolIcon(tc.Name),
	}

	// Parse the input JSON to extract relevant info
	var input map[string]interface{}
	if err := json.Unmarshal(tc.Input, &input); err != nil {
		display.Summary = tc.Name
		return display
	}

	switch tc.Name {
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			// Truncate command if too long
			cmd = truncateLine(cmd, maxWidth-10)
			display.Summary = cmd
		}
		if workdir, ok := input["workdir"].(string); ok && workdir != "" {
			display.Details = append(display.Details, fmt.Sprintf("workdir: %s", workdir))
		}

	case "read":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}

	case "write":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		if content, ok := input["content"].(string); ok {
			lines := strings.Count(content, "\n") + 1
			display.Details = append(display.Details, fmt.Sprintf("%d lines", lines))
		}

	case "edit":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		// Add diff preview
		if oldStr, ok := input["old_string"].(string); ok {
			if newStr, ok := input["new_string"].(string); ok {
				display.Details = append(display.Details, formatDiff(oldStr, newStr, maxWidth-4))
			}
		}

	case "replace_lines":
		if path, ok := input["path"].(string); ok {
			display.Summary = shortenPath(path, maxWidth-10)
		}
		start, hasStart := input["start_line"].(float64)
		end, hasEnd := input["end_line"].(float64)
		if hasStart && hasEnd {
			display.Details = append(display.Details, fmt.Sprintf("lines %.0f-%.0f", start, end))
		}

	case "glob":
		if pattern, ok := input["pattern"].(string); ok {
			display.Summary = pattern
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "find_files":
		if pattern, ok := input["pattern"].(string); ok && pattern != "" {
			display.Summary = pattern
		} else {
			display.Summary = "**/*"
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "grep":
		if pattern, ok := input["pattern"].(string); ok {
			display.Summary = pattern
		}
		if path, ok := input["path"].(string); ok && path != "" {
			display.Details = append(display.Details, fmt.Sprintf("in: %s", shortenPath(path, maxWidth-8)))
		}

	case "task":
		if desc, ok := input["description"].(string); ok {
			display.Summary = desc
		}
		if agentType, ok := input["subagent_type"].(string); ok {
			display.Details = append(display.Details, fmt.Sprintf("agent: %s", agentType))
		}

	default:
		display.Summary = tc.Name
	}

	return display
}

// shortenPath shortens a path to fit within maxLen
func shortenPath(path string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	pathRunes := []rune(path)
	if len(pathRunes) <= maxLen {
		return path
	}
	if maxLen <= 3 {
		return string(pathRunes[:maxLen])
	}
	// Try to show the filename and as much of the path as possible
	base := filepath.Base(path)
	baseRunes := []rune(base)
	if len(baseRunes) >= maxLen-3 {
		return string(baseRunes[:maxLen-3]) + "..."
	}
	remaining := maxLen - len(baseRunes) - 4 // for ".../"
	if remaining <= 0 {
		return base
	}
	dir := filepath.Dir(path)
	dirRunes := []rune(dir)
	if len(dirRunes) > remaining {
		dir = "..." + string(dirRunes[len(dirRunes)-remaining:])
	}
	return dir + "/" + base
}

// findToolNameByCallID finds the tool name for a given tool call ID
func findToolNameByCallID(toolCalls []session.ToolCall, callID string) string {
	for _, tc := range toolCalls {
		if tc.ID == callID {
			return tc.Name
		}
	}
	return "tool"
}

// formatDiff creates a simple diff display
func formatDiff(oldStr, newStr string, maxWidth int) string {
	var sb strings.Builder

	// Split into lines for comparison
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	// Show at most 5 lines of diff
	maxLines := 5

	// Show removed lines (up to maxLines/2)
	showCount := 0
	for i, line := range oldLines {
		if showCount >= (maxLines+1)/2 {
			if i < len(oldLines)-1 {
				sb.WriteString(diffRemoveStyle.Render(fmt.Sprintf("    ... (%d more removed)", len(oldLines)-i)))
				sb.WriteString("\n")
			}
			break
		}
		line = strings.TrimRight(line, " \t")
		line = truncateLine(line, maxWidth-6)
		sb.WriteString(diffRemoveStyle.Render(fmt.Sprintf("    - %s", line)))
		sb.WriteString("\n")
		showCount++
	}

	// Show added lines (up to maxLines/2)
	showCount = 0
	for i, line := range newLines {
		if showCount >= maxLines/2 {
			if i < len(newLines)-1 {
				sb.WriteString(diffAddStyle.Render(fmt.Sprintf("    ... (%d more added)", len(newLines)-i)))
				sb.WriteString("\n")
			}
			break
		}
		line = strings.TrimRight(line, " \t")
		line = truncateLine(line, maxWidth-6)
		sb.WriteString(diffAddStyle.Render(fmt.Sprintf("    + %s", line)))
		sb.WriteString("\n")
		showCount++
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

// Message types for the tea program
