package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type progressWrite struct {
	messageID string
	value     interface{}
}

type progressTestStore struct {
	storage.Store
	writes  chan progressWrite
	entered chan struct{}
	release chan struct{}
	fail    bool
}

func (s *progressTestStore) UpdateMessageMetadataKey(_, messageID, _ string, value interface{}, _ time.Time) error {
	if s.entered != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	if s.fail {
		s.fail = false
		return errors.New("temporary write failure")
	}
	// Capture an immutable snapshot, just like SQLite's JSON serialization.
	raw, _ := json.Marshal(value)
	var snapshot interface{}
	json.Unmarshal(raw, &snapshot)
	s.writes <- progressWrite{messageID: messageID, value: snapshot}
	return nil
}

func progressTestSession() *session.Session {
	sess := session.New("build")
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "call", Name: "parallel"}})
	return sess
}

func TestToolProgressFinalFlushCoalescesParallelSteps(t *testing.T) {
	store := &progressTestStore{writes: make(chan progressWrite, 10)}
	sess := progressTestSession()
	ag := New(Config{}, nil, nil, session.NewManager(store))
	p := ag.startToolProgressPersistence(sess, time.Hour)
	var wg sync.WaitGroup
	for step := 1; step <= 4; step++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			for tick := 0; tick < 100; tick++ {
				p.record(tools.ProgressEvent{ToolCallID: "call", Content: fmt.Sprint(tick), Metadata: map[string]interface{}{"parallel_step": step}})
			}
		}(step)
	}
	wg.Wait()
	if len(store.writes) != 0 {
		t.Fatal("saved before throttle interval")
	}
	if len(p.pending) != 4 {
		t.Fatalf("queue not coalesced: %d pending events", len(p.pending))
	}
	p.finish()
	if len(store.writes) != 1 {
		t.Fatalf("got %d writes, want one final flush", len(store.writes))
	}
	write := <-store.writes
	results := pendingToolResultsFromMetadata(write.value)
	if write.messageID != sess.Messages[0].ID || len(results) != 1 {
		t.Fatalf("bad final flush: %#v", write)
	}
	steps := parallelStepProgressFromMetadata(results[0].Metadata[toolMetadataParallelStepProgress])
	if len(steps) != 4 {
		t.Fatalf("lost parallel step history: %#v", steps)
	}
	for _, raw := range steps {
		step, _ := raw.(map[string]interface{})
		if step["content"] != "99" {
			t.Fatalf("latest progress not coalesced: %#v", step)
		}
	}
}

func TestToolProgressRecordingDoesNotWaitForDatabase(t *testing.T) {
	store := &progressTestStore{writes: make(chan progressWrite, 10), entered: make(chan struct{}, 10), release: make(chan struct{})}
	sess := progressTestSession()
	p := New(Config{}, nil, nil, session.NewManager(store)).startToolProgressPersistence(sess, 10*time.Millisecond)
	p.record(tools.ProgressEvent{ToolCallID: "call", Content: "first"})
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("periodic persistence did not start")
	}
	recorded := make(chan struct{})
	go func() {
		p.record(tools.ProgressEvent{ToolCallID: "call", Content: "latest"})
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(time.Second):
		close(store.release)
		p.finish()
		t.Fatal("record blocked on DB persistence")
	}
	close(store.release)
	p.finish()
	var last progressWrite
	for len(store.writes) > 0 {
		last = <-store.writes
	}
	if results := pendingToolResultsFromMetadata(last.value); len(results) != 1 || results[0].Content != "latest" {
		t.Fatalf("final pending progress lost: %#v", last)
	}
}

func TestToolProgressRetriesFailedPeriodicWriteOnFinish(t *testing.T) {
	store := &progressTestStore{writes: make(chan progressWrite, 10), entered: make(chan struct{}, 10), release: make(chan struct{}), fail: true}
	close(store.release)
	p := New(Config{}, nil, nil, session.NewManager(store)).startToolProgressPersistence(progressTestSession(), 10*time.Millisecond)
	p.record(tools.ProgressEvent{ToolCallID: "call", Content: "retry"})
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("periodic write did not run")
	}
	p.finish()
	if len(store.writes) != 1 {
		t.Fatalf("failed write not retried: %d successful writes", len(store.writes))
	}
}

// Signal only after the narrow write has committed, so the stale-save race is
// deterministic rather than relying on sleeps or worker scheduling.
type signalingProgressStore struct {
	*storage.SQLiteStore
	committed chan struct{}
}

func (s *signalingProgressStore) UpdateMessageMetadataKey(sessionID, messageID, key string, value interface{}, updatedAt time.Time) error {
	err := s.SQLiteStore.UpdateMessageMetadataKey(sessionID, messageID, key, value, updatedAt)
	if err == nil {
		s.committed <- struct{}{}
	}
	return err
}

func TestToolProgressFinalFlushRestoresStaleSessionSave(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &signalingProgressStore{SQLiteStore: store, committed: make(chan struct{}, 10)}
	sm := session.NewManager(wrapped)
	sess := progressTestSession()
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	stale, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{}, nil, nil, sm).startToolProgressPersistence(sess, 10*time.Millisecond)
	p.record(tools.ProgressEvent{ToolCallID: "call", Content: "durable latest"})
	select {
	case <-wrapped.committed:
	case <-time.After(time.Second):
		t.Fatal("periodic persistence did not commit")
	}
	// A note/tool loaded before that commit can overwrite pending progress.
	stale.AddUserMessageWithImagesAndMetadata("note", nil, map[string]interface{}{"injected_during_run": true})
	if err := sm.Save(stale); err != nil {
		t.Fatal(err)
	}
	p.finish()
	fresh, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	results := pendingToolResultsFromMetadata(fresh.Messages[0].Metadata[messageMetadataPendingToolResults])
	if len(results) != 1 || results[0].Content != "durable latest" || len(fresh.Messages) != 2 {
		t.Fatalf("final flush did not restore progress/preserve note: %#v", fresh.Messages)
	}
}
