package claudecli

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/llm"
	"testing"
)

func TestMCPBridgeToolLifecycleUsesCanonicalNames(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"mcp__a2gent__delegate_to_agent", "delegate_to_agent"},
		{"mcp__a2gent__delegate_to_external_agent", "delegate_to_external_agent"},
		{"mcp__other__delegate_to_agent", "mcp__other__delegate_to_agent"},
		{"Read", "Read"},
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/fallback", false: "/stream"}[fallback], func(t *testing.T) {
				var events []llm.StreamEvent
				p := newStreamProcessor(func(ev llm.StreamEvent) error { events = append(events, ev); return nil })
				var err error
				if fallback {
					err = p.handleEnvelope(cliStreamEnvelope{Type: "assistant", Message: cliStreamMessage{Content: []cliStreamContent{{Type: "tool_use", ID: "call-1", Name: tc.name, Input: json.RawMessage(`{"task":"review"}`)}}}})
				} else {
					err = p.handleContentBlockStart(cliStreamEvent{ContentBlock: cliStreamContentBlock{Type: "tool_use", ID: "call-1", Name: tc.name}})
					if err == nil {
						err = p.handleContentBlockStop(cliStreamEvent{})
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = p.emitToolOutput("call-1", "", json.RawMessage(`"review result"`), false); err != nil {
					t.Fatal(err)
				}
				if len(events) < 3 {
					t.Fatalf("missing lifecycle events: %+v", events)
				}
				for _, ev := range events {
					if ev.ToolCallName != tc.want {
						t.Errorf("name = %q want %q", ev.ToolCallName, tc.want)
					}
				}
			})
		}
	}
}
