package http

import (
	"context"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
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
