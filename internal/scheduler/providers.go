package scheduler

import (
	"fmt"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/codexauth"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/llm/cursorcli"
	"github.com/A2gent/brute/internal/llm/kimicli"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/storage"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeJobLLMProvider(raw string) string {
	return config.NormalizeProviderRef(raw)
}

func (s *Scheduler) resolveJobProviderType(job *storage.RecurringJob) config.ProviderType {
	if job != nil {
		provider := normalizeJobLLMProvider(job.LLMProvider)
		if provider != "" {
			return config.ProviderType(provider)
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Scheduler) resolveModelForProvider(providerType config.ProviderType) string {
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		return ""
	}
	provider := s.config.Providers[string(providerType)]
	if strings.TrimSpace(provider.Model) != "" {
		return strings.TrimSpace(provider.Model)
	}
	if def := config.GetProviderDefinition(providerType); def != nil && strings.TrimSpace(def.DefaultModel) != "" {
		return strings.TrimSpace(def.DefaultModel)
	}
	return strings.TrimSpace(s.config.DefaultModel)
}

func (s *Scheduler) resolveContextWindowForProvider(providerType config.ProviderType, model string) int {
	provider := s.config.Providers[string(providerType)]
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		chain, err := s.fallbackNodesForProvider(providerType)
		if err != nil {
			return 0
		}
		minContext := 0
		for _, node := range chain {
			nodeType := config.ProviderType(node.Provider)
			if config.GetProviderDefinition(nodeType) == nil {
				continue
			}
			window := config.ResolveContextWindow(nodeType, s.config.Providers[string(nodeType)], node.Model)
			if window <= 0 {
				continue
			}
			if minContext == 0 || window < minContext {
				minContext = window
			}
		}
		return minContext
	}
	return config.ResolveContextWindow(providerType, provider, model)
}

func (s *Scheduler) syncOpenAICodexOAuthFromCache() bool {
	if s == nil || s.config == nil {
		return false
	}
	oauth, _, err := codexauth.Load("")
	if err != nil || oauth == nil || strings.TrimSpace(oauth.AccessToken) == "" {
		return false
	}
	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	current := ""
	if provider.OAuth != nil {
		current = strings.TrimSpace(provider.OAuth.AccessToken)
	}
	if current == strings.TrimSpace(oauth.AccessToken) {
		return true
	}
	// WHY: recurring jobs may run for days while Codex CLI refreshes its token.
	// Keep scheduler clients aligned with the local Codex auth cache.
	provider.OAuth = oauth
	s.config.Providers[string(config.ProviderOpenAICodex)] = provider
	return true
}

func (s *Scheduler) createLLMClient(providerType config.ProviderType, model string, workDir string) (llm.Client, error) {
	if providerType == config.ProviderAutoRouter {
		return nil, fmt.Errorf("automatic router requires dynamic prompt routing")
	}
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		return s.createFallbackChainClient(providerType, workDir)
	}
	client, err := s.createBaseLLMClient(providerType, model, workDir)
	if err != nil {
		return nil, err
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = retry.DefaultMaxRetries
	}
	return retry.Wrap(client, retry.WithMaxRetries(retries)), nil
}

func (s *Scheduler) createBaseLLMClient(providerType config.ProviderType, model string, workDir string) (llm.Client, error) {
	def := config.GetProviderDefinition(providerType)
	if def == nil {
		return nil, fmt.Errorf("unknown provider: %s", providerType)
	}
	if providerType == config.ProviderFallback {
		return nil, fmt.Errorf("fallback aggregate is not a direct provider")
	}

	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	envURLKeys := []string{strings.ToUpper(string(providerType)) + "_BASE_URL"}
	if providerType == config.ProviderLMStudio {
		envURLKeys = append([]string{"LM_STUDIO_BASE_URL"}, envURLKeys...)
	}
	for _, key := range envURLKeys {
		if envURL := strings.TrimSpace(os.Getenv(key)); envURL != "" {
			baseURL = envURL
			break
		}
	}
	if envURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); envURL != "" && providerType == config.ProviderKimi {
		baseURL = envURL
	}
	if providerType == config.ProviderOpenAICodex {
		lower := strings.ToLower(strings.TrimSpace(baseURL))
		if lower == "" || strings.Contains(lower, "api.openai.com") {
			baseURL = strings.TrimSpace(def.DefaultURL)
		}
	}
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = s.resolveModelForProvider(providerType)
	}

	if providerType == config.ProviderAnthropic {
		return claudecli.NewClient(modelName, workDir), nil
	}
	if providerType == config.ProviderKimiCLI {
		return kimicli.NewClient(modelName, workDir), nil
	}
	if providerType == config.ProviderCursor {
		return cursorcli.NewClientWithOptions(modelName, cursorcli.Options{
			WorkDir: workDir,
			APIKey:  firstNonEmpty(strings.TrimSpace(provider.APIKey), s.apiKeyFromEnv(providerType)),
		}), nil
	}

	if parentProxyURL := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")); parentProxyURL != "" {
		proxyBaseURL := normalizeOpenAIBaseURL(strings.TrimRight(parentProxyURL, "/") + "/providers/" + string(providerType))
		proxyAPIKey := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_KEY"))
		if proxyAPIKey == "" {
			proxyAPIKey = "a2gent-proxy"
		}
		return lmstudio.NewClient(proxyAPIKey, modelName, proxyBaseURL), nil
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	oauthBacked := false
	if providerType == config.ProviderOpenAICodex && apiKey == "" && s.syncOpenAICodexOAuthFromCache() {
		provider = s.config.Providers[string(providerType)]
	}
	if apiKey == "" && s.providerSupportsOAuth(providerType) && provider.OAuth != nil {
		apiKey = strings.TrimSpace(provider.OAuth.AccessToken)
		oauthBacked = apiKey != ""
	}
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}
	if providerType == config.ProviderOpenCodeZen && apiKey == "" {
		apiKey = "public"
	}

	if def.RequiresKey && apiKey == "" {
		return nil, fmt.Errorf("%s requires an API key (configure provider API key or set %s)", def.DisplayName, s.apiKeyEnvName(providerType))
	}
	switch providerType {
	case config.ProviderGoogle:
		// Google Gemini uses a dedicated client with OpenAI-compatible API + Gemini extensions
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return gemini.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI, config.ProviderOpenCodeZen, config.ProviderGrok:
		// Other OpenAI-compatible providers
		baseURL = normalizeOpenAIBaseURL(baseURL)
		return lmstudio.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderOpenAICodex:
		options := openaicodex.Options{
			PromptCacheKey:    provider.PromptCacheKey,
			ReasoningEffort:   provider.ReasoningEffort,
			TextVerbosity:     provider.TextVerbosity,
			ServiceTier:       provider.ServiceTier,
			MaxTokens:         provider.MaxTokens,
			StatefulResponses: s.providerStatefulResponses(providerType),
		}
		if oauthBacked {
			options.AccessTokenProvider = func() string {
				if !s.syncOpenAICodexOAuthFromCache() {
					return ""
				}
				provider := s.config.Providers[string(config.ProviderOpenAICodex)]
				if provider.OAuth == nil {
					return ""
				}
				return strings.TrimSpace(provider.OAuth.AccessToken)
			}
		}
		return openaicodex.NewClientWithOptions(apiKey, modelName, baseURL, options), nil
	default:
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	}
}

func (s *Scheduler) createFallbackChainClient(providerRef config.ProviderType, workDir string) (llm.Client, error) {
	chain, err := s.fallbackNodesForProvider(providerRef)
	if err != nil {
		return nil, err
	}

	nodes := make([]fallback.Node, 0, len(chain))
	for _, node := range chain {
		ptype := config.ProviderType(node.Provider)
		model := strings.TrimSpace(node.Model)
		client, err := s.createBaseLLMClient(ptype, model, workDir)
		if err != nil {
			return nil, fmt.Errorf("fallback node %s/%s is not available: %w", node.Provider, model, err)
		}
		nodes = append(nodes, fallback.Node{
			Name:   node.Provider,
			Model:  model,
			Client: client,
		})
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = fallback.DefaultMaxRetries
	}
	return fallback.NewClient(nodes, fallback.WithMaxRetries(retries)), nil
}

func (s *Scheduler) apiKeyFromEnv(providerType config.ProviderType) string {
	envKey := s.apiKeyEnvName(providerType)
	if envKey != "" {
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			return value
		}
	}
	if providerType == config.ProviderGoogle {
		return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	return ""
}

func (s *Scheduler) apiKeyEnvName(providerType config.ProviderType) string {
	switch providerType {
	case config.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case config.ProviderCursor:
		return "CURSOR_API_KEY"
	case config.ProviderKimi:
		return "KIMI_API_KEY"
	case config.ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case config.ProviderOpenCodeZen:
		return "OPENCODE_API_KEY"
	case config.ProviderGoogle:
		return "GOOGLE_API_KEY"
	case config.ProviderOpenAI:
		return "OPENAI_API_KEY"
	case config.ProviderOpenAICodex:
		return "OPENAI_API_KEY"
	case config.ProviderGrok:
		return "XAI_API_KEY"
	default:
		return ""
	}
}

func (s *Scheduler) providerStatefulResponses(providerType config.ProviderType) bool {
	// Keep OpenAI Codex stateless in background jobs; the Codex backend requires
	// full local conversation state for tool-heavy agent runs.
	return false
}

func normalizeFallbackChainNodes(raw []config.FallbackChainNode) []config.FallbackChainNode {
	chain := make([]config.FallbackChainNode, 0, len(raw))
	for _, item := range raw {
		provider := config.NormalizeProviderRef(item.Provider)
		model := strings.TrimSpace(item.Model)
		if provider == "" || model == "" {
			continue
		}
		chain = append(chain, config.FallbackChainNode{Provider: provider, Model: model})
	}
	return chain
}

func normalizeOpenAIBaseURL(raw string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case strings.HasSuffix(baseURL, "/models"):
		baseURL = strings.TrimSuffix(baseURL, "/models")
	case strings.HasSuffix(baseURL, "/chat/completions"):
		baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	}
	return strings.TrimSpace(baseURL)
}

func legacyProvidersToFallbackNodes(raw []string, resolveModel func(config.ProviderType) string) []config.FallbackChainNode {
	nodes := make([]config.FallbackChainNode, 0, len(raw))
	for _, provider := range raw {
		normalizedProvider := config.NormalizeProviderRef(provider)
		if normalizedProvider == "" {
			continue
		}
		model := strings.TrimSpace(resolveModel(config.ProviderType(normalizedProvider)))
		if model == "" {
			continue
		}
		nodes = append(nodes, config.FallbackChainNode{Provider: normalizedProvider, Model: model})
	}
	return nodes
}

func (s *Scheduler) normalizeAndValidateFallbackChain(raw []config.FallbackChainNode) ([]config.FallbackChainNode, error) {
	chain := normalizeFallbackChainNodes(raw)
	if len(chain) < 2 {
		return nil, fmt.Errorf("fallback chain must contain at least two model nodes")
	}

	seen := make(map[string]struct{}, len(chain))
	for _, node := range chain {
		key := node.Provider + "::" + node.Model
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("fallback chain nodes must not repeat: %s/%s", node.Provider, node.Model)
		}
		seen[key] = struct{}{}

		ptype := config.ProviderType(node.Provider)
		if ptype == config.ProviderFallback {
			return nil, fmt.Errorf("fallback chain cannot include fallback_chain itself")
		}
		def := config.GetProviderDefinition(ptype)
		if def == nil {
			return nil, fmt.Errorf("unsupported provider in fallback chain: %s", node.Provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return nil, fmt.Errorf("provider %s is not configured or missing required credentials", node.Provider)
		}
	}
	return chain, nil
}

func (s *Scheduler) fallbackNodesForProvider(providerRef config.ProviderType) ([]config.FallbackChainNode, error) {
	ref := config.NormalizeProviderRef(string(providerRef))
	if ref == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		if len(provider.FallbackChainNodes) > 0 {
			return s.normalizeAndValidateFallbackChain(provider.FallbackChainNodes)
		}
		return s.normalizeAndValidateFallbackChain(legacyProvidersToFallbackNodes(provider.FallbackChain, s.resolveModelForProvider))
	}
	if config.IsFallbackAggregateRef(ref) {
		id := config.FallbackAggregateIDFromRef(ref)
		for _, aggregate := range s.config.FallbackAggregates {
			if config.NormalizeToken(aggregate.ID) == id {
				return s.normalizeAndValidateFallbackChain(aggregate.Chain)
			}
		}
		return nil, fmt.Errorf("fallback aggregate not found: %s", ref)
	}
	return nil, fmt.Errorf("provider is not fallback aggregate: %s", ref)
}

func (s *Scheduler) providerConfiguredForUse(providerType config.ProviderType) bool {
	def := config.GetProviderDefinition(providerType)
	if def == nil || providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		return false
	}
	if providerType == config.ProviderAnthropic {
		return claudecli.IsAvailable()
	}
	if providerType == config.ProviderKimiCLI {
		return kimicli.IsAvailable()
	}
	if providerType == config.ProviderCursor {
		return cursorcli.IsAvailable()
	}
	provider := s.config.Providers[string(providerType)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(def.DefaultURL)
	}
	if baseURL == "" {
		return false
	}
	if !def.RequiresKey {
		return true
	}
	if s.providerSupportsOAuth(providerType) && provider.OAuth != nil && strings.TrimSpace(provider.OAuth.AccessToken) != "" {
		return true
	}
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(providerType)
	}
	return apiKey != ""
}

func (s *Scheduler) providerSupportsOAuth(providerType config.ProviderType) bool {
	return providerType == config.ProviderOpenAICodex
}
