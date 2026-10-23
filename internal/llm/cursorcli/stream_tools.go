package cursorcli

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

const maxToolOutputBytes = 16 * 1024

// toolEmitter maps Cursor stream-json `thinking` and `tool_call` events to the
// same llm.StreamEvent types claudecli emits, so Caesar shows tool activity
// and reasoning for Cursor sessions too.
type toolEmitter struct {
	emit    func(llm.StreamEvent) error
	started map[string]toolInfo
	next    int
}

type toolInfo struct {
	index int
	name  string
	input string
}

func newToolEmitter(onEvent func(llm.StreamEvent) error) *toolEmitter {
	return &toolEmitter{emit: onEvent, started: map[string]toolInfo{}}
}

func (t *toolEmitter) handle(event cliStreamEnvelope) error {
	if t.emit == nil {
		return nil
	}
	switch event.Type {
	case "thinking":
		if event.Subtype == "delta" && event.Text != "" {
			return t.emit(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: event.Text})
		}
	case "tool_call":
		return t.handleToolCall(event)
	}
	return nil
}

func (t *toolEmitter) handleToolCall(event cliStreamEnvelope) error {
	// tool_call payload is {"<kind>ToolCall": {"args": {...}, "result": {...}}}.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(event.ToolCall, &payload); err != nil {
		return nil
	}
	var kind string
	for k := range payload {
		if strings.HasSuffix(k, "ToolCall") {
			kind = k
			break
		}
	}
	if kind == "" || event.CallID == "" {
		return nil
	}
	var call struct {
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload[kind], &call); err != nil {
		return nil
	}
	info, ok := t.started[event.CallID]
	if !ok {
		name, input := cursorToolNameAndInput(kind, call.Args)
		info = toolInfo{index: t.next, name: name, input: input}
		t.next++
		t.started[event.CallID] = info
		if err := t.emit(llm.StreamEvent{
			Type:          llm.StreamEventToolStarted,
			ToolCallIndex: info.index,
			ToolCallID:    event.CallID,
			ToolCallName:  info.name,
		}); err != nil {
			return err
		}
	}
	if event.Subtype != "completed" {
		return nil
	}
	output, isErr := cursorToolOutput(call.Result)
	if err := t.emit(llm.StreamEvent{
		Type:         llm.StreamEventToolOutput,
		ToolCallID:   event.CallID,
		ToolCallName: info.name,
		ToolOutput:   output,
		ToolIsError:  isErr,
	}); err != nil {
		return err
	}
	return t.emit(llm.StreamEvent{
		Type:           llm.StreamEventToolCompleted,
		ToolCallIndex:  info.index,
		ToolCallID:     event.CallID,
		ToolCallName:   info.name,
		ToolInputDelta: info.input,
	})
}

// cursorToolNameAndInput turns "readToolCall" into "read". MCP calls carry the
// real tool name in args, so prefer it and unwrap the nested args as input.
func cursorToolNameAndInput(kind string, args json.RawMessage) (string, string) {
	name := strings.TrimSuffix(kind, "ToolCall")
	input := string(args)
	if name == "mcp" {
		var m struct {
			Name     string          `json:"name"`
			ToolName string          `json:"toolName"`
			Args     json.RawMessage `json:"args"`
		}
		if json.Unmarshal(args, &m) == nil {
			if n := firstNonEmpty(m.ToolName, m.Name); n != "" {
				name = strings.TrimPrefix(n, "mcp__a2gent__")
			}
			if len(m.Args) > 0 {
				input = string(m.Args)
			}
		}
	}
	return name, input
}

// cursorToolOutput extracts readable output from result.success (shell has
// stdout/stderr) or result.error; other tools fall back to the raw JSON.
func cursorToolOutput(raw json.RawMessage) (string, bool) {
	var res struct {
		Success json.RawMessage `json:"success"`
		Error   struct {
			ErrorMessage string `json:"errorMessage"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return truncateToolOutput(string(raw)), false
	}
	if res.Error.ErrorMessage != "" {
		return truncateToolOutput(res.Error.ErrorMessage), true
	}
	var sh struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode *int   `json:"exitCode"`
	}
	if json.Unmarshal(res.Success, &sh) == nil && sh.ExitCode != nil {
		return truncateToolOutput(sh.Stdout + sh.Stderr), *sh.ExitCode != 0
	}
	return truncateToolOutput(string(res.Success)), false
}

func truncateToolOutput(s string) string {
	if len(s) > maxToolOutputBytes {
		return s[:maxToolOutputBytes] + "\n...[truncated]"
	}
	return s
}
