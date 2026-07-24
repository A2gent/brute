package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

type questionSessionStoreStub struct {
	question *session.QuestionData
	status   string
}

func (s *questionSessionStoreStub) SetPendingQuestion(_ string, data *session.QuestionData) error {
	s.question = data
	return nil
}

func (s *questionSessionStoreStub) SetSessionStatus(_ string, status string) error {
	s.status = status
	return nil
}

func TestQuestionToolExecuteWithMediaOptions(t *testing.T) {
	store := &questionSessionStoreStub{}
	tool := NewQuestionTool(store)
	params := json.RawMessage(`{
		"question": "Which asset should we use?",
		"header": "Pick asset",
		"options": [
			{
				"label": "Sunset",
				"description": "Warm palette",
				"image_url": "/workspace/generated/comfyui/sunset.png"
			},
			{
				"label": "Narrator A",
				"audio_url": "/workspace/generated/tts/voice-a.mp3"
			}
		]
	}`)

	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() success = false, error = %q", result.Error)
	}
	if store.status != "input_required" {
		t.Fatalf("status = %q, want input_required", store.status)
	}
	if store.question == nil {
		t.Fatal("expected pending question to be stored")
	}
	if got, want := len(store.question.Options), 2; got != want {
		t.Fatalf("options len = %d, want %d", got, want)
	}
	if got := store.question.Options[0].ImageURL; got != "/workspace/generated/comfyui/sunset.png" {
		t.Fatalf("option 0 image_url = %q", got)
	}
	if got := store.question.Options[1].AudioURL; got != "/workspace/generated/tts/voice-a.mp3" {
		t.Fatalf("option 1 audio_url = %q", got)
	}
	if got := store.question.Options[1].Description; got != "" {
		t.Fatalf("option 1 description = %q, want empty when media-only option is allowed", got)
	}
}

func TestQuestionToolExecuteRejectsEmptyMediaURLs(t *testing.T) {
	store := &questionSessionStoreStub{}
	tool := NewQuestionTool(store)
	params := json.RawMessage(`{
		"question": "Which asset?",
		"options": [
			{"label": "Broken", "description": "Missing media", "image_url": "   "}
		]
	}`)

	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	result, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Fatal("expected validation failure for blank image_url")
	}
}
