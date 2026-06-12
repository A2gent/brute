package http

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestInputRequiredStreamEventIncludesQuestionAndTranscript(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("deploy this")
	sess.AddAssistantMessage("I need confirmation.", nil)
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	question := &session.QuestionData{
		Question: "Deploy to production?",
		Header:   "Confirm deploy",
		Options: []session.QuestionOption{
			{Label: "Deploy", Description: "Proceed with production deploy"},
			{Label: "Stop", Description: "Do not deploy"},
		},
		Custom: true,
	}
	if err := sessionManager.SetPendingQuestion(sess.ID, question); err != nil {
		t.Fatalf("failed to set pending question: %v", err)
	}
	if err := sessionManager.SetSessionStatus(sess.ID, string(session.StatusInputRequired)); err != nil {
		t.Fatalf("failed to set input-required status: %v", err)
	}
	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}

	server := &Server{sessionManager: sessionManager}
	event := server.inputRequiredStreamEvent(fresh)
	if event == nil {
		t.Fatal("expected input-required stream event")
	}
	if event.Type != "input_required" || event.Status != string(session.StatusInputRequired) {
		t.Fatalf("unexpected event type/status: %#v", event)
	}
	if event.Question == nil || event.Question.Header != "Confirm deploy" {
		t.Fatalf("expected pending question payload, got %#v", event.Question)
	}
	if len(event.Messages) != 2 {
		t.Fatalf("expected transcript messages in event, got %d", len(event.Messages))
	}
}
