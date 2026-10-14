package config

import (
	"strings"
	"sync"
)

var anthropicModelContextCache struct {
	sync.RWMutex
	models map[string]int
}

// CacheAnthropicModelContextWindows stores limits reported by Anthropic's Models API.
func CacheAnthropicModelContextWindows(entries map[string]int) {
	if len(entries) == 0 {
		return
	}
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
	anthropicModelContextCache.Lock()
	defer anthropicModelContextCache.Unlock()
	anthropicModelContextCache.models = next
}

// ResetAnthropicModelContextCache clears cached API values. It is exported for tests.
func ResetAnthropicModelContextCache() {
	anthropicModelContextCache.Lock()
	defer anthropicModelContextCache.Unlock()
	anthropicModelContextCache.models = nil
}

func anthropicCachedContextWindow(modelID string) int {
	anthropicModelContextCache.RLock()
	defer anthropicModelContextCache.RUnlock()
	return anthropicModelContextCache.models[strings.ToLower(strings.TrimSpace(modelID))]
}
