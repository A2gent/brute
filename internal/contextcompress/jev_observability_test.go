package contextcompress

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestAdmissionJevTraceAndSavings(t *testing.T) {
	for _, confidence := range []float64{.95, .5} {
		client, _, _ := fakeJev(t, confidence, http.StatusOK)
		original := numberedOutput(600, func(int) string { return "" })
		got := NewCompressor(Config{Enabled: true}).AdmitToolResultWithGate(context.Background(), gateSession(), llm.ToolResult{Name: "read", Content: original, Metadata: map[string]interface{}{"existing": true}}, 8000, &RelevanceGate{Client: client})
		raw, _ := json.Marshal(got.Metadata)
		var metadata struct {
			Requests []json.RawMessage `json:"jev_requests"`
			Gate     struct {
				Outcome string `json:"outcome"`
				Saved   int    `json:"saved_tokens_approx"`
			} `json:"jev_admission"`
		}
		_ = json.Unmarshal(raw, &metadata)
		if len(metadata.Requests) == 0 || got.Metadata["existing"] != true {
			t.Fatalf("missing trace or existing metadata: %s", raw)
		}
		if confidence > .85 {
			if metadata.Gate.Outcome != "filtered" || metadata.Gate.Saved <= 0 {
				t.Fatalf("missing savings: %s", raw)
			}
		} else if metadata.Gate.Outcome != "fallback" || metadata.Gate.Saved != 0 {
			t.Fatalf("fallback must not attribute cap savings to Jev: %s", raw)
		}
	}
}
