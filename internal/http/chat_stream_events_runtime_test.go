package http

import (
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

func TestAgentEventToStreamEventMapsRuntimeEvents(t *testing.T) {
	server := &Server{}
	sess := &session.Session{}

	tests := []struct {
		name string
		ev   agent.Event
		want ChatStreamEvent
	}{
		{
			name: "reasoning_delta",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:           llm.StreamEventReasoningDelta,
					ReasoningDelta: "Checking",
				},
			},
			want: ChatStreamEvent{Type: "reasoning_delta", Step: 2, Delta: "Checking"},
		},
		{
			name: "tool_started",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:          llm.StreamEventToolStarted,
					ToolCallID:    "toolu_1",
					ToolCallName:  "Read",
					ToolCallIndex: 2,
				},
			},
			want: ChatStreamEvent{
				Type: "tool_started",
				Step: 2,
				RuntimeTool: &StreamRuntimeToolEvent{
					ID: "toolu_1", Name: "Read", Index: 2,
				},
			},
		},
		{
			name: "tool_updated",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:           llm.StreamEventToolUpdated,
					ToolCallID:     "toolu_1",
					ToolInputDelta: `{"file_path":"foo.go"}`,
				},
			},
			want: ChatStreamEvent{
				Type: "tool_updated",
				Step: 2,
				RuntimeTool: &StreamRuntimeToolEvent{
					ID: "toolu_1", InputJSON: `{"file_path":"foo.go"}`,
				},
			},
		},
		{
			name: "tool_input_completed",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:           llm.StreamEventToolInputCompleted,
					ToolCallID:     "toolu_1",
					ToolCallName:   "Read",
					ToolInputDelta: `{"file_path":"foo.go"}`,
				},
			},
			want: ChatStreamEvent{
				Type: "tool_input_completed",
				Step: 2,
				RuntimeTool: &StreamRuntimeToolEvent{
					ID: "toolu_1", Name: "Read", InputJSON: `{"file_path":"foo.go"}`,
				},
			},
		},
		{
			name: "tool_completed",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:           llm.StreamEventToolCompleted,
					ToolCallID:     "toolu_1",
					ToolCallName:   "Read",
					ToolInputDelta: `{"file_path":"foo.go"}`,
				},
			},
			want: ChatStreamEvent{
				Type: "tool_completed",
				Step: 2,
				RuntimeTool: &StreamRuntimeToolEvent{
					ID: "toolu_1", Name: "Read", InputJSON: `{"file_path":"foo.go"}`,
				},
			},
		},
		{
			name: "tool_output",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:         llm.StreamEventToolOutput,
					ToolCallID:   "toolu_1",
					ToolCallName: "Read",
					ToolOutput:   "package main\n",
				},
			},
			want: ChatStreamEvent{
				Type: "tool_output",
				Step: 2,
				ToolResult: &StreamToolResultEvent{
					ToolCallID: "toolu_1",
					Name:       "Read",
					Content:    "package main\n",
				},
			},
		},
		{
			name: "cost",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:          llm.StreamEventCost,
					TotalCostUSD:  0.0123,
					DurationMS:    4500,
					DurationAPIMS: 4100,
					NumTurns:      1,
				},
			},
			want: ChatStreamEvent{
				Type: "cost",
				Step: 2,
				Cost: &StreamRuntimeCostEvent{
					TotalCostUSD: 0.0123, DurationMS: 4500, DurationAPIMS: 4100, NumTurns: 1,
				},
			},
		},
		{
			name: "runtime_warning",
			ev: agent.Event{
				Type: EventLLMRuntimeAlias(),
				Step: 2,
				Runtime: &llm.StreamEvent{
					Type:           llm.StreamEventRuntimeWarning,
					RuntimeStatus:  "compacting",
					RuntimeWarning: "compacting",
				},
			},
			want: ChatStreamEvent{
				Type: "runtime_warning",
				Step: 2,
				RuntimeWarning: &StreamRuntimeWarningPayload{
					Status: "compacting", Message: "compacting",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := server.agentEventToStreamEvent(sess, config.ProviderAnthropic, tc.ev)
			if !ok {
				t.Fatal("expected mapped stream event")
			}
			if got.Type != tc.want.Type || got.Step != tc.want.Step || got.Delta != tc.want.Delta {
				t.Fatalf("base fields = %#v, want %#v", got, tc.want)
			}
			if tc.want.RuntimeTool != nil {
				if got.RuntimeTool == nil {
					t.Fatal("expected runtime_tool payload")
				}
				if *got.RuntimeTool != *tc.want.RuntimeTool {
					t.Fatalf("runtime_tool = %#v, want %#v", got.RuntimeTool, tc.want.RuntimeTool)
				}
			}
			if tc.want.ToolResult != nil {
				if got.ToolResult == nil || *got.ToolResult != *tc.want.ToolResult {
					t.Fatalf("tool_result = %#v, want %#v", got.ToolResult, tc.want.ToolResult)
				}
			}
			if tc.want.Cost != nil {
				if got.Cost == nil || *got.Cost != *tc.want.Cost {
					t.Fatalf("cost = %#v, want %#v", got.Cost, tc.want.Cost)
				}
			}
			if tc.want.RuntimeWarning != nil {
				if got.RuntimeWarning == nil || *got.RuntimeWarning != *tc.want.RuntimeWarning {
					t.Fatalf("runtime_warning = %#v, want %#v", got.RuntimeWarning, tc.want.RuntimeWarning)
				}
			}

			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal stream event: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("unmarshal stream event: %v", err)
			}
			if _, ok := payload["type"]; !ok {
				t.Fatalf("missing type in JSON: %s", string(raw))
			}
			if tc.want.RuntimeTool != nil {
				if _, ok := payload["runtime_tool"]; !ok {
					t.Fatalf("missing runtime_tool in JSON: %s", string(raw))
				}
			}
			if tc.want.Cost != nil {
				if _, ok := payload["cost"]; !ok {
					t.Fatalf("missing cost in JSON: %s", string(raw))
				}
			}
			if tc.want.RuntimeWarning != nil {
				if _, ok := payload["runtime_warning"]; !ok {
					t.Fatalf("missing runtime_warning in JSON: %s", string(raw))
				}
			}
		})
	}
}

func TestAgentEventToStreamEventToolCompletedMutualExclusion(t *testing.T) {
	server := &Server{}
	sess := &session.Session{Status: session.StatusRunning}

	agentCompleted, ok := server.agentEventToStreamEvent(sess, config.ProviderAnthropic, agent.Event{
		Type: agent.EventToolCompleted,
		Step: 3,
	})
	if !ok {
		t.Fatal("expected agent tool_completed mapping")
	}
	if agentCompleted.Type != "tool_completed" || agentCompleted.Step != 3 {
		t.Fatalf("agent tool_completed = %#v", agentCompleted)
	}
	if agentCompleted.Status != string(session.StatusRunning) {
		t.Fatalf("agent tool_completed status = %q, want running", agentCompleted.Status)
	}
	if agentCompleted.RuntimeTool != nil {
		t.Fatalf("agent tool_completed must not include runtime_tool: %#v", agentCompleted.RuntimeTool)
	}

	agentRaw, err := json.Marshal(agentCompleted)
	if err != nil {
		t.Fatalf("marshal agent tool_completed: %v", err)
	}
	var agentPayload map[string]json.RawMessage
	if err := json.Unmarshal(agentRaw, &agentPayload); err != nil {
		t.Fatalf("unmarshal agent tool_completed: %v", err)
	}
	if _, ok := agentPayload["status"]; !ok {
		t.Fatalf("agent tool_completed missing status: %s", string(agentRaw))
	}
	if _, ok := agentPayload["runtime_tool"]; ok {
		t.Fatalf("agent tool_completed must not marshal runtime_tool: %s", string(agentRaw))
	}

	runtimeCompleted, ok := server.agentEventToStreamEvent(sess, config.ProviderAnthropic, agent.Event{
		Type: agent.EventLLMRuntime,
		Step: 3,
		Runtime: &llm.StreamEvent{
			Type:           llm.StreamEventToolCompleted,
			ToolCallID:     "toolu_1",
			ToolCallName:   "Read",
			ToolInputDelta: `{"file_path":"foo.go"}`,
		},
	})
	if !ok {
		t.Fatal("expected runtime tool_completed mapping")
	}
	if runtimeCompleted.Type != "tool_completed" || runtimeCompleted.RuntimeTool == nil {
		t.Fatalf("runtime tool_completed = %#v", runtimeCompleted)
	}
	if runtimeCompleted.Status != "" {
		t.Fatalf("runtime tool_completed must not include status: %q", runtimeCompleted.Status)
	}
	if runtimeCompleted.Message != nil {
		t.Fatal("runtime tool_completed must not include message")
	}

	runtimeRaw, err := json.Marshal(runtimeCompleted)
	if err != nil {
		t.Fatalf("marshal runtime tool_completed: %v", err)
	}
	var runtimePayload map[string]json.RawMessage
	if err := json.Unmarshal(runtimeRaw, &runtimePayload); err != nil {
		t.Fatalf("unmarshal runtime tool_completed: %v", err)
	}
	if _, ok := runtimePayload["runtime_tool"]; !ok {
		t.Fatalf("runtime tool_completed missing runtime_tool: %s", string(runtimeRaw))
	}
	if _, ok := runtimePayload["status"]; ok {
		t.Fatalf("runtime tool_completed must not marshal status: %s", string(runtimeRaw))
	}
	if _, ok := runtimePayload["message"]; ok {
		t.Fatalf("runtime tool_completed must not marshal message: %s", string(runtimeRaw))
	}
}

func TestAgentEventToStreamEventPreservesExistingMappings(t *testing.T) {
	server := &Server{}
	sess := &session.Session{}

	assistant, ok := server.agentEventToStreamEvent(sess, config.ProviderOpenAI, agent.Event{
		Type:  agent.EventAssistantDelta,
		Delta: "hello",
	})
	if !ok || assistant.Type != "assistant_delta" || assistant.Delta != "hello" {
		t.Fatalf("assistant mapping changed: %#v", assistant)
	}

	toolExec, ok := server.agentEventToStreamEvent(sess, config.ProviderOpenAI, agent.Event{
		Type: agent.EventToolExecuting,
		Step: 1,
		ToolCalls: []agent.ToolCallEvent{{
			ID: "call_1", Name: "read", Input: `{"path":"foo.go"}`,
		}},
	})
	if !ok || toolExec.Type != "tool_executing" || len(toolExec.ToolCalls) != 1 {
		t.Fatalf("tool_executing mapping changed: %#v", toolExec)
	}
}

func EventLLMRuntimeAlias() agent.EventType {
	return agent.EventLLMRuntime
}
