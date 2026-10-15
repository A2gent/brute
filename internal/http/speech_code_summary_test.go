package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
)

func TestSpokenCodeSummary(t *testing.T) {
	var body map[string]interface{}
	upstream := newReasoningTestCodexServer(t, &body, "Печатает число.")
	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test", BaseURL: upstream.URL, Model: "gpt-4.1"}
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{config: cfg, store: store}
	got := s.summarizeSpokenCode(context.Background(), "Длинный блок кода.", strings.Repeat("print(1)\n", 2000), "ru")
	if got != "Длинный блок кода. Печатает число." {
		t.Fatalf("summary = %q", got)
	}
	if body["model"] != "gpt-4.1" {
		t.Fatalf("model = %v", body["model"])
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(encoded), "print(1)") != 1333 {
		t.Fatalf("code input was not bounded: %s", encoded)
	}
	if !strings.Contains(string(encoded), "Respond in Russian") {
		t.Fatalf("missing language instruction: %s", encoded)
	}
	upstream.Close()
	if got := s.summarizeSpokenCode(context.Background(), "Long code block.", "print(1)", "en"); got != "Long code block." {
		t.Fatalf("provider failure fallback = %q", got)
	}
}

func TestSpokenCodeSummaryFallback(t *testing.T) {
	for _, s := range []*Server{nil, {}, {config: config.DefaultConfig()}} {
		if got := s.summarizeSpokenCode(context.Background(), "Long code block.", "code", "en"); got != "Long code block." {
			t.Fatalf("fallback = %q", got)
		}
	}
}
