package agent

import (
	"context"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type parkingLLM struct {
	sm        *session.Manager
	sessionID string
	content   string
	calls     int
}

func (p *parkingLLM) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	if err := p.sm.SetPendingQuestion(p.sessionID, &session.QuestionData{Question: "Which color?"}); err != nil {
		return nil, err
	}
	if err := p.sm.SetSessionStatus(p.sessionID, string(session.StatusInputRequired)); err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Content: p.content}, nil
}

func TestRunKeepsInputRequiredParkedDuringProviderTurn(t *testing.T) {
	for name, content := range map[string]string{"with reply": "Waiting for your answer.", "empty reply": ""} {
		t.Run(name, func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("build")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			client := &parkingLLM{sm: sm, sessionID: sess.ID, content: content}
			ag := New(Config{MaxSteps: 5}, client, tools.NewManager(t.TempDir()), sm)
			if _, _, err := ag.Run(context.Background(), sess, "pick a color"); err != nil {
				t.Fatalf("run: %v", err)
			}
			if client.calls != 1 {
				t.Fatalf("LLM calls = %d, want 1", client.calls)
			}
			got, err := sm.Get(sess.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Status != session.StatusInputRequired {
				t.Fatalf("status = %q, want input_required", got.Status)
			}
			if q, _ := sm.GetPendingQuestion(sess.ID); q == nil || q.Question != "Which color?" {
				t.Fatalf("pending question lost: %#v", q)
			}
		})
	}
}
