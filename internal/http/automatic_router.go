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
	"github.com/A2gent/brute/internal/session"
)

type executionTarget struct {
	ProviderType            config.ProviderType
	Model                   string
	ReasoningEffort         string
	RoutingRule             string
	RoutingReason           string
	ContextWindow           int
	StatefulResponses       bool
	ProviderSessions        bool
	ProviderSessionIdentity string
	Client                  llm.Client
}

func routerDecisionStreamEvent(requestedProvider config.ProviderType, target *executionTarget) *ChatStreamEvent {
	if requestedProvider != config.ProviderAutoRouter || target == nil {
		return nil
	}
	return &ChatStreamEvent{
		Type:           "router_decision",
		RoutedProvider: strings.TrimSpace(string(target.ProviderType)),
		RoutedModel:    strings.TrimSpace(target.Model),
		RoutedRule:     strings.TrimSpace(target.RoutingRule),
		RoutedReason:   strings.TrimSpace(target.RoutingReason),
	}
}

func normalizeRouterRules(raw []config.RouterRule) []config.RouterRule {
	rules := make([]config.RouterRule, 0, len(raw))
	for _, rule := range raw {
		match := strings.TrimSpace(rule.Match)
		provider := config.NormalizeProviderRef(rule.Provider)
		model := strings.TrimSpace(rule.Model)
		reasoningEffort := strings.TrimSpace(rule.ReasoningEffort)
		if match == "" || provider == "" {
			continue
		}
		rules = append(rules, config.RouterRule{
			Match:           match,
			Provider:        provider,
			Model:           model,
			ReasoningEffort: reasoningEffort,
		})
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

		def := config.GetProviderDefinitionForRef(rule.Provider)
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
		def := config.GetProviderDefinitionForRef(routerProvider)
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

func (s *Server) defaultSessionProviderRef() string {
	if active := config.NormalizeProviderRef(s.config.ActiveProvider); active != "" {
		return active
	}
	autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
	if s.autoRouterConfigured(autoCfg) {
		return string(config.ProviderAutoRouter)
	}
	return ""
}

func (s *Server) resolveExecutionTarget(ctx context.Context, providerType config.ProviderType, model string, userPrompt string, sess *session.Session) (*executionTarget, error) {
	requestedModel := strings.TrimSpace(model)
	if providerType != config.ProviderAutoRouter {
		if requestedModel == "" {
			requestedModel = s.resolveModelForProvider(providerType)
		}
		client, err := s.createLLMClient(providerType, requestedModel, sess)
		if err != nil {
			return nil, err
		}
		reasoningEffort := strings.TrimSpace(s.config.Providers[string(providerType)].ReasoningEffort)
		if sessionEffort := sessionReasoningEffort(sess); sessionEffort != "" {
			reasoningEffort = sessionEffort
		}
		return &executionTarget{
			ProviderType:            providerType,
			Model:                   requestedModel,
			ReasoningEffort:         reasoningEffort,
			ContextWindow:           s.resolveContextWindowForProvider(providerType, requestedModel),
			StatefulResponses:       s.providerStatefulResponses(providerType),
			ProviderSessions:        s.providerSessionPersistence(providerType),
			ProviderSessionIdentity: s.claudeProviderSessionIdentity(string(providerType)),
			Client:                  client,
		}, nil
	}

	autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
	if err := s.validateAutoRouterProvider(autoCfg); err != nil {
		return nil, err
	}
	rules, _ := s.normalizeAndValidateRouterRules(autoCfg.RouterRules)
	chosen, reason, err := s.selectRoutingRule(ctx, strings.TrimSpace(userPrompt), autoCfg, rules)
	if err != nil {
		logging.Error("Automatic router model decision failed: %v", err)
		return nil, fmt.Errorf("automatic router failed: %w", err)
	}
	if chosen == nil {
		return nil, fmt.Errorf("automatic router could not resolve a route")
	}

	targetProvider := config.ProviderType(config.NormalizeProviderRef(chosen.Provider))
	targetModel := strings.TrimSpace(chosen.Model)
	if targetModel == "" {
		targetModel = s.resolveModelForProvider(targetProvider)
	}
	client, err := s.createLLMClient(targetProvider, targetModel, sess)
	if err != nil {
		return nil, fmt.Errorf("automatic router target %s/%s is unavailable: %w", targetProvider, targetModel, err)
	}
	logging.Info("Automatic router selected target provider=%s model=%s rule=%q reason=%s", targetProvider, targetModel, chosen.Match, reason)
	return &executionTarget{
		ProviderType:            targetProvider,
		Model:                   targetModel,
		ReasoningEffort:         strings.TrimSpace(chosen.ReasoningEffort),
		RoutingRule:             strings.TrimSpace(chosen.Match),
		RoutingReason:           strings.TrimSpace(reason),
		ContextWindow:           s.resolveContextWindowForProvider(targetProvider, targetModel),
		StatefulResponses:       s.providerStatefulResponses(targetProvider),
		ProviderSessions:        s.providerSessionPersistence(targetProvider),
		ProviderSessionIdentity: s.claudeProviderSessionIdentity(string(targetProvider)),
		Client:                  client,
	}, nil
}

func (s *Server) selectRoutingRule(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string, error) {
	if len(rules) == 0 {
		return nil, "", fmt.Errorf("no routing rules are configured")
	}
	if len(rules) == 1 {
		return &rules[0], "single rule", nil
	}
	if userPrompt == "" {
		return nil, "", fmt.Errorf("cannot classify an empty prompt")
	}

	rule, reason, err := s.selectRoutingRuleViaLLM(ctx, userPrompt, autoCfg, rules)
	if err != nil {
		return nil, "", err
	}
	if rule == nil {
		return nil, "", fmt.Errorf("router returned no rule")
	}
	return rule, reason, nil
}

func (s *Server) selectRoutingRuleViaLLM(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string, error) {
	routerProviderRef := config.NormalizeProviderRef(autoCfg.RouterProvider)
	routerProviderType := config.ProviderType(routerProviderRef)
	routerModel := strings.TrimSpace(autoCfg.RouterModel)
	if config.IsFallbackAggregateRef(routerProviderRef) || routerProviderType == config.ProviderFallback {
		routerModel = ""
	}

	routerClient, err := s.createLLMClient(routerProviderType, routerModel, nil)
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
		ReasoningEffort: strings.TrimSpace(autoCfg.RouterReasoningEffort),
		Messages: []llm.Message{
			{Role: "system", Content: automaticRouterSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("Rules: %s\n\nUser prompt: %s", string(rulesJSON), userPrompt)},
		},
		Temperature: 0,
		// Gemini thinking models can consume a small output budget before emitting the
		// JSON answer. Keep this aligned with the standalone automatic-router client.
		MaxTokens: 2048,
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
		return nil, "", fmt.Errorf("router returned out-of-range index %d; expected 1-%d", choice.Index, len(rules))
	}
	selected := rules[choice.Index-1]
	return &selected, strings.TrimSpace(choice.Reason), nil
}

const automaticRouterSystemPrompt = `You are a strict model router. Classify the primary action the user is asking the agent to perform and choose exactly one of the supplied routing rules.

Important classification rules:
- Classify by the requested deliverable and action, not by incidental words, filenames, or subject matter.
- Choose a documentation rule only when the primary deliverable is prose documentation, reference material, or explanatory text.
- Choose a coding or refactoring rule when the user asks to create, edit, move, remove, debug, test, or review source code, even when the feature itself concerns Markdown, docs, or text files.
- Choose only a 1-based index that exists in the supplied Rules array. Never return 0, -1, or an invented rule.

Return exactly one complete JSON object on one line and nothing else:
{"index":<1-based integer>,"reason":"brief primary-intent explanation"}`

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
