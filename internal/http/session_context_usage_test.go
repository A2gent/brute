package http

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestSessionResponseRejectsLegacyClaudeUncachedContext(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	for _, tc := range []struct {
		name, provider               string
		current, input, cached, want int
	}{
		{"legacy routed Claude", "anthropic", 20, 20, 696847, 0},
		{"legacy Claude instance", "anthropic:work", 20, 20, 696847, 0},
		{"normalized Claude", "anthropic", 700000, 700000, 696847, 700000},
		{"latest CLI turn", "anthropic", 30000, 700000, 696847, 30000},
		{"other provider", "openai", 20, 20, 696847, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := session.New("build")
			sess.Metadata["provider"] = "automatic_router"
			sess.Metadata["fallback_active_provider"] = tc.provider
			sess.Metadata["last_llm_provider"] = tc.provider
			sess.Metadata["last_llm_input_tokens"] = tc.input
			sess.Metadata["last_llm_cached_input_tokens"] = tc.cached
			sess.Metadata["current_context_tokens"] = tc.current
			if got := server.sessionToResponse(sess).CurrentContextTokens; got != tc.want {
				t.Fatalf("current context = %d, want %d", got, tc.want)
			}
			if sess.Metadata["current_context_tokens"] != tc.current {
				t.Fatal("response shaping mutated historical usage")
			}
		})
	}
}
