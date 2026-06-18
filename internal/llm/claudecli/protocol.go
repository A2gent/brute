package claudecli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

type cliResult struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	IsError        bool            `json:"is_error"`
	Result         string          `json:"result"`
	Message        string          `json:"message"`
	Error          string          `json:"error"`
	SessionID      string          `json:"session_id"`
	StopReason     string          `json:"stop_reason"`
	TotalCostUSD   float64         `json:"total_cost_usd"`
	DurationMS     int64           `json:"duration_ms"`
	DurationAPIMS  int64           `json:"duration_api_ms"`
	NumTurns       int             `json:"num_turns"`
	Usage          json.RawMessage `json:"usage"`
	PermissionMode string          `json:"permission_mode"`
}

type cliStreamEnvelope struct {
	Type       string           `json:"type"`
	Subtype    string           `json:"subtype"`
	IsError    bool             `json:"is_error"`
	Result     string           `json:"result"`
	Message    cliStreamMessage `json:"message"`
	Error      string           `json:"error"`
	SessionID  string           `json:"session_id"`
	StopReason string           `json:"stop_reason"`
	Usage      json.RawMessage  `json:"usage"`
	Event      cliStreamEvent   `json:"event"`
}

type cliStreamEvent struct {
	Type    string           `json:"type"`
	Delta   cliStreamDelta   `json:"delta"`
	Message cliStreamMessage `json:"message"`
	Usage   json.RawMessage  `json:"usage"`
}

type cliStreamDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	StopReason string `json:"stop_reason"`
}

type cliStreamMessage struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	Content    []cliStreamContent `json:"content"`
	Usage      json.RawMessage    `json:"usage"`
	StopReason string             `json:"stop_reason"`
}

type cliStreamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseCLIResult(stdout string) (cliResult, string, error) {
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return cliResult{}, "", fmt.Errorf("Claude CLI returned empty output")
	}

	var parsed cliResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return cliResult{Result: raw}, raw, nil
	}
	return parsed, raw, nil
}

func parseCLIStreamEnvelope(line string) (cliStreamEnvelope, error) {
	var event cliStreamEnvelope
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return cliStreamEnvelope{}, fmt.Errorf("failed to parse Claude CLI stream line: %w", err)
	}
	return event, nil
}

func streamMessageText(message cliStreamMessage) string {
	var b strings.Builder
	for _, item := range message.Content {
		if item.Type == "text" && item.Text != "" {
			b.WriteString(item.Text)
		}
	}
	return b.String()
}

func mergeUsage(current, next llm.TokenUsage) llm.TokenUsage {
	if next.InputTokens != 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		current.OutputTokens = next.OutputTokens
	}
	if next.CachedInputTokens != 0 {
		current.CachedInputTokens = next.CachedInputTokens
	}
	if next.ReasoningTokens != 0 {
		current.ReasoningTokens = next.ReasoningTokens
	}
	return current
}

func usageFromRaw(raw json.RawMessage) llm.TokenUsage {
	if len(raw) == 0 {
		return llm.TokenUsage{}
	}
	var values map[string]interface{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return llm.TokenUsage{}
	}
	return llm.TokenUsage{
		InputTokens:       intFromMap(values, "input_tokens"),
		OutputTokens:      intFromMap(values, "output_tokens"),
		CachedInputTokens: intFromMap(values, "cache_read_input_tokens") + intFromMap(values, "cache_creation_input_tokens") + intFromMap(values, "cached_input_tokens"),
		ReasoningTokens:   intFromMap(values, "reasoning_tokens"),
	}
}

func intFromMap(values map[string]interface{}, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(value))
		return n
	default:
		return 0
	}
}
