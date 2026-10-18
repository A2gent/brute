package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestRunPausesWhenDiskSpaceIsLow(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("hello")

	original := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = original })
	freeDiskBytes = func(string) (uint64, bool) { return 1 << 30, true }

	mockLLM := &MockLLM{Response: &llm.ChatResponse{Content: "hi"}}
	ag := New(Config{MaxSteps: 3}, mockLLM, tools.NewManager(t.TempDir()), sm)
	_, _, err = ag.RunWithEvents(context.Background(), sess, "hello", nil)

	if !errors.Is(err, ErrLowDiskSpace) {
		t.Fatalf("expected ErrLowDiskSpace, got %v", err)
	}
	if mockLLM.CapturedRequest != nil {
		t.Fatal("LLM must not be called when disk space is low")
	}
	if sess.Status != session.StatusPaused {
		t.Fatalf("expected paused status, got %s", sess.Status)
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "1.0 GB free") {
		t.Fatalf("expected low disk explanation, got %s: %q", last.Role, last.Content)
	}
	saved, err := sm.Get(sess.ID)
	if err != nil || saved.Status != session.StatusPaused {
		t.Fatalf("paused status not persisted: %v", err)
	}

	freeDiskBytes = func(string) (uint64, bool) { return 10 << 30, true }
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "hello", nil); err != nil {
		t.Fatalf("run should proceed with enough disk space: %v", err)
	}
}

func TestAvailableDiskBytesReportsRealDisk(t *testing.T) {
	free, ok := availableDiskBytes(t.TempDir())
	if ok && free == 0 {
		t.Fatal("expected non-zero free space")
	}
}
