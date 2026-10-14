package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// BenchmarkLoopSessionReloads500Messages isolates DB/merge overhead, excluding
// LLM and tool execution. Each tool result contributes 16 KiB to the transcript.
func BenchmarkLoopSessionReloads500Messages(b *testing.B) {
	store, err := storage.NewSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess := session.New("build")
	for i := 0; i < 500; i++ {
		sess.AddToolResult([]session.ToolResult{{ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 16<<10)}})
	}
	if err := sm.Save(sess); err != nil {
		b.Fatal(err)
	}
	ag := New(Config{}, nil, nil, sm)
	b.Run("tool_step", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ag.mergeFreshSessionState(sess) // before buildRequest
			if fresh, _ := ag.mergeFreshSessionLiveState(sess); fresh == nil {
				b.Fatal("missing live state")
			}
		}
	})
	b.Run("progress_tick", func(b *testing.B) {
		progressSession := *sess
		progressSession.Messages = append([]session.Message(nil), sess.Messages...)
		progressSession.AddAssistantMessage("", []session.ToolCall{{ID: "progress", Name: "test"}})
		if err := sm.Save(&progressSession); err != nil {
			b.Fatal(err)
		}
		p := ag.startToolProgressPersistence(&progressSession, toolProgressSaveInterval)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p.record(tools.ProgressEvent{ToolCallID: "progress", Content: "working"})
		}
		b.StopTimer()
		p.finish()
	})
}

func BenchmarkToolProgressFlush500Messages(b *testing.B) {
	store, err := storage.NewSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess := session.New("build")
	for i := 0; i < 499; i++ {
		sess.AddToolResult([]session.ToolResult{{ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 16<<10)}})
	}
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "progress", Name: "test"}})
	if err := sm.Save(sess); err != nil {
		b.Fatal(err)
	}
	value := []session.ToolResult{{ToolCallID: "progress", Content: "working"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sm.SaveMessageMetadataKey(sess.ID, sess.GetLastMessage().ID, messageMetadataPendingToolResults, value, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
