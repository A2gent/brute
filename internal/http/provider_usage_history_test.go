package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

func usageSnapshot(at time.Time, used int) ProviderUsageResponse {
	return ProviderUsageResponse{
		Provider:  "openai_codex",
		Status:    providerUsageStatusAvailable,
		CheckedAt: at.UTC().Format(time.RFC3339),
		UsageBars: []ProviderUsageBar{{Label: "Codex 5h", UsedPercent: used, ResetsAt: "2026-10-16T15:00:00Z", WindowSeconds: 18000}},
	}
}

func TestProviderUsageHistoryRecordsChangesAndHeartbeats(t *testing.T) {
	h := newProviderUsageHistory(t.TempDir())
	start := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

	h.record(usageSnapshot(start, 10), start)
	h.record(usageSnapshot(start.Add(5*time.Minute), 10), start)                 // unchanged, before heartbeat: skipped
	h.record(usageSnapshot(start.Add(10*time.Minute), 12), start)                // changed: kept
	h.record(usageSnapshot(start.Add(10*time.Minute+20*time.Second), 13), start) // too soon: skipped
	h.record(usageSnapshot(start.Add(45*time.Minute), 12), start)                // unchanged, heartbeat elapsed: kept
	unavailable := usageSnapshot(start.Add(50*time.Minute), 50)
	unavailable.Status = providerUsageStatusUnavailable
	h.record(unavailable, start)

	samples := h.list("openai_codex", start.Add(-time.Hour), start.Add(time.Hour))
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3: %+v", len(samples), samples)
	}
	if samples[1].Bars[0].UsedPercent != 12 || samples[1].Bars[0].WindowSeconds != 18000 {
		t.Fatalf("unexpected second sample: %+v", samples[1])
	}
}

func TestProviderUsageHistoryListPrunesExpiredSamples(t *testing.T) {
	dir := t.TempDir()
	h := newProviderUsageHistory(dir)
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	old := now.Add(-providerUsageHistoryRetention - time.Hour)

	h.record(usageSnapshot(old, 5), old)
	h.record(usageSnapshot(now.Add(-time.Hour), 20), now)

	samples := h.list("openai_codex", time.Time{}, now)
	if len(samples) != 1 || samples[0].Bars[0].UsedPercent != 20 {
		t.Fatalf("unexpected samples after prune: %+v", samples)
	}
	body, err := os.ReadFile(filepath.Join(dir, providerUsageHistoryDirName, "openai_codex.jsonl"))
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	if lines := strings.Count(string(body), "\n"); lines != 1 {
		t.Fatalf("history file lines = %d, want 1 after prune", lines)
	}
}

func TestProviderUsageHistoryEndpointReturnsSamples(t *testing.T) {
	s := &Server{config: config.DefaultConfig(), usageHistory: newProviderUsageHistory(t.TempDir())}
	now := time.Now().UTC()
	s.usageHistory.record(usageSnapshot(now.Add(-2*time.Hour), 30), now)

	router := chi.NewRouter()
	router.Get("/providers/{providerType}/usage/history", s.handleProviderUsageHistory)

	req := httptest.NewRequest(http.MethodGet, "/providers/openai_codex/usage/history?days=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ProviderUsageHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Samples) != 1 || resp.Samples[0].Bars[0].UsedPercent != 30 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	req = httptest.NewRequest(http.MethodGet, "/providers/openai_codex/usage/history?days=abc", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid days status = %d, want 400", rec.Code)
	}
}

func TestClaudeRateLimitUsageBarsIncludeWindow(t *testing.T) {
	bars := claudeRateLimitUsageBars(claudeRateLimitCache{RateLimits: claudeRateLimitWindows{
		FiveHour: &claudeRateLimitWindow{UsedPercentage: 40, ResetsAt: 1792152000},
		SevenDay: &claudeRateLimitWindow{UsedPercentage: 10},
	}})
	if len(bars) != 2 {
		t.Fatalf("bars = %+v", bars)
	}
	if bars[0].WindowSeconds != 5*3600 || bars[0].ResetsAt != time.Unix(1792152000, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("unexpected 5h window: %+v", bars[0])
	}
	if bars[1].WindowSeconds != 7*24*3600 || bars[1].ResetsAt != "" {
		t.Fatalf("unexpected weekly window: %+v", bars[1])
	}
}
