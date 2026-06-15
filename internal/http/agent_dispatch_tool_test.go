package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmptyDockerDelegationMessageIncludesToolLoopDiagnostics(t *testing.T) {
	message := emptyDockerDelegationMessage("agent-youtube", "child-1", ChatResponse{
		Status: "completed",
		Messages: []MessageResponse{
			{Role: "assistant", ToolCalls: []ToolCallResponse{{ID: "call-1", Name: "youtube_transcript", Input: json.RawMessage(`{"url":"https://youtu.be/abc"}`)}}},
			{Role: "tool", ToolResults: []ToolResultResponse{{ToolCallID: "call-1", Name: "youtube_transcript", Content: strings.Repeat("x", 15185)}}},
		},
	})

	for _, expected := range []string{
		`docker agent "agent-youtube" returned empty response`,
		"child session child-1",
		"status=completed",
		"1 tool call(s)",
		"1 tool result(s)",
		"last_tool=youtube_transcript",
		"last_tool_result_chars=15185",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected diagnostic to contain %q, got %q", expected, message)
		}
	}
}

func TestPostLocalDockerAgentChatStreamReturnsDoneEvent(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/sessions/child-1/chat/stream" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"status","status":"running"}` + "\n"))
		_, _ = w.Write([]byte(`{"type":"tool_executing","step":1,"tool_calls":[{"id":"call-1","name":"grep","input":{}}]}` + "\n"))
		_, _ = w.Write([]byte(`{"type":"done","content":"audit complete","status":"completed","usage":{"input_tokens":7,"output_tokens":3},"messages":[{"role":"assistant","content":"audit complete","timestamp":"2026-06-15T12:00:00Z"}]}` + "\n"))
	}))
	defer server.Close()

	var eventTypes []string
	resp, err := postLocalDockerAgentChatStream(
		context.Background(),
		server.Client(),
		server.URL+"/sessions/child-1/chat/stream",
		ChatRequest{Message: "audit"},
		func(event ChatStreamEvent) {
			eventTypes = append(eventTypes, event.Type)
		},
	)
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if resp.Content != "audit complete" || resp.Status != "completed" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].Content != "audit complete" {
		t.Fatalf("expected final messages, got %+v", resp.Messages)
	}
	if strings.Join(eventTypes, ",") != "status,tool_executing,done" {
		t.Fatalf("unexpected event types: %v", eventTypes)
	}
}

func TestPostLocalDockerAgentChatStreamReturnsErrorEvent(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"error","error":"child failed","status":"failed","messages":[{"role":"assistant","content":"Request failed","timestamp":"2026-06-15T12:00:00Z"}]}` + "\n"))
	}))
	defer server.Close()

	resp, err := postLocalDockerAgentChatStream(context.Background(), server.Client(), server.URL+"/stream", ChatRequest{Message: "audit"}, nil)
	if err == nil || !strings.Contains(err.Error(), "child failed") {
		t.Fatalf("expected child error, got resp=%+v err=%v", resp, err)
	}
	if resp.Status != "failed" || len(resp.Messages) != 1 {
		t.Fatalf("expected error event state to be preserved, got %+v", resp)
	}
}

func TestPostLocalDockerAgentChatStreamRequiresTerminalEvent(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"status","status":"running"}` + "\n"))
	}))
	defer server.Close()

	resp, err := postLocalDockerAgentChatStream(context.Background(), server.Client(), server.URL+"/stream", ChatRequest{Message: "audit"}, nil)
	if err == nil || !strings.Contains(err.Error(), "ended before terminal event") {
		t.Fatalf("expected terminal-event error, got resp=%+v err=%v", resp, err)
	}
	if resp.Status != "running" {
		t.Fatalf("expected last status to be retained, got %+v", resp)
	}
}
