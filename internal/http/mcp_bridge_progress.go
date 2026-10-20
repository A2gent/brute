package http

import (
	"bytes"
	"encoding/json"
	"strings"
)

// resolveLiveRuntimeToolCall maps an MCP bridge tool invocation back to the CLI
// runtime tool_use that triggered it. MCP tools/call requests carry no tool_use id,
// so without this, progress from long tools (e.g. delegate_to_agent reporting the
// child session id) could not be attached to the tool card while it is running.
// It scans the in-flight turn buffer for a not yet finished call with the same name,
// preferring an exact input match and falling back to the only candidate.
func (s *Server) resolveLiveRuntimeToolCall(sessionID, toolName string, arguments json.RawMessage) (toolCallID string, turnID string) {
	toolName = strings.TrimSpace(toolName)
	if s == nil || sessionID == "" || toolName == "" {
		return "", ""
	}
	want := canonicalJSONString(arguments)

	s.sessionEventsMu.Lock()
	defer s.sessionEventsMu.Unlock()

	finished := map[string]bool{}
	seen := map[string]bool{}
	candidateID, candidateTurn, candidates := "", "", 0
	buf := s.liveTurnEvents[sessionID]
	for i := len(buf) - 1; i >= 0; i-- {
		ev := buf[i].event
		switch {
		case ev.Type == "tool_output" && ev.ToolResult != nil:
			finished[ev.ToolResult.ToolCallID] = true
		case ev.Type == "tool_input_completed" && ev.RuntimeTool != nil:
			rt := ev.RuntimeTool
			if rt.ID == "" || rt.Name != toolName || finished[rt.ID] || seen[rt.ID] {
				continue
			}
			seen[rt.ID] = true
			if want != "" && canonicalJSONString(json.RawMessage(rt.InputJSON)) == want {
				return rt.ID, ev.TurnID
			}
			candidateID, candidateTurn = rt.ID, ev.TurnID
			candidates++
		}
	}
	if candidates == 1 {
		return candidateID, candidateTurn
	}
	return "", ""
}

func canonicalJSONString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var value interface{}
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return ""
	}
	// encoding/json sorts map keys, so equal objects encode identically.
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
