package http

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func latencySample(provider string, ttft, duration float64) storage.LLMLatencySample {
	return storage.LLMLatencySample{Provider: provider, TTFTMS: &ttft, DurationMS: duration}
}

func TestAggregateLLMLatencyEmpty(t *testing.T) {
	if got := aggregateLLMLatency(nil); got != nil {
		t.Fatalf("expected nil for no samples, got %+v", got)
	}
}

func TestAggregateLLMLatencySingleRequest(t *testing.T) {
	got := aggregateLLMLatency([]storage.LLMLatencySample{latencySample("openai", 300, 2000)})
	want := LLMLatencyStats{Count: 1, TTFTCount: 1, TTFTP50MS: 300, TTFTP90MS: 300, DurationP50MS: 2000}
	if got.LLMLatencyStats != want {
		t.Fatalf("got %+v, want %+v", got.LLMLatencyStats, want)
	}
	if len(got.Providers) != 1 || got.Providers[0].Provider != "openai" || got.Providers[0].LLMLatencyStats != want {
		t.Fatalf("unexpected providers: %+v", got.Providers)
	}
}

func TestAggregateLLMLatencyOutlierMovesOnlyP90(t *testing.T) {
	var samples []storage.LLMLatencySample
	for i := 1; i <= 9; i++ {
		samples = append(samples, latencySample("anthropic", float64(i*100), 1000))
	}
	samples = append(samples, latencySample("kimi", 60000, 90000))
	samples = append(samples, storage.LLMLatencySample{Provider: "kimi", DurationMS: 1000})

	got := aggregateLLMLatency(samples)
	if got.Count != 11 || got.TTFTCount != 10 {
		t.Fatalf("unexpected counts: %+v", got.LLMLatencyStats)
	}
	if got.TTFTP50MS != 500 || got.DurationP50MS != 1000 {
		t.Fatalf("median must ignore the outlier: %+v", got.LLMLatencyStats)
	}
	if got.TTFTP90MS != 900 {
		t.Fatalf("expected nearest-rank p90 of 900, got %v", got.TTFTP90MS)
	}
	if len(got.Providers) != 2 || got.Providers[0].Provider != "anthropic" || got.Providers[1].Provider != "kimi" {
		t.Fatalf("providers should be ordered by request count: %+v", got.Providers)
	}
	if kimi := got.Providers[1]; kimi.Count != 2 || kimi.TTFTCount != 1 || kimi.TTFTP50MS != 60000 {
		t.Fatalf("unexpected kimi stats: %+v", kimi)
	}
}
