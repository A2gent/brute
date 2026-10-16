package http

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/go-chi/chi/v5"
)

// Provider usage APIs only return the current snapshot, so Brute keeps its own
// time series of snapshots to let clients chart usage pace and projections.
const (
	providerUsageHistoryDirName      = "provider-usage-history"
	providerUsageHistoryRetention    = 60 * 24 * time.Hour
	providerUsageHistoryHeartbeat    = 30 * time.Minute
	providerUsageHistoryMinInterval  = time.Minute
	providerUsageSamplerInterval     = 5 * time.Minute
	providerUsageSamplerInitialDelay = 30 * time.Second
	providerUsageHistoryDefaultDays  = 30
)

// Providers polled in the background. Custom Claude instances share the same
// Claude Code rate-limit cache as "anthropic", so sampling it covers them.
var providerUsageSampledProviders = []config.ProviderType{
	config.ProviderAnthropic,
	config.ProviderOpenAICodex,
	config.ProviderCursor,
	config.ProviderOpenRouter,
}

type ProviderUsageHistoryBar struct {
	Label         string `json:"label"`
	UsedPercent   int    `json:"used_percent"`
	ResetsAt      string `json:"resets_at,omitempty"`
	WindowSeconds int64  `json:"window_seconds,omitempty"`
}

type ProviderUsageHistorySample struct {
	CheckedAt string                    `json:"checked_at"`
	Bars      []ProviderUsageHistoryBar `json:"bars"`
}

type ProviderUsageHistoryResponse struct {
	Provider      string                       `json:"provider"`
	Samples       []ProviderUsageHistorySample `json:"samples"`
	RetentionDays int                          `json:"retention_days"`
	SampleMinutes int                          `json:"sample_interval_minutes"`
}

type providerUsageHistory struct {
	mu   sync.Mutex
	dir  string
	last map[string]providerUsageHistoryLast
}

type providerUsageHistoryLast struct {
	at     time.Time
	sample ProviderUsageHistorySample
}

func newProviderUsageHistory(dataPath string) *providerUsageHistory {
	if strings.TrimSpace(dataPath) == "" {
		return nil
	}
	return &providerUsageHistory{
		dir:  filepath.Join(dataPath, providerUsageHistoryDirName),
		last: make(map[string]providerUsageHistoryLast),
	}
}

var providerUsageHistoryFileUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (h *providerUsageHistory) path(provider string) string {
	name := providerUsageHistoryFileUnsafe.ReplaceAllString(strings.TrimSpace(provider), "_")
	return filepath.Join(h.dir, name+".jsonl")
}

// record appends a snapshot when it carries new information: values changed, or
// the heartbeat elapsed so flat periods stay visible in charts.
func (h *providerUsageHistory) record(usage ProviderUsageResponse, now time.Time) {
	if h == nil || usage.Status != providerUsageStatusAvailable || len(usage.UsageBars) == 0 {
		return
	}
	at, err := time.Parse(time.RFC3339, usage.CheckedAt)
	if err != nil {
		at = now
	}
	at = at.UTC()
	sample := ProviderUsageHistorySample{CheckedAt: at.Format(time.RFC3339)}
	for _, bar := range usage.UsageBars {
		sample.Bars = append(sample.Bars, ProviderUsageHistoryBar{
			Label:         bar.Label,
			UsedPercent:   bar.UsedPercent,
			ResetsAt:      bar.ResetsAt,
			WindowSeconds: bar.WindowSeconds,
		})
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if last, ok := h.last[usage.Provider]; ok {
		elapsed := at.Sub(last.at)
		// Snapshots taken from a cache file (Claude) reuse the file mtime, so an
		// unchanged cache never produces duplicate points.
		if elapsed <= 0 {
			return
		}
		if elapsed < providerUsageHistoryMinInterval {
			return
		}
		if providerUsageHistoryBarsEqual(last.sample.Bars, sample.Bars) && elapsed < providerUsageHistoryHeartbeat {
			return
		}
	}

	line, err := json.Marshal(sample)
	if err != nil {
		return
	}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		logging.Warn("provider usage history: create dir: %v", err)
		return
	}
	f, err := os.OpenFile(h.path(usage.Provider), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logging.Warn("provider usage history: open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		logging.Warn("provider usage history: write: %v", err)
		return
	}
	h.last[usage.Provider] = providerUsageHistoryLast{at: at, sample: sample}
}

func providerUsageHistoryBarsEqual(a, b []ProviderUsageHistoryBar) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// list returns samples newer than since, oldest first. Expired samples are
// dropped from disk while reading so the file does not grow forever.
func (h *providerUsageHistory) list(provider string, since, now time.Time) []ProviderUsageHistorySample {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	path := h.path(provider)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	cutoff := now.Add(-providerUsageHistoryRetention)
	var kept []ProviderUsageHistorySample
	expired := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var sample ProviderUsageHistorySample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, sample.CheckedAt)
		if err != nil {
			continue
		}
		if at.Before(cutoff) {
			expired++
			continue
		}
		kept = append(kept, sample)
	}
	f.Close()

	if expired > 0 {
		h.rewrite(path, kept)
	}

	result := make([]ProviderUsageHistorySample, 0, len(kept))
	for _, sample := range kept {
		if at, _ := time.Parse(time.RFC3339, sample.CheckedAt); !at.Before(since) {
			result = append(result, sample)
		}
	}
	return result
}

func (h *providerUsageHistory) rewrite(path string, samples []ProviderUsageHistorySample) {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, sample := range samples {
		line, err := json.Marshal(sample)
		if err != nil {
			continue
		}
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	os.Rename(tmp, path)
}

func (s *Server) handleProviderUsageHistory(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if config.GetProviderDefinitionForRef(providerRef) == nil {
		s.errorResponse(w, http.StatusNotFound, "Unknown provider")
		return
	}
	days := providerUsageHistoryDefaultDays
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			s.errorResponse(w, http.StatusBadRequest, "days must be a positive integer")
			return
		}
		days = parsed
	}
	now := time.Now()
	samples := s.usageHistory.list(providerRef, now.Add(-time.Duration(days)*24*time.Hour), now)
	if samples == nil {
		samples = []ProviderUsageHistorySample{}
	}
	s.jsonResponse(w, http.StatusOK, ProviderUsageHistoryResponse{
		Provider:      providerRef,
		Samples:       samples,
		RetentionDays: int(providerUsageHistoryRetention / (24 * time.Hour)),
		SampleMinutes: int(providerUsageSamplerInterval / time.Minute),
	})
}

// runProviderUsageSampler keeps history flowing even when no UI is open.
// Docker child agents skip it: the parent owns credentials and history.
func (s *Server) runProviderUsageSampler(ctx context.Context) {
	if s.usageHistory == nil || s.parentProxyAvailable() {
		return
	}
	timer := time.NewTimer(providerUsageSamplerInitialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for _, providerType := range providerUsageSampledProviders {
			if ctx.Err() != nil {
				return
			}
			if !s.providerConfiguredForUse(providerType) {
				continue
			}
			s.usageHistory.record(s.providerUsageStatus(ctx, providerType), time.Now())
		}
		timer.Reset(providerUsageSamplerInterval)
	}
}
