package http

// Pure Telegram formatting, truncation, and sanitization helpers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

func telegramAgentPromptContext(userMessage string, metadata map[string]interface{}) string {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		return ""
	}
	msgType := "text"
	if metadata != nil {
		if raw, ok := metadata["inbound_message_type"].(string); ok && strings.TrimSpace(raw) != "" {
			msgType = strings.TrimSpace(raw)
		}
	}
	return fmt.Sprintf("[Inbound channel: Telegram | message type: %s]\n%s", msgType, userMessage)
}

func telegramPartsForSessionMessage(msg session.Message) []string {
	parts := make([]string, 0, 3)
	content := strings.TrimSpace(msg.Content)

	switch strings.ToLower(strings.TrimSpace(msg.Role)) {
	case "user":
		if content != "" {
			parts = append(parts, "You: "+content)
		}
	case "assistant":
		if content != "" {
			parts = append(parts, "Agent: "+content)
		}
		if len(msg.ToolCalls) > 0 {
			lines := make([]string, 0, len(msg.ToolCalls)+1)
			lines = append(lines, "Agent tool calls:")
			for _, tc := range msg.ToolCalls {
				name := strings.TrimSpace(tc.Name)
				if name == "" {
					name = "tool"
				}
				input := compactTelegramJSON(tc.Input)
				if input != "" {
					lines = append(lines, fmt.Sprintf("- %s %s", name, truncateRunes(input, 500)))
				} else {
					lines = append(lines, "- "+name)
				}
			}
			parts = append(parts, strings.Join(lines, "\n"))
		}
	case "tool":
		if content != "" {
			parts = append(parts, "Tool: "+content)
		}
	}

	if len(msg.ToolResults) > 0 {
		lines := make([]string, 0, len(msg.ToolResults)+1)
		lines = append(lines, "Tool results:")
		for _, tr := range msg.ToolResults {
			status := "ok"
			if tr.IsError {
				status = "error"
			}
			body := truncateRunes(strings.TrimSpace(tr.Content), 1200)
			if body == "" {
				lines = append(lines, fmt.Sprintf("- [%s]", status))
				continue
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", status, body))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}

	return parts
}

func compactTelegramJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(trimmed)); err == nil {
		return out.String()
	}
	return trimmed
}

func truncateRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func splitTelegramText(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{text}
	}

	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}

	parts := make([]string, 0, (len(runes)/maxRunes)+1)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, strings.TrimSpace(string(runes[start:end])))
	}
	return parts
}

func telegramInboundFailureReply(err error) string {
	base := "I couldn't process that request."
	if err == nil {
		return base + " Check integration and provider setup in WebApp."
	}

	msg := sanitizeTelegramError(err)
	if msg == "" {
		return base + " Check integration and provider setup in WebApp."
	}

	const maxErrChars = 350
	runes := []rune(msg)
	if len(runes) > maxErrChars {
		msg = string(runes[:maxErrChars]) + "..."
	}
	return fmt.Sprintf("%s %s", base, msg)
}

func sanitizeTelegramError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	return telegramBotTokenPattern.ReplaceAllString(text, "bot<redacted>")
}
