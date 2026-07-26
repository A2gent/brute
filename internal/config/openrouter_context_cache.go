package config

import "sync"

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
func openRouterCachedContextWindow(modelID string) int {
	openRouterModelContextCache.RLock()
	defer openRouterModelContextCache.RUnlock()
	if openRouterModelContextCache.models == nil {
		return 0
	}
	return openRouterModelContextCache.models[modelID]
}
