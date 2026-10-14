package config

import "testing"

func TestResolveContextWindowUsesCodexCache(t *testing.T) {
	ResetCodexModelContextCache()
	t.Cleanup(ResetCodexModelContextCache)
	CacheCodexModelContextWindows(map[string]int{"GPT-6-Luna": 272000})

	if got := ResolveContextWindow(ProviderOpenAICodex, Provider{}, "gpt-6-luna"); got != 272000 {
		t.Fatalf("got %d, want 272000", got)
	}
	if got := ResolveContextWindow(ProviderOpenAICodex, Provider{}, "unknown"); got != 1000000 {
		t.Fatalf("fallback got %d, want provider default 1000000", got)
	}
}
