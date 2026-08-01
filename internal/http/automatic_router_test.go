package http

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func TestParseRouterChoiceRejectsTruncatedJSON(t *testing.T) {
	if _, err := parseRouterChoice(`{"index":`); err == nil {
		t.Fatal("expected truncated router JSON to fail")
	}
}

func TestSelectRoutingRuleRejectsEmptyPrompt(t *testing.T) {
	server := &Server{}
	rules := []config.RouterRule{
		{Match: "documentation", Provider: "google"},
		{Match: "coding", Provider: "cursor"},
	}

	if _, _, err := server.selectRoutingRule(context.Background(), "", config.Provider{}, rules); err == nil {
		t.Fatal("expected empty prompt routing to fail")
	}
}

func TestAutomaticRouterPromptClassifiesPrimaryDeliverable(t *testing.T) {
	for _, required := range []string{
		"primary action",
		"documentation rule only when the primary deliverable",
		"coding or refactoring rule",
		"even when the feature itself concerns Markdown",
		"Never return 0, -1",
	} {
		if !strings.Contains(automaticRouterSystemPrompt, required) {
			t.Fatalf("automatic router prompt is missing %q", required)
		}
	}
}

func TestRouterDecisionStreamEventIncludesVisibleDecision(t *testing.T) {
	event := routerDecisionStreamEvent(config.ProviderAutoRouter, &executionTarget{
		ProviderType:  config.ProviderCursor,
		Model:         "composer-2.5",
		RoutingRule:   "coding, mid complexity",
		RoutingReason: "The primary action is editing source code.",
	})
	if event == nil {
		t.Fatal("expected automatic-router event")
	}
	if event.Type != "router_decision" || event.RoutedProvider != "cursor" || event.RoutedModel != "composer-2.5" {
		t.Fatalf("unexpected router event: %#v", event)
	}
	if event.RoutedRule != "coding, mid complexity" || event.RoutedReason == "" {
		t.Fatalf("router event omitted decision context: %#v", event)
	}
}

func TestAutoRouterAcceptsCustomClaudeAsRouterProviderAndRule(t *testing.T) {
	claudePath := filepath.Join(t.TempDir(), "claude-work")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	customRef := "anthropic:work"
	cfg.Providers[customRef] = config.Provider{
		Name:       customRef,
		BinaryPath: claudePath,
		Model:      "claude-sonnet-4-6",
	}
	server := &Server{config: cfg}
	autoCfg := config.Provider{
		RouterProvider: customRef,
		RouterModel:    "claude-sonnet-4-6",
		RouterRules: []config.RouterRule{
			{Match: "coding", Provider: customRef},
		},
	}

	if err := server.validateAutoRouterProvider(autoCfg); err != nil {
		t.Fatalf("validateAutoRouterProvider(custom Claude): %v", err)
	}
	rules, err := server.normalizeAndValidateRouterRules(autoCfg.RouterRules)
	if err != nil {
		t.Fatalf("normalizeAndValidateRouterRules(custom Claude): %v", err)
	}
	if len(rules) != 1 || rules[0].Provider != customRef || rules[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("unexpected normalized custom Claude rule: %+v", rules)
	}
}

func TestNormalizeRouterRulesPreservesReasoningEffort(t *testing.T) {
	rules := normalizeRouterRules([]config.RouterRule{{
		Match:           " coding ",
		Provider:        " openai_codex ",
		Model:           " gpt-5.6-codex ",
		ReasoningEffort: " high ",
	}})

	if len(rules) != 1 || rules[0].ReasoningEffort != "high" {
		t.Fatalf("reasoning effort was not normalized: %+v", rules)
	}
}

func TestNormalizeFallbackChainNodesPreservesReasoningEffort(t *testing.T) {
	nodes := normalizeFallbackChainNodes([]config.FallbackChainNode{{
		Provider:        " openai_codex ",
		Model:           " gpt-5.6-codex ",
		ReasoningEffort: " xhigh ",
	}})

	if len(nodes) != 1 || nodes[0].ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning effort was not normalized: %+v", nodes)
	}
}

func TestResolveExecutionTargetCarriesRuleReasoningEffortToAgentConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:1",
		Model:   "gpt-5.6-codex",
	}
	cfg.Providers[string(config.ProviderAutoRouter)] = config.Provider{
		RouterProvider: string(config.ProviderOpenAICodex),
		RouterModel:    "gpt-5.6-codex",
		RouterRules: []config.RouterRule{{
			Match:           "coding",
			Provider:        string(config.ProviderOpenAICodex),
			Model:           "gpt-5.6-codex",
			ReasoningEffort: "high",
		}},
	}
	server := &Server{config: cfg}

	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderAutoRouter, "", "fix tests", nil)
	if err != nil {
		t.Fatalf("resolveExecutionTarget failed: %v", err)
	}
	if target.ReasoningEffort != "high" {
		t.Fatalf("target reasoning effort = %q, want high", target.ReasoningEffort)
	}
	agentCfg := server.agentConfigFromTarget(nil, target, "", 1, 0)
	if agentCfg.ReasoningEffort != "high" {
		t.Fatalf("agent reasoning effort = %q, want high", agentCfg.ReasoningEffort)
	}
}

func TestResolveExecutionTargetUsesSessionReasoningEffortOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:            string(config.ProviderOpenAICodex),
		APIKey:          "test-key",
		BaseURL:         "http://127.0.0.1:1",
		Model:           "gpt-5.6-codex",
		ReasoningEffort: "low",
	}
	server := &Server{config: cfg}
	sess := session.New("build")
	sess.Metadata["reasoning_effort"] = " high "

	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderOpenAICodex, "gpt-5.6-codex", "", sess)
	if err != nil {
		t.Fatalf("resolveExecutionTarget failed: %v", err)
	}
	if target.ReasoningEffort != "high" {
		t.Fatalf("target reasoning effort = %q, want high", target.ReasoningEffort)
	}
}

func TestResolveExecutionTargetPreservesProviderReasoningEffortWhenSessionUnset(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:            string(config.ProviderOpenAICodex),
		APIKey:          "test-key",
		BaseURL:         "http://127.0.0.1:1",
		Model:           "gpt-5.6-codex",
		ReasoningEffort: "medium",
	}
	server := &Server{config: cfg}

	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderOpenAICodex, "gpt-5.6-codex", "", session.New("build"))
	if err != nil {
		t.Fatalf("resolveExecutionTarget failed: %v", err)
	}
	if target.ReasoningEffort != "medium" {
		t.Fatalf("target reasoning effort = %q, want medium", target.ReasoningEffort)
	}
}
