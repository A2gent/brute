package lmstudio

import "testing"

func TestEffectiveMaxTokensOpenRouterUnlimited(t *testing.T) {
	or := &Client{baseURL: "https://openrouter.ai/api/v1"}
	if got := or.effectiveMaxTokens(0); got != 0 {
		t.Fatalf("openrouter default = %d, want 0", got)
	}
	if got := or.effectiveMaxTokens(100); got != 100 {
		t.Fatalf("explicit cap = %d, want 100", got)
	}
	lm := &Client{baseURL: "http://localhost:1234/v1"}
	if got := lm.effectiveMaxTokens(0); got != defaultMaxTokens {
		t.Fatalf("lmstudio default = %d", got)
	}
	proxy := &Client{baseURL: "http://host.docker.internal:5445/providers/openrouter/v1", ErrorLabel: "openrouter (via parent proxy)"}
	if got := proxy.effectiveMaxTokens(0); got != 0 {
		t.Fatalf("parent proxy default = %d, want 0", got)
	}
}
