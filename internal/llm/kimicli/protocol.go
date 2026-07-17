package kimicli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

type streamMessage struct {
	Role      string          `json:"role"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	SessionID string          `json:"session_id"`
}

func parseStreamLine(line string) (streamMessage, error) {
	var msg streamMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return streamMessage{}, fmt.Errorf("failed to parse Kimi CLI stream line: %w", err)
	}
	return msg, nil
}

func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if strings.TrimSpace(part.Type) != "text" {
			continue
		}
		if part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func cliErrorMessage(err error, stdout, stderr string) string {
	parts := make([]string, 0, 3)
	if msg := strings.TrimSpace(stderr); msg != "" {
		parts = append(parts, msg)
	}
	if msg := strings.TrimSpace(stdout); msg != "" {
		parts = append(parts, msg)
	}
	if err != nil {
		parts = append(parts, strings.TrimSpace(err.Error()))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type limitedBuffer struct {
	buf   strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.buf.Write(p)
		} else {
			_, _ = b.buf.Write(p[:remaining])
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

var _ = llm.StreamEvent{}
