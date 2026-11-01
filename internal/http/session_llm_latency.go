package http

import (
	"math"
	"sort"

	"github.com/A2gent/brute/internal/storage"
)

type LLMLatencyStats struct {
	Count         int     `json:"count"`
	TTFTCount     int     `json:"ttft_count"`
	TTFTP50MS     float64 `json:"ttft_p50_ms,omitempty"`
	TTFTP90MS     float64 `json:"ttft_p90_ms,omitempty"`
	DurationP50MS float64 `json:"duration_p50_ms"`
}

type ProviderLLMLatencyStats struct {
	Provider string `json:"provider"`
	LLMLatencyStats
}

type LLMLatencyPayload struct {
	LLMLatencyStats
	Providers []ProviderLLMLatencyStats `json:"providers,omitempty"`
}

func aggregateLLMLatency(samples []storage.LLMLatencySample) *LLMLatencyPayload {
	if len(samples) == 0 {
		return nil
	}
	byProvider := map[string][]storage.LLMLatencySample{}
	for _, sample := range samples {
		byProvider[sample.Provider] = append(byProvider[sample.Provider], sample)
	}
	payload := &LLMLatencyPayload{LLMLatencyStats: latencyStats(samples)}
	for provider, group := range byProvider {
		payload.Providers = append(payload.Providers, ProviderLLMLatencyStats{Provider: provider, LLMLatencyStats: latencyStats(group)})
	}
	sort.Slice(payload.Providers, func(i, j int) bool {
		if payload.Providers[i].Count != payload.Providers[j].Count {
			return payload.Providers[i].Count > payload.Providers[j].Count
		}
		return payload.Providers[i].Provider < payload.Providers[j].Provider
	})
	return payload
}

func latencyStats(samples []storage.LLMLatencySample) LLMLatencyStats {
	durations := make([]float64, 0, len(samples))
	var ttfts []float64
	for _, sample := range samples {
		durations = append(durations, sample.DurationMS)
		if sample.TTFTMS != nil {
			ttfts = append(ttfts, *sample.TTFTMS)
		}
	}
	return LLMLatencyStats{
		Count:         len(durations),
		TTFTCount:     len(ttfts),
		TTFTP50MS:     percentile(ttfts, 0.5),
		TTFTP90MS:     percentile(ttfts, 0.9),
		DurationP50MS: percentile(durations, 0.5),
	}
}

// percentile uses nearest-rank so a single slow request moves p90 but never the median.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(rank, 0)]
}
