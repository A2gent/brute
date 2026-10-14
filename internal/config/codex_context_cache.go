package config

import (
	"strings"
	"sync"
)

// codexModelContextCache holds per-model context windows reported by the Codex
// /models endpoint, so limits come from the API instead of the 128k provider default.
var codexModelContextCache struct {
	sync.RWMutex
	models map[string]int
}

// CacheCodexModelContextWindows replaces the cached Codex windows. Empty input is
// ignored so a failed discovery does not wipe previously learned values.
func CacheCodexModelContextWindows(entries map[string]int) {
	next := make(map[string]int, len(entries))
	for id, window := range entries {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && window > 0 {
			next[id] = window
		}
	}
	if len(next) == 0 {
		return
	}
	codexModelContextCache.Lock()
	defer codexModelContextCache.Unlock()
	codexModelContextCache.models = next
}

// ResetCodexModelContextCache clears the cache. Exposed for tests.
func ResetCodexModelContextCache() {
	codexModelContextCache.Lock()
	defer codexModelContextCache.Unlock()
	codexModelContextCache.models = nil
}

func codexCachedContextWindow(modelID string) int {
	codexModelContextCache.RLock()
	defer codexModelContextCache.RUnlock()
	return codexModelContextCache.models[strings.ToLower(strings.TrimSpace(modelID))]
}
