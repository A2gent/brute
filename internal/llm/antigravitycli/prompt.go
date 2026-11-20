package antigravitycli

import (
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

const promptPrefix = "You are running through Antigravity CLI. Use Antigravity's native tools directly when you need to inspect or modify files, then return the final answer to A2gent. A2gent-only tools such as question, tasks, suggest_session and suggest_git_commit are not available in this mode. Do not print JSON tool calls for A2gent to execute."

func buildPrompt(request *llm.ChatRequest) string {
	var b strings.Builder
	b.WriteString(promptPrefix)
	if system := strings.TrimSpace(request.SystemPrompt); system != "" {
		b.WriteString("\n\n")
		b.WriteString(system)
	}
	b.WriteString("\n\n")
	if len(request.Messages) == 0 {
		b.WriteString("Continue.")
		return b.String()
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == "user" && len(request.Messages[0].ToolResults) == 0 {
		b.WriteString(strings.TrimSpace(request.Messages[0].Content))
		return strings.TrimSpace(b.String())
	}
	b.WriteString("Continue the following A2gent conversation. Treat the final user message as the active request.\n\n")
	for _, msg := range request.Messages {
		writeMessage(&b, msg)
	}
	return strings.TrimSpace(b.String())
}

func writeMessage(b *strings.Builder, msg llm.Message) {
	role := strings.TrimSpace(msg.Role)
	if role == "" {
		role = "message"
	}
	b.WriteString(strings.ToUpper(role[:1]) + role[1:] + ":\n")
	if content := strings.TrimSpace(msg.Content); content != "" {
		b.WriteString(content + "\n")
	}
	for _, img := range msg.Images {
		b.WriteString("[Image attachment omitted: " + firstNonEmpty(img.Name, img.URL, img.MediaType, "inline image") + "]\n")
	}
	for _, tc := range msg.ToolCalls {
		b.WriteString("[Tool call: " + tc.Name)
		if tc.ID != "" {
			b.WriteString(" id=" + tc.ID)
		}
		b.WriteString("]\n")
		if input := strings.TrimSpace(tc.Input); input != "" {
			b.WriteString(input + "\n")
		}
	}
	for _, tr := range msg.ToolResults {
		b.WriteString("[Tool result")
		if tr.Name != "" {
			b.WriteString(": " + tr.Name)
		}
		if tr.ToolCallID != "" {
			b.WriteString(" id=" + tr.ToolCallID)
		}
		if tr.IsError {
			b.WriteString(" error=true")
		}
		b.WriteString("]\n")
		if content := strings.TrimSpace(tr.Content); content != "" {
			b.WriteString(content + "\n")
		}
	}
	b.WriteString("\n")
}
