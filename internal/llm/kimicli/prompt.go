package kimicli

import (
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

func buildSystemPrompt(systemPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return kimiCodePromptPrefix
	}
	return kimiCodePromptPrefix + "\n\n" + systemPrompt
}

func buildPrompt(request *llm.ChatRequest) string {
	if request == nil || len(request.Messages) == 0 {
		return "Continue."
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == "user" &&
		len(request.Messages[0].ToolCalls) == 0 && len(request.Messages[0].ToolResults) == 0 &&
		len(request.Messages[0].Images) == 0 {
		return strings.TrimSpace(request.Messages[0].Content)
	}

	var b strings.Builder
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
	b.WriteString(strings.ToUpper(role[:1]))
	if len(role) > 1 {
		b.WriteString(role[1:])
	}
	b.WriteString(":\n")
	if content := strings.TrimSpace(msg.Content); content != "" {
		b.WriteString(content)
		b.WriteString("\n")
	}
	for _, img := range msg.Images {
		label := strings.TrimSpace(img.Name)
		if label == "" {
			label = strings.TrimSpace(img.URL)
		}
		if label == "" {
			label = strings.TrimSpace(img.MediaType)
		}
		if label == "" {
			label = "inline image"
		}
		b.WriteString("[Image attachment omitted in Kimi CLI provider adapter: ")
		b.WriteString(label)
		b.WriteString("]\n")
	}
	for _, tc := range msg.ToolCalls {
		b.WriteString("[Tool call: ")
		b.WriteString(tc.Name)
		if tc.ID != "" {
			b.WriteString(" id=")
			b.WriteString(tc.ID)
		}
		b.WriteString("]\n")
		if input := strings.TrimSpace(tc.Input); input != "" {
			b.WriteString(input)
			b.WriteString("\n")
		}
	}
	for _, tr := range msg.ToolResults {
		b.WriteString("[Tool result")
		if tr.Name != "" {
			b.WriteString(": ")
			b.WriteString(tr.Name)
		}
		if tr.ToolCallID != "" {
			b.WriteString(" id=")
			b.WriteString(tr.ToolCallID)
		}
		if tr.IsError {
			b.WriteString(" error=true")
		}
		b.WriteString("]\n")
		if tr.Content != "" {
			b.WriteString(tr.Content)
			if !strings.HasSuffix(tr.Content, "\n") {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
}
