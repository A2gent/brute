package http

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

// preflightTestServer builds a server whose fallback chain is [claude, openai] and
// whose Claude usage comes from the statusLine cache, so limits are fully stubbable.
func preflightTestServer(t *testing.T, usedPercent int, resetsAt time.Time) (*Server, *config.Config) {
	t.Helper()
	server, _ := newUnifiedAgentsTestServer(t)
	claudePath := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", claudePath)

	cachePath := filepath.Join(t.TempDir(), "claude-rate-limits.json")
	cache := `{"rate_limits":{"five_hour":{"used_percentage":` +
		strconv.Itoa(usedPercent) + `,"resets_at":` + strconv.FormatInt(resetsAt.Unix(), 10) + `}}}`
	writeAnthropicRateLimitCache(t, cachePath, cache, time.Now())
	t.Setenv(claudeRateLimitsCachePathEnv, cachePath)

	cfg := server.config
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test-key", ContextWindow: 1000000}
	cfg.Providers[string(config.ProviderFallback)] = config.Provider{
		FallbackChainNodes: []config.FallbackChainNode{
			{Provider: string(config.ProviderAnthropic), Model: "claude-opus-4-8"},
			{Provider: string(config.ProviderOpenAI), Model: "gpt-5.5"},
		},
	}
	return server, cfg
}

// TestPreflightFallbackStartSkipsExhaustedHeadNode covers the reported bug: a new
// session must start on the first usable node instead of failing there first.
func TestPreflightFallbackStartSkipsExhaustedHeadNode(t *testing.T) {
	server, _ := preflightTestServer(t, 100, time.Now().Add(3*time.Hour))
	sess := sessionForPreflight(t, server)

	server.preflightFallbackStart(context.Background(), config.ProviderFallback, sess)

	if got := sessionFallbackStartIndex(sess, config.ProviderFallback); got != 1 {
		t.Fatalf("fallback start = %d, want 1 (advanced past exhausted node)", got)
	}
	persisted, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionFallbackStartIndex(persisted, config.ProviderFallback); got != 1 {
		t.Fatalf("persisted fallback start = %d, want 1", got)
	}
}

func TestPreflightFallbackStartKeepsUsableHeadNode(t *testing.T) {
	server, _ := preflightTestServer(t, 37, time.Now().Add(3*time.Hour))
	sess := sessionForPreflight(t, server)

	server.preflightFallbackStart(context.Background(), config.ProviderFallback, sess)

	if got := sessionFallbackStartIndex(sess, config.ProviderFallback); got != 0 {
		t.Fatalf("fallback start = %d, want 0 (head node still usable)", got)
	}
}

// An exhausted window whose reset already passed must not block the node.
func TestPreflightFallbackStartIgnoresElapsedReset(t *testing.T) {
	server, _ := preflightTestServer(t, 100, time.Now().Add(-time.Minute))
	sess := sessionForPreflight(t, server)

	server.preflightFallbackStart(context.Background(), config.ProviderFallback, sess)

	if got := sessionFallbackStartIndex(sess, config.ProviderFallback); got != 0 {
		t.Fatalf("fallback start = %d, want 0 (limit already reset)", got)
	}
}

// With every node exhausted the chain must still report its aggregate error, so
// the index stays put rather than pointing past the end.
func TestPreflightFallbackStartLeavesIndexWhenAllNodesExhausted(t *testing.T) {
	server, cfg := preflightTestServer(t, 100, time.Now().Add(3*time.Hour))
	cfg.Providers[string(config.ProviderFallback)] = config.Provider{
		FallbackChainNodes: []config.FallbackChainNode{
			{Provider: string(config.ProviderAnthropic), Model: "claude-opus-4-8"},
			{Provider: "anthropic:work", Model: "claude-sonnet-4-6"},
		},
	}
	workPath := filepath.Join(t.TempDir(), "claude-work")
	if err := os.WriteFile(workPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["anthropic:work"] = config.Provider{Name: "anthropic:work", BinaryPath: workPath}
	sess := sessionForPreflight(t, server)

	server.preflightFallbackStart(context.Background(), config.ProviderFallback, sess)

	if got := sessionFallbackStartIndex(sess, config.ProviderFallback); got != 0 {
		t.Fatalf("fallback start = %d, want 0 when no node can serve the run", got)
	}
}

func TestPreflightFallbackStartIgnoresDirectProvider(t *testing.T) {
	server, _ := preflightTestServer(t, 100, time.Now().Add(3*time.Hour))
	sess := sessionForPreflight(t, server)

	server.preflightFallbackStart(context.Background(), config.ProviderAnthropic, sess)

	if got := sessionFallbackStartIndex(sess, config.ProviderAnthropic); got != 0 {
		t.Fatalf("direct provider must not get a chain index, got %d", got)
	}
}

func sessionForPreflight(t *testing.T, server *Server) *session.Session {
	t.Helper()
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata["provider"] = string(config.ProviderFallback)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

// The preflight must change the whole run start, not just metadata: the target's
// context window comes from the node the chain will actually use.
func TestResolveExecutionTargetAppliesFallbackPreflight(t *testing.T) {
	server, _ := preflightTestServer(t, 100, time.Now().Add(3*time.Hour))
	sess := sessionForPreflight(t, server)

	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderFallback, "", "fix tests", sess)
	if err != nil {
		t.Fatalf("resolveExecutionTarget failed: %v", err)
	}
	if got := sessionFallbackStartIndex(sess, config.ProviderFallback); got != 1 {
		t.Fatalf("fallback start = %d, want 1", got)
	}
	if target.ContextWindow != 1000000 {
		t.Fatalf("context window = %d, want the OpenAI node window 1000000", target.ContextWindow)
	}
	if target.Client == nil {
		t.Fatal("target client is missing")
	}
}
