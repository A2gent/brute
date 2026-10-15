package jev

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm/lmstudio"
)

// Runs only against a live LM Studio, e.g.:
// SYSTEM_ONE_LMSTUDIO_MODEL=google/gemma-4-e4b go test -run LMStudio ./internal/llm/jev
func TestLLMBackendLMStudioIntegration(t *testing.T) {
	model := os.Getenv("SYSTEM_ONE_LMSTUDIO_MODEL")
	if model == "" {
		t.Skip("SYSTEM_ONE_LMSTUDIO_MODEL not set")
	}
	baseURL := os.Getenv("LMSTUDIO_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	client := NewLLMClient(lmstudio.NewClient("", model, baseURL), model)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	started := time.Now()
	resp, err := client.SystemOne(ctx, SystemOneRequest{
		State: "User request: please fix the failing unit test in parser_test.go and run go test",
		Questions: map[string]Question{
			"intent": {Type: "choice", Instructions: "What does the user want?", Criteria: map[string]string{
				"code": "Change or debug source code", "email": "Write or read email", "image": "Generate an image"}},
			"relevance": {Type: "score", Instructions: "How relevant is a code editing toolset?", Criteria: []string{"irrelevant", "tangential", "useful", "highly relevant", "essential"}},
			"browser":   {Type: "noul", Instructions: "Does this request need a web browser?"},
		},
	})
	if err != nil {
		t.Fatalf("SystemOne via LM Studio: %v", err)
	}
	t.Logf("model=%s took=%s answers=%+v", model, time.Since(started), resp.Answers)
	if got := resp.Answers["intent"].Choice; got != "code" {
		t.Errorf("intent = %q, want code", got)
	}
	if s := resp.Answers["relevance"].Score; s == nil || *s < 2 {
		t.Errorf("relevance score = %v, want >= 2", s)
	}
	if n := resp.Answers["browser"].Noul; n == nil || *n > 0.5 {
		t.Errorf("browser noul = %v, want <= 0.5", n)
	}
}
