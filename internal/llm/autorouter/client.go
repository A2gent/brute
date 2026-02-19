package autorouter

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

// Client routes each request to a target provider based on configured routing rules.
type Client struct {
	cfg          *config.Config
	createClient func(providerRef string, modelOverride string) (llm.Client, string, error)
}

// New creates an automatic-router client.
func New(cfg *config.Config, createClient func(providerRef string, modelOverride string) (llm.Client, string, error)) *Client {
	return &Client{
		cfg:          cfg,
		createClient: createClient,
	}
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	target, routedReq, err := c.resolveTarget(ctx, request)
	if err != nil {
		return nil, err
	}
	return target.Client.Chat(ctx, routedReq)
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	target, routedReq, err := c.resolveTarget(ctx, request)
	if err != nil {
		return nil, err
	}
	streamClient, ok := target.Client.(llm.StreamingClient)
	if !ok {
		resp, chatErr := target.Client.Chat(ctx, routedReq)
		if chatErr != nil {
			return nil, chatErr
		}
		if onEvent != nil {
			if strings.TrimSpace(resp.Content) != "" {
				_ = onEvent(llm.StreamEvent{Type: llm.StreamEventContentDelta, ContentDelta: resp.Content})
			}
			_ = onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: resp.Usage})
		}
		return resp, nil
	}
	return streamClient.ChatStream(ctx, routedReq, onEvent)
}

func (c *Client) resolveTarget(ctx context.Context, request *llm.ChatRequest) (*resolvedTarget, *llm.ChatRequest, error) {
	if request == nil {
		return nil, nil, fmt.Errorf("chat request is nil")
	}
	if c.cfg == nil {
		return nil, nil, fmt.Errorf("automatic router requires config")
	}
	if c.createClient == nil {
		return nil, nil, fmt.Errorf("automatic router client factory is not configured")
	}

	autoCfg := c.cfg.Providers[string(config.ProviderAutoRouter)]
	if err := validateAutoRouterProvider(autoCfg); err != nil {
		return nil, nil, err
	}

	rules := normalizeRouterRules(autoCfg.RouterRules)
	userPrompt := extractUserPrompt(request.Messages)
	chosen, reason := c.selectRoutingRule(ctx, userPrompt, autoCfg, rules)
	if chosen == nil {
		return nil, nil, fmt.Errorf("automatic router could not resolve a route")
	}

	targetClient, targetModel, err := c.createClient(chosen.Provider, strings.TrimSpace(chosen.Model))
	if err != nil {
		return nil, nil, fmt.Errorf("automatic router target %s/%s is unavailable: %w", chosen.Provider, strings.TrimSpace(chosen.Model), err)
	}

	cloned := *request
	if targetModel != "" {
		cloned.Model = targetModel
	} else {
		cloned.Model = strings.TrimSpace(chosen.Model)
	}

	logging.Info("Automatic router selected target provider=%s model=%s rule=%q reason=%s", chosen.Provider, cloned.Model, chosen.Match, reason)
	return &resolvedTarget{
		Provider: chosen.Provider,
		Model:    cloned.Model,
		Client:   targetClient,
	}, &cloned, nil
}

type resolvedTarget struct {
	Provider string
	Model    string
	Client   llm.Client
}

func validateAutoRouterProvider(provider config.Provider) error {
	routerProvider := config.NormalizeProviderRef(provider.RouterProvider)
	if routerProvider == "" {
		return fmt.Errorf("automatic router requires router_provider")
	}
	if routerProvider == string(config.ProviderAutoRouter) {
		return fmt.Errorf("automatic router cannot use automatic_router as router provider")
	}
	routerProviderType := config.ProviderType(routerProvider)
	if (config.IsFallbackAggregateRef(routerProvider) || routerProviderType == config.ProviderFallback) && strings.TrimSpace(provider.RouterModel) != "" {
		return fmt.Errorf("router_model is not allowed when router_provider is a fallback chain")
	}
	if !config.IsFallbackAggregateRef(routerProvider) && config.GetProviderDefinition(routerProviderType) == nil {
		return fmt.Errorf("router_provider is unsupported: %s", routerProvider)
	}

	rules := normalizeRouterRules(provider.RouterRules)
	if len(rules) == 0 {
		return fmt.Errorf("automatic router requires at least one routing rule")
	}
	for _, rule := range rules {
		targetProvider := config.NormalizeProviderRef(rule.Provider)
		targetType := config.ProviderType(targetProvider)
		if targetType == config.ProviderAutoRouter {
			return fmt.Errorf("routing rule %q cannot target automatic_router", rule.Match)
		}
		if (config.IsFallbackAggregateRef(targetProvider) || targetType == config.ProviderFallback) && strings.TrimSpace(rule.Model) != "" {
			return fmt.Errorf("routing rule %q targets a fallback chain and must not set model", rule.Match)
		}
		if !config.IsFallbackAggregateRef(targetProvider) && config.GetProviderDefinition(targetType) == nil {
			return fmt.Errorf("routing rule %q has unsupported provider: %s", rule.Match, rule.Provider)
		}
	}

	return nil
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

func extractUserPrompt(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.EqualFold(msg.Role, "user") && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func (c *Client) selectRoutingRule(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string) {
	if len(rules) == 0 {
		return nil, ""
	}
	if len(rules) == 1 {
		return &rules[0], "single rule"
	}
	if userPrompt == "" {
		return &rules[0], "empty prompt fallback"
	}

	rule, reason, err := c.selectRoutingRuleViaLLM(ctx, userPrompt, autoCfg, rules)
	if err == nil && rule != nil {
		return rule, reason
	}
	if err != nil {
		logging.Warn("Automatic router model decision failed, using keyword fallback: %v", err)
	}
	return selectRoutingRuleByKeyword(userPrompt, rules), "keyword fallback"
}

func (c *Client) selectRoutingRuleViaLLM(ctx context.Context, userPrompt string, autoCfg config.Provider, rules []config.RouterRule) (*config.RouterRule, string, error) {
	routerProviderRef := config.NormalizeProviderRef(autoCfg.RouterProvider)
	routerProviderType := config.ProviderType(routerProviderRef)
	routerModel := strings.TrimSpace(autoCfg.RouterModel)
	if config.IsFallbackAggregateRef(routerProviderRef) || routerProviderType == config.ProviderFallback {
		routerModel = ""
	}

	routerClient, _, err := c.createClient(routerProviderRef, routerModel)
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

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
