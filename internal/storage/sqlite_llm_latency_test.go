package storage

import (
	"testing"
	"time"
)

func TestListLLMLatencySamplesReadsAssistantTimings(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	row := &Session{ID: "latency", AgentID: "build", Status: "running", CreatedAt: now, UpdatedAt: now,
		Messages: []Message{
			{ID: "u", Role: "user", Content: "hi", Timestamp: now, Metadata: map[string]interface{}{"llm_duration_ms": 1}},
			{ID: "a1", Role: "assistant", Timestamp: now, Metadata: map[string]interface{}{"llm_provider": "openai", "llm_ttft_ms": 120, "llm_duration_ms": 900}},
			{ID: "a2", Role: "assistant", Timestamp: now, Metadata: map[string]interface{}{"llm_duration_ms": 400}},
			{ID: "a3", Role: "assistant", Timestamp: now},
		},
	}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}

	samples, err := store.ListLLMLatencySamples(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 assistant samples, got %+v", samples)
	}
	byDuration := map[float64]LLMLatencySample{}
	for _, s := range samples {
		byDuration[s.DurationMS] = s
	}
	if s := byDuration[900]; s.Provider != "openai" || s.TTFTMS == nil || *s.TTFTMS != 120 {
		t.Fatalf("unexpected streamed sample: %+v", s)
	}
	if s := byDuration[400]; s.Provider != "" || s.TTFTMS != nil {
		t.Fatalf("unexpected non-streamed sample: %+v", s)
	}
}
