package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestBuildRequestReadCacheCompaction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager := tools.NewManager(dir)
	ag := New(Config{}, nil, manager, nil)
	sess := session.New("test")
	ctx := context.WithValue(context.Background(), "session_id", sess.ID)
	params := json.RawMessage(`{"path":"sample.txt"}`)
	read := func(id string) string {
		result, err := manager.Execute(context.WithValue(ctx, "tool_call_id", id), "read", params)
		if err != nil || !result.Success {
			t.Fatalf("read failed: %+v %v", result, err)
		}
		sess.AddAssistantMessage("", []session.ToolCall{{ID: id, Name: "read", Input: params}})
		sess.AddToolResult([]session.ToolResult{{ToolCallID: id, Name: "read", Content: result.Output, Metadata: result.Metadata}})
		return result.Output
	}
	first := read("first")
	ag.buildRequest(sess)
	stub := read("repeat")
	if !strings.Contains(stub, "unchanged since earlier read") {
		t.Fatalf("not cached: %s", stub)
	}
	// The same boundary used by real compaction: the original is retained in
	// storage, but no longer included in the active model conversation.
	summary := session.Message{Role: "assistant", Content: "summary", Metadata: map[string]interface{}{messageMetadataCompaction: true}}
	sess.Messages = append(sess.Messages[:2], append([]session.Message{summary}, sess.Messages[2:]...)...)
	request := ag.buildRequest(sess)
	if got := request.Messages[len(request.Messages)-1].ToolResults[0].Content; got != first {
		t.Fatalf("dangling stub not restored: %s", got)
	}
	request = ag.buildRequest(sess)
	if got := request.Messages[len(request.Messages)-1].ToolResults[0].Content; got != first {
		t.Fatalf("repair not persisted: %s", got)
	}
	if got := read("after-compaction"); got != first {
		t.Fatalf("compacted original remained cached: %s", got)
	}
}

func TestBuildRequestReadCacheCompressedOriginal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager := tools.NewManager(dir)
	ag := New(Config{CompressToolResults: true}, nil, manager, nil)
	sess := session.New("test")
	ctx := context.WithValue(context.Background(), "session_id", sess.ID)
	ctx = context.WithValue(ctx, "tool_call_id", "first")
	params := json.RawMessage(`{"path":"sample.txt"}`)
	first, err := manager.Execute(ctx, "read", params)
	if err != nil {
		t.Fatal(err)
	}
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "first", Name: "read", Input: params}})
	sess.AddToolResult([]session.ToolResult{{ToolCallID: "first", Name: "read", Content: first.Output}})
	ag.buildRequest(sess)
	sess.Messages[1].ToolResults[0].Content = "[brute-compressed kind=tool_result] alpha"
	ag.buildRequest(sess)
	repeat, err := manager.Execute(ctx, "read", params)
	if err != nil || repeat.Output != first.Output {
		t.Fatalf("compressed original was cached: %+v %v", repeat, err)
	}
}
