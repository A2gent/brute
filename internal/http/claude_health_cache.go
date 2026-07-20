package http

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/llm/claudecli"
)

type claudeHealthCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]claudeHealthCacheEntry
}

type claudeHealthCacheEntry struct {
	report    claudecli.HealthReport
	expiresAt time.Time
}

func newClaudeHealthCache() *claudeHealthCache {
	ttl := 5 * time.Minute
	if raw := os.Getenv("AAGENT_CLAUDE_HEALTH_CACHE_TTL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			ttl = parsed
		} else if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			ttl = time.Duration(seconds) * time.Second
		}
	}
	return &claudeHealthCache{ttl: ttl, items: make(map[string]claudeHealthCacheEntry)}
}

func (c *claudeHealthCache) Get(key string) (claudecli.HealthReport, bool) {
	if c == nil {
		return claudecli.HealthReport{}, false
	}
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || now.After(entry.expiresAt) {
		delete(c.items, key)
		return claudecli.HealthReport{}, false
	}
	report := entry.report
	report.Cached = true
	report.ExpiresAt = entry.expiresAt
	return report, true
}

func (c *claudeHealthCache) Set(key string, report claudecli.HealthReport) claudecli.HealthReport {
	if c == nil {
		return report
	}
	now := time.Now().UTC()
	expiresAt := now.Add(c.ttl)
	report.CheckedAt = now
	report.ExpiresAt = expiresAt
	report.Cached = false
	c.mu.Lock()
	c.items[key] = claudeHealthCacheEntry{report: report, expiresAt: expiresAt}
	c.mu.Unlock()
	return report
}

func (c *claudeHealthCache) InvalidatePrefix(prefix string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.items {
		if prefix == "" || strings.HasPrefix(key, prefix) {
			delete(c.items, key)
		}
	}
}
