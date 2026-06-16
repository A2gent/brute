package tui

import (
	"fmt"
	"github.com/A2gent/brute/internal/session"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

func (m Model) renderMessages() string {
	var sb strings.Builder

	for i, msg := range m.messages {
		var prevMsg *message
		if i > 0 {
			prevMsg = &m.messages[i-1]
		}
		sb.WriteString(m.renderMessageWithContext(msg, prevMsg))
		sb.WriteString("\n\n")
	}

	return sb.String()
}

func isCompactionMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata["context_compaction"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		normalized := strings.TrimSpace(strings.ToLower(v))
		return normalized == "true" || normalized == "1" || normalized == "yes"
	default:
		return false
	}
}

// renderMessageWithContext renders a message with context from previous message
func (m Model) renderMessageWithContext(msg message, prevMsg *message) string {
	var sb strings.Builder

	// Timestamp
	timestamp := timestampStyle.Render(msg.timestamp.Format("15:04:05"))

	// Calculate wrap width (leave some margin)
	wrapWidth := m.width - 4
	if wrapWidth < 20 {
		wrapWidth = 20
	}

	switch msg.role {
	case "user":
		header := userStyle.Render("You")
		indicator := sentStyle.Render(" ✓")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap and render user content with navy background
		wrapped := wrapText(msg.content, wrapWidth-2) // -2 for padding
		content := userContentStyle.Width(wrapWidth).Render(wrapped)
		sb.WriteString(content)

	case "assistant":
		header := assistantStyle.Render("Assistant")
		indicator := receivedStyle.Render(" ⬇")
		contentStyle := assistantContentStyle
		if isCompactionMetadata(msg.metadata) {
			header = compactionStyle.Render("Compaction")
			indicator = ""
			contentStyle = compactionStyle
		}
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap assistant content
		wrapped := wrapText(msg.content, wrapWidth)
		sb.WriteString(contentStyle.Render(wrapped))

		// Render tool calls with icons and details
		for _, tc := range msg.toolCalls {
			display := parseToolCall(tc, wrapWidth)
			style := getToolStyle(tc.Name)

			// Tool header with icon and name
			toolHeader := style.Render(fmt.Sprintf("  %s %s", display.Icon, tc.Name))
			sb.WriteString("\n" + toolHeader)

			// Tool summary (command, path, pattern, etc.)
			if display.Summary != "" {
				summaryLine := toolResultStyle.Render(fmt.Sprintf("    %s", display.Summary))
				sb.WriteString("\n" + summaryLine)
			}

			// Additional details (diff, workdir, etc.)
			for _, detail := range display.Details {
				sb.WriteString("\n" + detail)
			}
		}

	case "tool":
		header := toolResultStyle.Render("Tool Results")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))

		// Get tool calls from previous assistant message (if available)
		var prevToolCalls []session.ToolCall
		if prevMsg != nil && prevMsg.role == "assistant" {
			prevToolCalls = prevMsg.toolCalls
		}

		for _, tr := range msg.toolResults {
			// Find the matching tool call to get the tool name
			toolName := findToolNameByCallID(prevToolCalls, tr.ToolCallID)
			icon := getToolIcon(toolName)
			style := getToolStyle(toolName)

			var statusIcon string
			var statusStyle lipgloss.Style
			if tr.IsError {
				statusIcon = ""
				statusStyle = errorStyle
			} else {
				statusIcon = ""
				statusStyle = style
			}

			// Format the result with icon and status
			resultHeader := statusStyle.Render(fmt.Sprintf("  %s %s %s", icon, toolName, statusIcon))
			sb.WriteString(resultHeader + "\n")

			// Show content preview (truncated)
			content := tr.Content
			if len(content) > 0 {
				// Limit to first few lines
				lines := strings.SplitN(content, "\n", 6)
				for i, line := range lines {
					if i >= 5 {
						remaining := len(strings.Split(content, "\n")) - 5
						if remaining > 0 {
							sb.WriteString(toolResultStyle.Render(fmt.Sprintf("    ... (%d more lines)", remaining)) + "\n")
						}
						break
					}
					line = strings.TrimRight(line, " \t\r")
					line = truncateLine(line, m.width-8)
					sb.WriteString(toolResultStyle.Render(fmt.Sprintf("    %s", line)) + "\n")
				}
			}
		}

	case "error":
		header := errorStyle.Render("Error")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))
		sb.WriteString(errorStyle.Render(msg.content))

	case "queued":
		header := queuedStyle.Render("You (queued)")
		indicator := queuedStyle.Render(" ⏳")
		sb.WriteString(fmt.Sprintf("%s %s%s\n", timestamp, header, indicator))
		// Wrap and render queued content with gray background
		wrapped := wrapText(msg.content, wrapWidth-2)
		content := queuedContentStyle.Width(wrapWidth).Render(wrapped)
		sb.WriteString(content)

	case "system":
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			Render("System")
		sb.WriteString(fmt.Sprintf("%s %s\n", timestamp, header))
		wrapped := wrapText(msg.content, wrapWidth)
		sb.WriteString(wrapped)
	}

	return sb.String()
}

// handleUserInput processes user input and starts the agent
