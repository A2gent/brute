package jev

import (
	"context"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

type fakeChat struct {
	reply   string
	request *llm.ChatRequest
}

func (f *fakeChat) Chat(_ context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	f.request = request
	return &llm.ChatResponse{Content: f.reply, Usage: llm.TokenUsage{InputTokens: 7, OutputTokens: 3}}, nil
}

func TestLLMBackendDerivesJevAnswers(t *testing.T) {
	chat := &fakeChat{reply: "<think>hmm</think>```json\n" +
		`{"pick": {"a": 0.2, "b": 0.6, "zzz": 5}, "level": {"0": 0, "1": 1, "2": 1}, "ok": {"yes": "0.9", "no": 0.1}}` + "\n```"}
	client := NewLLMClient(chat, "local-model")

	resp, err := client.SystemOne(context.Background(), SystemOneRequest{
		State: "some state",
		Questions: map[string]Question{
			"pick":  {Type: "choice", Instructions: "pick one", Criteria: map[string]string{"a": "A", "b": "B"}},
			"level": {Type: "score", Instructions: "rate", Criteria: []string{"low", "mid", "high"}},
			"ok":    {Type: "noul", Instructions: "is it ok?"},
		},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if chat.request.Model != "local-model" || !strings.Contains(chat.request.Messages[0].Content, "some state") {
		t.Fatalf("unexpected chat request: %+v", chat.request)
	}

	pick := resp.Answers["pick"]
	// The unoffered "zzz" option is dropped and the rest renormalized.
	if pick.Choice != "b" || pick.Confidence < 0.74 || pick.Confidence > 0.76 || len(pick.Probabilities) != 2 {
		t.Fatalf("choice answer = %+v", pick)
	}
	if level := resp.Answers["level"]; level.Score == nil || *level.Score != 1.5 {
		t.Fatalf("score answer = %+v", level)
	}
	if ok := resp.Answers["ok"]; ok.Noul == nil || *ok.Noul < 0.89 || *ok.Noul > 0.91 {
		t.Fatalf("noul answer = %+v", ok)
	}
	if resp.Usage.InputTokens != 7 {
		t.Fatalf("usage not propagated: %+v", resp.Usage)
	}
}

func TestLLMBackendRejectsMissingAnswer(t *testing.T) {
	client := NewLLMClient(&fakeChat{reply: `{"other": {"yes": 1}}`}, "m")
	_, err := client.SystemOne(context.Background(), SystemOneRequest{
		State:     "x",
		Questions: map[string]Question{"ok": {Type: "noul", Instructions: "?"}},
	})
	if err == nil {
		t.Fatal("expected an error so callers fall back to ordinary processing")
	}
}

func TestLLMBackendAcceptsBareAnswers(t *testing.T) {
	client := NewLLMClient(&fakeChat{reply: `{"pick": "B", "level": 2, "ok": 0.3}`}, "small")
	resp, err := client.SystemOne(context.Background(), SystemOneRequest{
		State: "x",
		Questions: map[string]Question{
			"pick":  {Type: "choice", Instructions: "?", Criteria: map[string]string{"a": "A", "b": "B"}},
			"level": {Type: "score", Instructions: "?", Criteria: []string{"l", "m", "h"}},
			"ok":    {Type: "noul", Instructions: "?"},
		},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if resp.Answers["pick"].Choice != "b" || *resp.Answers["level"].Score != 2 || *resp.Answers["ok"].Noul != 0.3 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
}
