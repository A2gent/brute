package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

type executionTarget struct {
	ProviderType  config.ProviderType
	Model         string
	ContextWindow int
	Client        llm.Client
}

func normalizeRouterRules(raw []config.RouterRule) []config.RouterRule {
	rules := make([]config.RouterRule, 0, len(raw))
	for _, rule := range raw {
		match := strings.TrimSpace(rule.Match)
		provider := config.NormalizeProviderRef(rule.Provider)
		model := strings.TrimSpace(rule.Model)
		if match == "" || provider == "" {
			continue
		}
		rules = append(rules, config.RouterRule{Match: match, Provider: provider, Model: model})
	}
	return rules
}

func (s *Server) normalizeAndValidateRouterRules(raw []config.RouterRule) ([]config.RouterRule, error) {
	rules := normalizeRouterRules(raw)
	if len(rules) == 0 {
		return nil, fmt.Errorf("automatic router requires at least one routing rule")
	}

	for i := range rules {
		rule := &rules[i]
		ptype := config.ProviderType(rule.Provider)
		if ptype == config.ProviderAutoRouter {
			return nil, fmt.Errorf("routing rule %q cannot target automatic_router", rule.Match)
		}
		if config.IsFallbackAggregateRef(rule.Provider) || ptype == config.ProviderFallback {
			if strings.TrimSpace(rule.Model) != "" {
				return nil, fmt.Errorf("routing rule %q targets a fallback chain and must not set model", rule.Match)
			}
			if _, err := s.fallbackNodesForProvider(ptype); err != nil {
				return nil, fmt.Errorf("routing rule %q has invalid fallback target: %w", rule.Match, err)
			}
			continue
		}

		def := config.GetProviderDefinition(ptype)
		if def == nil {
			return nil, fmt.Errorf("routing rule %q has unsupported provider: %s", rule.Match, rule.Provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return nil, fmt.Errorf("routing rule %q uses provider %s that is not configured", rule.Match, rule.Provider)
		}
		if strings.TrimSpace(rule.Model) == "" {
			rule.Model = s.resolveModelForProvider(ptype)
		}
		if strings.TrimSpace(rule.Model) == "" {
			return nil, fmt.Errorf("routing rule %q requires a model for provider %s", rule.Match, rule.Provider)
		}
	}

	return rules, nil
}

func (s *Server) validateAutoRouterProvider(provider config.Provider) error {
	routerProvider := config.NormalizeProviderRef(provider.RouterProvider)
	if routerProvider == "" {
		return fmt.Errorf("automatic router requires router_provider")
	}
	if routerProvider == string(config.ProviderAutoRouter) {
		return fmt.Errorf("automatic router cannot use automatic_router as router provider")
	}
	routerProviderType := config.ProviderType(routerProvider)
	if config.IsFallbackAggregateRef(routerProvider) || routerProviderType == config.ProviderFallback {
		if strings.TrimSpace(provider.RouterModel) != "" {
			return fmt.Errorf("router_model is not allowed when router_provider is a fallback chain")
		}
		if _, err := s.fallbackNodesForProvider(routerProviderType); err != nil {
			return fmt.Errorf("invalid router_provider: %w", err)
		}
	} else {
		def := config.GetProviderDefinition(routerProviderType)
		if def == nil {
			return fmt.Errorf("router_provider is unsupported: %s", routerProvider)
		}
		if !s.providerConfiguredForUse(routerProviderType) {
			return fmt.Errorf("router_provider %s is not configured", routerProvider)
		}
	}
	_, err := s.normalizeAndValidateRouterRules(provider.RouterRules)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) autoRouterConfigured(provider config.Provider) bool {
	return s.validateAutoRouterProvider(provider) == nil
}

func (s *Server) resolveExecutionTarget(ctx context.Context, providerType config.ProviderType, model string, userPrompt string) (*executionTarget, error) {
	requestedModel := strings.TrimSpace(model)
	if providerType != config.ProviderAutoRouter {
		if requestedModel == "" {
			requestedModel = s.resolveModelForProvider(providerType)
		}
		client, err := s.createLLMClient(providerType, requestedModel)
		if err != nil {
			return nil, err
		}
		return &executionTarget{
			ProviderType:  providerType,
			Model:         requestedModel,
			ContextWindow: s.resolveContextWindowForProvider(providerType),
			Client:        client,
		}, nil
	}

	autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
	if err := s.validateAutoRouterProvider(autoCfg); err != nil {
		return nil, err
	}
	rules, _ := s.normalizeAndValidateRouterRules(autoCfg.RouterRules)
	chosen, reason := s.selectRoutingRule(ctx, strings.TrimSpace(userPrompt), autoCfg, rules)
	if chosen == nil {
		return nil, fmt.Errorf("automatic router could not resolve a route")
	}

	targetProvider := config.ProviderType(config.NormalizeProviderRef(chosen.Provider))
	targetModel := strings.TrimSpace(chosen.Model)
	if targetModel == "" {
		targetModel = s.resolveModelForProvider(targetProvider)
	}
	client, err := s.createLLMClient(targetProvider, targetModel)
	if err != nil {
		return nil, fmt.Errorf("automatic router target %s/%s is unavailable: %w", targetProvider, targetModel, err)
	}
	logging.Info("Automatic router selected target provider=%s model=%s rule=%q reason=%s", targetProvider, targetModel, chosen.Match, reason)
	return &executionTarget{
		ProviderType:  targetProvider,
		Model:         targetModel,
		ContextWindow: s.resolveContextWindowForProvider(targetProvider),
		Client:        client,
	}, nil
}

func (s *Server) selectRoutingRule(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string) {
	if len(rules) == 0 {
		return nil, ""
	}
	if len(rules) == 1 {
		return &rules[0], "single rule"
	}
	if userPrompt == "" {
		return &rules[0], "empty prompt fallback"
	}

	rule, reason, err := s.selectRoutingRuleViaLLM(ctx, userPrompt, autoCfg, rules)
	if err == nil && rule != nil {
		return rule, reason
	}
	if err != nil {
		logging.Warn("Automatic router model decision failed, using keyword fallback: %v", err)
	}
	return selectRoutingRuleByKeyword(userPrompt, rules), "keyword fallback"
}

func (s *Server) selectRoutingRuleViaLLM(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string, error) {
	routerProviderRef := config.NormalizeProviderRef(autoCfg.RouterProvider)
	routerProviderType := config.ProviderType(routerProviderRef)
	routerModel := strings.TrimSpace(autoCfg.RouterModel)
	if config.IsFallbackAggregateRef(routerProviderRef) || routerProviderType == config.ProviderFallback {
		routerModel = ""
	}

	routerClient, err := s.createLLMClient(routerProviderType, routerModel)
	if err != nil {
		return nil, "", fmt.Errorf("failed to initialize router provider: %w", err)
	}

	type indexedRule struct {
		Index  int    `json:"index"`
		Match  string `json:"match"`
		Target string `json:"target"`
	}
	indexed := make([]indexedRule, 0, len(rules))
	for i, rule := range rules {
		target := rule.Provider
		if strings.TrimSpace(rule.Model) != "" {
			target = target + "/" + strings.TrimSpace(rule.Model)
		}
		indexed = append(indexed, indexedRule{Index: i + 1, Match: rule.Match, Target: target})
	}
	rulesJSON, _ := json.Marshal(indexed)

	req := &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "You are a strict model router. Choose exactly one routing rule index that best matches the user prompt intent. Return JSON only: {\"index\":<number>,\"reason\":\"short\"}."},
			{Role: "user", Content: fmt.Sprintf("Rules: %s\n\nUser prompt: %s", string(rulesJSON), userPrompt)},
		},
		Temperature: 0,
		MaxTokens:   120,
	}
	if routerModel != "" {
		req.Model = routerModel
	}

	resp, err := routerClient.Chat(ctx, req)
	if err != nil {
		return nil, "", err
	}
	choice, err := parseRouterChoice(resp.Content)
	if err != nil {
		return nil, "", err
	}
	if choice.Index < 1 || choice.Index > len(rules) {
		return nil, "", fmt.Errorf("router returned out-of-range index: %d", choice.Index)
	}
	selected := rules[choice.Index-1]
	return &selected, strings.TrimSpace(choice.Reason), nil
}

type routerChoice struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

func parseRouterChoice(raw string) (*routerChoice, error) {
	content := strings.TrimSpace(raw)
	if content == "" {
		return nil, fmt.Errorf("empty router response")
	}
	if start := strings.Index(content, "{"); start >= 0 {
		if end := strings.LastIndex(content, "}"); end > start {
			content = content[start : end+1]
		}
	}
	var choice routerChoice
	if err := json.Unmarshal([]byte(content), &choice); err == nil {
		return &choice, nil
	}
	content = strings.Trim(content, "` ")
	if n, err := strconv.Atoi(content); err == nil {
		return &routerChoice{Index: n}, nil
	}
	return nil, fmt.Errorf("invalid router response: %s", raw)
}

func selectRoutingRuleByKeyword(userPrompt string, rules []config.RouterRule) *config.RouterRule {
	if len(rules) == 0 {
		return nil
	}
	prompt := strings.ToLower(strings.TrimSpace(userPrompt))
	bestIndex := 0
	bestScore := -1
	for i, rule := range rules {
		match := strings.ToLower(strings.TrimSpace(rule.Match))
		if match == "" {
			continue
		}
		score := 0
		if strings.Contains(prompt, match) {
			score = len(match) + 10
		}
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	selected := rules[bestIndex]
	return &selected
}
