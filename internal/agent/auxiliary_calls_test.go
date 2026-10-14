package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

type compactionStatusClient struct {
	MockLLM
	beforeChat func()
}

func (c *compactionStatusClient) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.beforeChat()
	return c.MockLLM.Chat(ctx, request)
}

func TestAuxiliaryCompactionStatusAndTiming(t *testing.T) {
	t.Setenv(envCompactionTriggerPercent, "50")
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("test")
			if err != nil {
				t.Fatal(err)
			}
			sess.AddUserMessage("old prompt")
			sess.AddAssistantMessage("old answer", nil)
			sess.AddUserMessage("continue")
			metadataSetFloat(sess, metadataCurrentContextTokens, 600)
			var events []Event
			client := &compactionStatusClient{MockLLM: MockLLM{Response: &llm.ChatResponse{Content: "summary"}}}
			if fail {
				client.Err = errors.New("provider unavailable")
			}
			client.beforeChat = func() {
				if len(events) != 1 || events[0].Type != EventProviderTrace || events[0].Step != 2 || events[0].Provider.Phase != "compaction_started" || events[0].Provider.Reason != "Compacting context..." {
					t.Fatalf("missing status before Chat: %+v", events)
				}
			}
			ag := New(Config{ContextWindow: 1000}, client, nil, sm)
			_, compacted, err := ag.maybeCompactContext(context.Background(), sess, 2, func(ev Event) { events = append(events, ev) })
			if compacted == fail || (err != nil) != fail {
				t.Fatalf("compacted=%v err=%v", compacted, err)
			}
			if metadataFloat(sess.Metadata, "compaction_attempt_count") != 1 {
				t.Fatal("missing attempt count")
			}
			if _, ok := sess.Metadata["compaction_last_duration_ms"]; !ok {
				t.Fatal("missing duration")
			}
			want := "success"
			if fail {
				want = "error"
			}
			if sess.Metadata["compaction_last_outcome"] != want {
				t.Fatalf("metadata: %v", sess.Metadata)
			}
			if fail {
				// The loop persists the original session when compaction fails.
				if err := sm.Save(sess); err != nil {
					t.Fatal(err)
				}
			}
			{
				saved, err := sm.Get(sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Metadata["compaction_last_outcome"] != want {
					t.Fatal("timing not persisted")
				}
			}
		})
	}
}

func TestAuxiliaryToolSelectionTimeoutFailOpen(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	ag := newSelectionAgent(t, jev.NewClient("key", "", server.URL+"/v1"))
	sess := session.New("test")
	sess.AddUserMessage("open example.com")
	started := time.Now()
	ag.ensureToolGroups(context.Background(), sess)
	elapsed := time.Since(started)
	if elapsed < 2500*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("selection blocked for %s", elapsed)
	}
	if !toolNames(ag, sess)["jira_query"] {
		t.Fatal("timeout must fail open")
	}
	if sess.Metadata["tool_selection_last_outcome"] != "timeout" || metadataFloat(sess.Metadata, "tool_selection_attempt_count") != 1 {
		t.Fatalf("metadata: %v", sess.Metadata)
	}
	ag.ensureToolGroups(context.Background(), sess)
	if calls.Load() != 1 {
		t.Fatalf("retried timed out selector: %d calls", calls.Load())
	}
}
