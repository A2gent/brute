package config

import (
	"strings"
	"sync"
)

// openRouterModelContextCache stores per-model context window sizes fetched
// from the OpenRouter /api/v1/models endpoint. The cache is populated lazily
// when the model list is fetched and consulted in modelContextWindow so that
// every OpenRouter model gets its real context limit instead of the
// conservative 128k provider default.
var openRouterModelContextCache struct {
	sync.RWMutex
	models map[string]int // model id -> context window
}

// CacheOpenRouterModelContextWindow stores a model context window entry.
func CacheOpenRouterModelContextWindow(modelID string, contextWindow int) {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" || contextWindow <= 0 {
		return
	}
	openRouterModelContextCache.Lock()
	defer openRouterModelContextCache.Unlock()
	if openRouterModelContextCache.models == nil {
		openRouterModelContextCache.models = make(map[string]int)
	}
	openRouterModelContextCache.models[modelID] = contextWindow
}

// CacheOpenRouterModelContextWindows bulk-stores model context windows.
func CacheOpenRouterModelContextWindows(entries map[string]int) {
	if len(entries) == 0 {
		return
	}
	openRouterModelContextCache.Lock()
	defer openRouterModelContextCache.Unlock()
	if openRouterModelContextCache.models == nil {
		openRouterModelContextCache.models = make(map[string]int, len(entries))
	}
	for id, window := range entries {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && window > 0 {
			openRouterModelContextCache.models[id] = window
		}
	}
}

// ResetOpenRouterModelContextCache clears the cache. Exposed for tests.
func ResetOpenRouterModelContextCache() {
	openRouterModelContextCache.Lock()
	defer openRouterModelContextCache.Unlock()
	openRouterModelContextCache.models = nil
}

// openRouterCachedContextWindow returns the cached context window for the
// given OpenRouter model id, or 0 if no entry exists.
//
// OpenRouter ids appear in several shapes in config and UI
// (owl-alpha, openrouter/owl-alpha, openrouter/openrouter/owl-alpha,
// xiaomi/mimo-v2.5 vs openrouter/xiaomi/mimo-v2.5). Try all reasonable
// variants so a hit on any stored form wins.
func openRouterCachedContextWindow(modelID string) int {
	openRouterModelContextCache.RLock()
	defer openRouterModelContextCache.RUnlock()
	if openRouterModelContextCache.models == nil {
		return 0
	}
	for _, candidate := range openRouterModelIDCandidates(modelID) {
		if window := openRouterModelContextCache.models[candidate]; window > 0 {
			return window
		}
	}
	return 0
}

// openRouterModelIDCandidates returns lookup keys for an OpenRouter model id,
// covering repeated "openrouter/" prefixes and the bare vendor/model form.
func openRouterModelIDCandidates(modelID string) []string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if normalized == "" {
		return nil
	}

	seen := make(map[string]struct{}, 4)
	out := make([]string, 0, 4)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	add(normalized)

	stripped := normalized
	for strings.HasPrefix(stripped, "openrouter/") {
		stripped = strings.TrimPrefix(stripped, "openrouter/")
		add(stripped)
	}
	if stripped != "" && stripped == normalized {
		// No prefix present: also try the common openrouter/<id> cache key.
		add("openrouter/" + normalized)
	} else if stripped != "" {
		add("openrouter/" + stripped)
	}

	return out
}
