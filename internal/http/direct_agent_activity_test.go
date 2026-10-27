package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
)

func runtimeTurn(id string, extra map[string]interface{}) map[string]interface{} {
	md := map[string]interface{}{"runtime_turn_id": id}
	for k, v := range extra {
		md[k] = v
	}
	return md
}

// directChildTranscript is a child session with two turns. The second turn has
// a parallel ordinary delegation, an injected note, a native-runtime step and
// the final reply.
func directChildTranscript() []MessageResponse {
	return []MessageResponse{
		{Role: "user", Content: "first request"},
		{Role: "assistant", ToolCalls: []ToolCallResponse{{ID: "old-call", Name: "bash", Input: json.RawMessage(`{}`)}}, Metadata: runtimeTurn("t1a", nil)},
		{Role: "tool", ToolResults: []ToolResultResponse{{ToolCallID: "old-call", Name: "bash", Content: "old output"}}},
		{Role: "assistant", Content: "first answer", Metadata: runtimeTurn("t1f", nil)},
		{Role: "user", Content: "second request"},
		{Role: "assistant", Content: "delegating", ToolCalls: []ToolCallResponse{
			{ID: "call-a", Name: "delegate_to_agent", Input: json.RawMessage(`{"agent":"a"}`)},
			{ID: "call-b", Name: "delegate_to_agent", Input: json.RawMessage(`{"agent":"b"}`)},
		}, Metadata: runtimeTurn("t2a", map[string]interface{}{"llm_usage": 1, "provider_response_id": "resp-1"})},
		{Role: "tool", ToolResults: []ToolResultResponse{
			{ToolCallID: "call-a", Name: "delegate_to_agent", Content: "A done", Metadata: map[string]interface{}{"child_session_id": "grand-a"}, DurationMs: 5},
			{ToolCallID: "call-b", Name: "delegate_to_agent", Content: "B failed", IsError: true, Metadata: map[string]interface{}{"child_session_id": "grand-b"}},
		}},
		{Role: "user", Content: "also check logs", Metadata: map[string]interface{}{"injected_during_run": true}},
		{Role: "assistant", Content: "checking", Metadata: runtimeTurn("t2b", map[string]interface{}{"runtime_native": true, "runtime_tools": map[string]interface{}{"n1": map[string]interface{}{"name": "Read", "output": "log"}}})},
		{Role: "assistant", Content: "second answer", Metadata: runtimeTurn("t2f", map[string]interface{}{"llm_usage": 2})},
	}
}

// assertSecondTurnActivity checks the projected activity of turn 2 (without the
// final reply) as returned to Caesar.
func assertSecondTurnActivity(t *testing.T, got []MessageResponse) {
	t.Helper()
	if len(got) != 3 {
		t.Fatalf("activity = %d messages, want 3: %+v", len(got), got)
	}
	call := got[0]
	if call.Role != "assistant" || call.Content != "delegating" || len(call.ToolCalls) != 2 || call.ToolCalls[0].ID != "call-a" || call.ToolCalls[1].ID != "call-b" {
		t.Fatalf("parallel tool call message = %+v", call)
	}
	if call.Metadata["runtime_turn_id"] != "t2a" || call.Metadata["llm_usage"] != nil || call.Metadata["provider_response_id"] != nil {
		t.Fatalf("tool call metadata = %v, want runtime_* only", call.Metadata)
	}
	results := got[1].ToolResults
	if got[1].Role != "tool" || len(results) != 2 || results[0].Metadata["child_session_id"] != "grand-a" || results[1].Metadata["child_session_id"] != "grand-b" || !results[1].IsError || results[0].DurationMs != 5 {
		t.Fatalf("tool results = %+v", got[1])
	}
	native := got[2]
	if native.Content != "checking" || native.Metadata["runtime_native"] != true || native.Metadata["runtime_tools"] == nil {
		t.Fatalf("native step = %+v", native)
	}
}

func TestDirectAgentTurnActivityIsCurrentTurnOnly(t *testing.T) {
	msgs := directChildTranscript()
	assertSecondTurnActivity(t, directAgentTurnActivity(msgs, len(msgs)-1))
	// First turn of a multi-turn child: bounded by its own request.
	if got := directAgentTurnActivity(msgs, 3); len(got) != 2 || got[0].ToolCalls[0].ID != "old-call" {
		t.Fatalf("first turn activity = %+v", got)
	}
	// No request boundary: never replay the whole history.
	if got := directAgentTurnActivity(msgs[5:], 4); got != nil {
		t.Fatalf("activity without boundary = %+v, want nil", got)
	}
}

func TestAppendDirectAgentReplyPersistsActivityOnce(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := &session.Session{ID: "parent"}
	sess.AddUserMessage("second request")
	appendDirectAgentReply(sess, ChatResponse{Content: "second answer", Messages: directChildTranscript()})

	got := server.messagesToResponse(sess.Messages)
	if len(got) != 5 || got[0].Role != "user" {
		t.Fatalf("parent messages = %+v", got)
	}
	assertSecondTurnActivity(t, got[1:4])
	final := got[4]
	if final.Content != "second answer" || final.Metadata["runtime_turn_id"] != "t2f" || final.Metadata[directAgentActivityMarker] != true || final.Metadata["llm_usage"] != nil {
		t.Fatalf("final reply = %+v", final)
	}
	for _, m := range got {
		if m.Content == "first answer" || m.Content == "also check logs" {
			t.Fatalf("previous turn or injected note copied: %+v", m)
		}
	}
}

// The background session_run path persists through finalize; it must store the
// same activity and survive a storage round trip.
func TestFinalizeSessionRunPersistsDirectAgentActivity(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("second request")
	resp := ChatResponse{Content: "second answer", Status: "completed", Messages: directChildTranscript()}
	if err := server.finalizeSessionRunWithoutStreaming(context.Background(), sess, sessionRunResult{DirectAgent: true, Content: resp.Content, DirectReply: resp}, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := server.messagesToResponse(loaded.Messages)
	if len(got) != 5 {
		t.Fatalf("persisted messages = %+v", got)
	}
	assertSecondTurnActivity(t, got[1:4])
	if got[4].Content != "second answer" || got[4].Metadata[directAgentActivityMarker] != true {
		t.Fatalf("final reply = %+v", got[4])
	}
}

func TestGetSessionHydratesLegacyDirectAgentActivity(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	parent, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	serveChild := func(id, source string) *httptest.Server {
		return httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			fetches.Add(1)
			_ = json.NewEncoder(w).Encode(SessionResponse{ID: id, Status: "completed", Messages: directChildTranscript(),
				Metadata: map[string]interface{}{"source": source, "parent_session_id": parent.ID}})
		}))
	}
	direct := serveChild("direct-child", "direct_agent_session")
	defer direct.Close()
	// Newest mapping is a delegate_to_agent child with the same turn ids: it
	// must not be used to hydrate the direct conversation.
	delegated := serveChild("delegated-child", "delegate_to_agent")
	defer delegated.Close()

	parent.Metadata = map[string]interface{}{"docker_agent_id": "agent-1"}
	addDockerChildSessionEntry(parent, &LocalDockerAgent{Name: "qa", APIURL: direct.URL}, "direct-child", nil)
	addDockerChildSessionEntry(parent, &LocalDockerAgent{Name: "qa", APIURL: "http://example.com"}, "remote-child", nil)
	addDockerChildSessionEntry(parent, &LocalDockerAgent{Name: "qa", APIURL: delegated.URL}, "delegated-child", nil)
	// Legacy reply saved before activity persistence: only final text.
	parent.AddUserMessage("second request")
	parent.AddAssistantMessageWithMetadata("second answer", nil, map[string]interface{}{"runtime_turn_id": "t2f"})
	parent.SetStatus(session.StatusCompleted)
	if err := server.sessionManager.Save(parent); err != nil {
		t.Fatal(err)
	}

	get := func(query string) SessionResponse {
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodGet, "/sessions/"+parent.ID+query, nil))
		if rec.Code != stdhttp.StatusOK {
			t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
		}
		var resp SessionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	got := get("").Messages
	if len(got) != 5 || got[0].Content != "second request" || got[4].Content != "second answer" {
		t.Fatalf("hydrated messages = %+v", got)
	}
	assertSecondTurnActivity(t, got[1:4])
	if fetches.Load() != 2 {
		t.Fatalf("child fetches = %d, want 2 (delegated rejected, direct used)", fetches.Load())
	}
	// The reply can be the first message on a page. Its cursor must remain a
	// parent-store ID, not one of the display-only child activity IDs.
	page := get("?message_limit=1")
	if len(page.Messages) != 4 || page.MessagePage == nil || !page.MessagePage.HasMore || page.MessagePage.BeforeMessage != got[4].ID {
		t.Fatalf("reply-only page = %+v", page)
	}
	assertSecondTurnActivity(t, page.Messages[:3])

	// Read-only: storage keeps the legacy transcript.
	if stored, _ := server.sessionManager.Get(parent.ID); len(stored.Messages) != 2 {
		t.Fatalf("stored messages = %d, want 2", len(stored.Messages))
	}

	// Reconnecting to SSE must not overwrite GET's recovered history with the
	// legacy activity-less transcript.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.router.ServeHTTP(stream, httptest.NewRequest(stdhttp.MethodGet, "/sessions/"+parent.ID+"/events", nil).WithContext(ctx))
	}()
	if !stream.waitFor("grand-a", 3*time.Second) {
		t.Fatal("SSE snapshot lost hydrated delegation")
	}
	cancel()
	<-done

	// Replies persisted with their activity are never re-fetched.
	parent, _ = server.sessionManager.Get(parent.ID)
	parent.Messages = nil
	parent.AddUserMessage("second request")
	appendDirectAgentReply(parent, ChatResponse{Content: "second answer", Messages: directChildTranscript()})
	if err := server.sessionManager.Save(parent); err != nil {
		t.Fatal(err)
	}
	fetches.Store(0)
	if got := get("").Messages; len(got) != 5 || fetches.Load() != 0 {
		t.Fatalf("persisted session: %d messages, %d fetches; want 5 and 0", len(got), fetches.Load())
	}
}

func TestAppendDirectAgentReplyPreservesParkedToolTail(t *testing.T) {
	for _, status := range []string{"input_required", "waiting_external", "paused"} {
		t.Run(status, func(t *testing.T) {
			server, _ := newUnifiedAgentsTestServer(t)
			parent := &session.Session{ID: "parent"}
			parent.AddUserMessage("delegate and wait")
			child := []MessageResponse{
				{Role: "user", Content: "previous request"},
				{Role: "assistant", Content: "previous answer"},
				{Role: "user", Content: "delegate and wait"},
				{Role: "assistant", Metadata: runtimeTurn("parked", nil), ToolCalls: []ToolCallResponse{
					{ID: "delegate", Name: "delegate_to_agent", Input: json.RawMessage(`{"agent_id":"developer","task":"fix"}`)},
				}},
				{Role: "tool", ToolResults: []ToolResultResponse{
					{ToolCallID: "delegate", Name: "delegate_to_agent", Content: "waiting", Metadata: map[string]interface{}{"child_session_id": "developer-child", "progress_pending": true}},
				}},
			}
			appendDirectAgentReply(parent, ChatResponse{Status: status, Messages: child})
			got := server.messagesToResponse(parent.Messages)
			if len(got) != 3 || len(got[1].ToolCalls) != 1 || len(got[2].ToolResults) != 1 {
				t.Fatalf("parked transcript = %+v", got)
			}
			if got[1].Metadata[directAgentActivityMarker] != true || got[2].ToolResults[0].Metadata["child_session_id"] != "developer-child" {
				t.Fatalf("parked identity or activity marker lost: %+v", got)
			}
		})
	}
}

func TestDirectAgentReplyDoesNotBorrowPreviousAnswer(t *testing.T) {
	messages := []MessageResponse{
		{Role: "user", Content: "old request"},
		{Role: "assistant", Content: "old answer", Metadata: runtimeTurn("old", nil)},
		{Role: "user", Content: "new request"},
	}
	if got := directAgentReplyMetadata(ChatResponse{Messages: messages}); got != nil {
		t.Fatalf("previous reply metadata leaked: %+v", got)
	}
}

func TestDirectAgentParkedRecoveryDoesNotDuplicateNarration(t *testing.T) {
	messages := []MessageResponse{
		{Role: "user", Content: "work"},
		{Role: "assistant", Content: "Delegating now.", Metadata: runtimeTurn("parked", nil), ToolCalls: []ToolCallResponse{{ID: "call", Name: "delegate_to_agent", Input: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolResults: []ToolResultResponse{{ToolCallID: "call", Content: "waiting"}}},
	}
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method == stdhttp.MethodGet {
			_ = json.NewEncoder(w).Encode(SessionResponse{ID: "child", Status: "waiting_external", Messages: messages})
			return
		}
		// A disconnected stream sends status but never its terminal event.
		_ = json.NewEncoder(w).Encode(ChatStreamEvent{Type: "status", Status: "waiting_external"})
	}))
	defer child.Close()
	recovered, err := postLocalDockerAgentChatStream(context.Background(), child.Client(), child.URL+"/sessions/child/chat/stream", ChatRequest{Message: "work"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Content != "Delegating now." {
		t.Fatalf("recovered content = %q", recovered.Content)
	}
	parent := &session.Session{}
	parent.AddUserMessage("work")
	appendDirectAgentReply(parent, recovered)
	if len(parent.Messages) != 3 || parent.Messages[2].Role != "tool" {
		t.Fatalf("duplicated recovered narration: %+v", parent.Messages)
	}
}

func TestHydrateDirectAgentParkedHistoryStaysWithinTurn(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	parent := &session.Session{ID: "parent", Metadata: map[string]interface{}{"docker_agent_id": "direct"}}
	childMessages := []MessageResponse{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "Waiting.", Metadata: runtimeTurn("parked", nil), ToolCalls: []ToolCallResponse{{ID: "question", Name: "question", Input: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolResults: []ToolResultResponse{{ToolCallID: "question", Content: "asked"}}},
		{Role: "user", Content: "resume"},
		{Role: "assistant", Content: "later answer", Metadata: runtimeTurn("done", nil)},
	}
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		_ = json.NewEncoder(w).Encode(SessionResponse{ID: "child", Messages: childMessages, Metadata: map[string]interface{}{"source": "direct_agent_session", "parent_session_id": parent.ID}})
	}))
	defer child.Close()
	addDockerChildSessionEntry(parent, &LocalDockerAgent{APIURL: child.URL}, "child", nil)
	legacy := []MessageResponse{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "Waiting.", Metadata: runtimeTurn("parked", nil)},
		{Role: "user", Content: "resume"},
		{Role: "assistant", Content: "later answer", Metadata: runtimeTurn("done", nil)},
	}
	got := server.hydrateDirectAgentActivity(context.Background(), parent, legacy)
	if len(got) != 5 || got[2].Role != "tool" || got[3].Role != "user" || got[4].Content != "later answer" {
		t.Fatalf("parked turn leaked later activity or duplicated narration: %+v", got)
	}
	page := server.hydrateDirectAgentActivity(context.Background(), parent, legacy[1:2])
	if len(page) != 2 || page[0].Content != "Waiting." || page[1].Role != "tool" {
		t.Fatalf("old page includes later turns: %+v", page)
	}
}
