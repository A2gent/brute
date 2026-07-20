package claudecli

import (
	"bytes"
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
	Type            string           `json:"type"`
	Subtype         string           `json:"subtype"`
	Status          string           `json:"status"`
	IsError         bool             `json:"is_error"`
	Result          string           `json:"result"`
	Message         cliStreamMessage `json:"-"`
	MessageText     string           `json:"-"`
	Error           string           `json:"error"`
	SessionID       string           `json:"session_id"`
	StopReason      string           `json:"stop_reason"`
	TotalCostUSD    float64          `json:"total_cost_usd"`
	DurationMS      int64            `json:"duration_ms"`
	DurationAPIMS   int64            `json:"duration_api_ms"`
	NumTurns        int              `json:"num_turns"`
	Usage           json.RawMessage  `json:"usage"`
	CompactMetadata json.RawMessage  `json:"compact_metadata"`
	Event           cliStreamEvent   `json:"event"`
}

func (e *cliStreamEnvelope) UnmarshalJSON(data []byte) error {
	type envelopeFields struct {
		Type            string          `json:"type"`
		Subtype         string          `json:"subtype"`
		Status          string          `json:"status"`
		IsError         bool            `json:"is_error"`
		Result          string          `json:"result"`
		Message         json.RawMessage `json:"message"`
		Error           string          `json:"error"`
		SessionID       string          `json:"session_id"`
		StopReason      string          `json:"stop_reason"`
		TotalCostUSD    float64         `json:"total_cost_usd"`
		DurationMS      int64           `json:"duration_ms"`
		DurationAPIMS   int64           `json:"duration_api_ms"`
		NumTurns        int             `json:"num_turns"`
		Usage           json.RawMessage `json:"usage"`
		CompactMetadata json.RawMessage `json:"compact_metadata"`
		Event           cliStreamEvent  `json:"event"`
	}

	var raw envelopeFields
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	e.Type = raw.Type
	e.Subtype = raw.Subtype
	e.Status = raw.Status
	e.IsError = raw.IsError
	e.Result = raw.Result
	e.Error = raw.Error
	e.SessionID = raw.SessionID
	e.StopReason = raw.StopReason
	e.TotalCostUSD = raw.TotalCostUSD
	e.DurationMS = raw.DurationMS
	e.DurationAPIMS = raw.DurationAPIMS
	e.NumTurns = raw.NumTurns
	e.Usage = raw.Usage
	e.CompactMetadata = raw.CompactMetadata
	e.Event = raw.Event

	if len(raw.Message) == 0 {
		return nil
	}
	switch raw.Message[0] {
	case '"':
		return json.Unmarshal(raw.Message, &e.MessageText)
	case '{':
		return json.Unmarshal(raw.Message, &e.Message)
	default:
		return fmt.Errorf("unexpected message JSON type")
	}
}

type cliStreamEvent struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	Delta        cliStreamDelta        `json:"delta"`
	Message      cliStreamMessage      `json:"message"`
	Usage        json.RawMessage       `json:"usage"`
	ContentBlock cliStreamContentBlock `json:"content_block"`
}

type cliStreamContentBlock struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Thinking string          `json:"thinking"`
	Text     string          `json:"text"`
}

type cliStreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type cliStreamMessage struct {
	ID         string             `json:"id"`
	Role       string             `json:"role"`
	Model      string             `json:"model"`
	Content    []cliStreamContent `json:"content"`
	Usage      json.RawMessage    `json:"usage"`
	StopReason string             `json:"stop_reason"`
}

type cliStreamContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
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

func compactJSONRaw(raw json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return strings.TrimSpace(string(raw))
}

// toolResultContent extracts displayable tool output from CLI tool_result content.
// invalid is true only when raw is not valid JSON (should not happen for envelope content).
// Unsupported but syntactically valid shapes are returned as compact JSON in content.
func toolResultContent(raw json.RawMessage) (content string, invalid bool) {
	if len(raw) == 0 {
		return "", false
	}
	if !json.Valid(raw) {
		return "", true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return compactJSONRaw(raw), false
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", true
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type != "" && part.Type != "text" {
			return compactJSONRaw(raw), false
		}
		b.WriteString(part.Text)
	}
	return b.String(), false
}

func toolInputString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if string(raw) == "{}" || string(raw) == "null" {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return strings.TrimSpace(string(raw))
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
