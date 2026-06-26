// provider_clients.go keeps provider resolution and client construction together after splitting server.go.
package http

import (
	"fmt"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/llm/cursorcli"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/session"
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

const llmProviderProxyEnabledSettingKey = "A2GENT_LLM_PROVIDER_PROXY_ENABLED"

func normalizeJobLLMProvider(raw string) string {
	return config.NormalizeProviderRef(raw)
}

func (s *Server) resolveJobProviderType(job *storage.RecurringJob) config.ProviderType {
	if job != nil {
		provider := normalizeJobLLMProvider(job.LLMProvider)
		if provider != "" {
			return config.ProviderType(provider)
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Server) resolveModelForProvider(providerType config.ProviderType) string {
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

func (s *Server) resolveSessionProviderType(sess *session.Session) config.ProviderType {
	if sess != nil && sess.Metadata != nil {
		if raw, ok := sess.Metadata["provider"]; ok {
			if provider, ok := raw.(string); ok && strings.TrimSpace(provider) != "" {
				return config.ProviderType(config.NormalizeProviderRef(provider))
			}
		}
	}
	return config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
}

func (s *Server) resolveSessionModel(sess *session.Session, providerType config.ProviderType) string {
	if sess != nil && sess.Metadata != nil {
		if raw, ok := sess.Metadata["model"]; ok {
			if model, ok := raw.(string); ok && strings.TrimSpace(model) != "" {
				return strings.TrimSpace(model)
			}
		}
	}
	return s.resolveModelForProvider(providerType)
}

func (s *Server) resolveContextWindowForProvider(providerType config.ProviderType, model string) int {
	if providerType == config.ProviderAutoRouter {
		return 0
	}
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

func (s *Server) providerStatefulResponses(providerType config.ProviderType) bool {
	provider := s.config.Providers[string(providerType)]
	return s.providerStatefulResponsesForConfig(providerType, provider.StatefulResponses)
}

func (s *Server) providerStatefulResponsesForConfig(providerType config.ProviderType, configured *bool) bool {

	return false
}

func (s *Server) createLLMClient(providerType config.ProviderType, model string, sess *session.Session) (llm.Client, error) {
	if providerType == config.ProviderAutoRouter {
		return nil, fmt.Errorf("automatic router requires dynamic prompt routing")
	}
	if config.IsFallbackAggregateRef(string(providerType)) || providerType == config.ProviderFallback {
		return s.createFallbackChainClient(providerType, sess)
	}
	client, err := s.createBaseLLMClientForSession(providerType, model, sess)
	if err != nil {
		return nil, err
	}
	retries := s.config.LLMRetries
	if retries <= 0 {
		retries = retry.DefaultMaxRetries
	}
	return retry.Wrap(client, retry.WithMaxRetries(retries)), nil
}

func (s *Server) createBaseLLMClientForSession(providerType config.ProviderType, model string, sess *session.Session) (llm.Client, error) {
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = s.resolveModelForProvider(providerType)
	}
	if client, ok := s.createParentProxyLLMClient(providerType, modelName); ok {
		return client, nil
	}
	if providerType == config.ProviderAnthropic {
		return claudecli.NewClient(modelName, s.resolveSessionWorkDir(sess)), nil
	}
	if providerType == config.ProviderCursor {
		provider := s.config.Providers[string(providerType)]
		return cursorcli.NewClientWithOptions(modelName, cursorcli.Options{
			WorkDir: s.resolveSessionWorkDir(sess),
			APIKey:  firstNonEmpty(strings.TrimSpace(provider.APIKey), s.apiKeyFromEnv(providerType)),
		}), nil
	}
	return s.createBaseLLMClient(providerType, modelName)
}

func (s *Server) createBaseLLMClient(providerType config.ProviderType, model string) (llm.Client, error) {
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
	if client, ok := s.createParentProxyLLMClient(providerType, modelName); ok {
		return client, nil
	}
	if providerType == config.ProviderAnthropic {
		return claudecli.NewClient(modelName, s.resolveSessionWorkDir(nil)), nil
	}
	if providerType == config.ProviderCursor {
		return cursorcli.NewClientWithOptions(modelName, cursorcli.Options{
			WorkDir: s.resolveSessionWorkDir(nil),
			APIKey:  firstNonEmpty(strings.TrimSpace(provider.APIKey), s.apiKeyFromEnv(providerType)),
		}), nil
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" && s.providerSupportsOAuth(providerType) && provider.OAuth != nil {
		apiKey = strings.TrimSpace(provider.OAuth.AccessToken)
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

		baseURL = normalizeOpenAIBaseURL(baseURL)
		return gemini.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI, config.ProviderOpenCodeZen:

		baseURL = normalizeOpenAIBaseURL(baseURL)
		return lmstudio.NewClient(apiKey, modelName, baseURL), nil
	case config.ProviderOpenAICodex:
		return openaicodex.NewClientWithOptions(apiKey, modelName, baseURL, openaicodex.Options{
			PromptCacheKey:    provider.PromptCacheKey,
			ReasoningEffort:   provider.ReasoningEffort,
			TextVerbosity:     provider.TextVerbosity,
			ServiceTier:       provider.ServiceTier,
			MaxTokens:         provider.MaxTokens,
			StatefulResponses: s.providerStatefulResponsesForConfig(providerType, provider.StatefulResponses),
		}), nil
	default:
		return anthropic.NewClientWithBaseURL(apiKey, modelName, baseURL), nil
	}
}

func (s *Server) createFallbackChainClient(providerRef config.ProviderType, sess *session.Session) (llm.Client, error) {
	chain, err := s.fallbackNodesForProvider(providerRef)
	if err != nil {
		return nil, err
	}

	nodes := make([]fallback.Node, 0, len(chain))
	for _, node := range chain {
		ptype := config.ProviderType(node.Provider)
		model := strings.TrimSpace(node.Model)
		client, err := s.createBaseLLMClientForSession(ptype, model, sess)
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
	start := sessionFallbackStartIndex(sess, providerRef)
	return fallback.NewClient(nodes, fallback.WithMaxRetries(retries), fallback.WithStartIndex(start)), nil
}

func (s *Server) createParentProxyLLMClient(providerType config.ProviderType, modelName string) (llm.Client, bool) {
	parentProxyURL := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL"))
	if parentProxyURL == "" {
		return nil, false
	}
	// WHY: Docker sub-agents inherit the parent host's provider through Brute's
	// OpenAI-compatible proxy. Host-only providers such as Claude CLI must run on
	// the parent machine, not inside the child container where the CLI is absent.
	proxyBaseURL := normalizeOpenAIBaseURL(strings.TrimRight(parentProxyURL, "/") + "/providers/" + string(providerType))
	proxyAPIKey := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_KEY"))
	if proxyAPIKey == "" {
		proxyAPIKey = "a2gent-proxy"
	}
	return lmstudio.NewClient(proxyAPIKey, modelName, proxyBaseURL), true
}

func (s *Server) parentProxyAvailable() bool {
	return strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != ""
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

func (s *Server) apiKeyFromEnv(providerType config.ProviderType) string {
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

func (s *Server) apiKeyEnvName(providerType config.ProviderType) string {
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
	default:
		return ""
	}
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

func (s *Server) normalizeAndValidateFallbackChain(raw []config.FallbackChainNode) ([]config.FallbackChainNode, error) {
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

func (s *Server) fallbackChainIsConfigured(chain []config.FallbackChainNode) bool {
	if len(chain) < 2 {
		return false
	}
	validated, err := s.normalizeAndValidateFallbackChain(chain)
	return err == nil && len(validated) >= 2
}

func (s *Server) fallbackNodesForProvider(providerRef config.ProviderType) ([]config.FallbackChainNode, error) {
	ref := config.NormalizeProviderRef(string(providerRef))
	if ref == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		if len(provider.FallbackChainNodes) > 0 {
			return s.normalizeAndValidateFallbackChain(provider.FallbackChainNodes)
		}
		return s.normalizeAndValidateFallbackChain(legacyProvidersToFallbackNodes(provider.FallbackChain, s.resolveModelForProvider))
	}
	if config.IsFallbackAggregateRef(ref) {
		aggregate, _ := s.findFallbackAggregateByRef(ref)
		if aggregate == nil {
			return nil, fmt.Errorf("fallback aggregate not found: %s", ref)
		}
		return s.normalizeAndValidateFallbackChain(aggregate.Chain)
	}
	return nil, fmt.Errorf("provider is not fallback aggregate: %s", ref)
}

func (s *Server) findFallbackAggregateByID(id string) *config.FallbackAggregate {
	normalizedID := config.NormalizeToken(id)
	for i := range s.config.FallbackAggregates {
		if config.NormalizeToken(s.config.FallbackAggregates[i].ID) == normalizedID {
			return &s.config.FallbackAggregates[i]
		}
	}
	return nil
}

func (s *Server) findFallbackAggregateByRef(ref string) (*config.FallbackAggregate, int) {
	id := config.FallbackAggregateIDFromRef(ref)
	if id == "" {
		return nil, -1
	}
	for i := range s.config.FallbackAggregates {
		if config.NormalizeToken(s.config.FallbackAggregates[i].ID) == id {
			return &s.config.FallbackAggregates[i], i
		}
	}
	return nil, -1
}

func (s *Server) providerRefExists(ref string) bool {
	normalized := config.NormalizeProviderRef(ref)
	if config.GetProviderDefinition(config.ProviderType(normalized)) != nil {
		return true
	}
	if config.IsFallbackAggregateRef(normalized) {
		aggregate, _ := s.findFallbackAggregateByRef(normalized)
		return aggregate != nil
	}
	return false
}

func (s *Server) validateProviderRefForExecution(ref string) error {
	normalized := config.NormalizeProviderRef(ref)
	if normalized == "" {
		return fmt.Errorf("provider is empty")
	}
	ptype := config.ProviderType(normalized)
	if def := config.GetProviderDefinition(ptype); def != nil {
		if ptype == config.ProviderFallback {
			_, err := s.fallbackNodesForProvider(ptype)
			return err
		}
		if ptype == config.ProviderAutoRouter {
			provider := s.config.Providers[string(config.ProviderAutoRouter)]
			return s.validateAutoRouterProvider(provider)
		}
		if !s.providerConfiguredForUse(ptype) {
			return fmt.Errorf("provider is not configured")
		}
		return nil
	}
	if config.IsFallbackAggregateRef(normalized) {
		_, err := s.fallbackNodesForProvider(ptype)
		return err
	}
	return fmt.Errorf("provider not found")
}

func (s *Server) providerConfiguredForUse(providerType config.ProviderType) bool {
	def := config.GetProviderDefinition(providerType)
	if def == nil || providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		return false
	}
	if s.parentProxyAvailable() {
		return true
	}
	if providerType == config.ProviderAnthropic {
		return claudecli.IsAvailable()
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

func (s *Server) providerSupportsOAuth(providerType config.ProviderType) bool {
	return providerType == config.ProviderOpenAICodex
}

func (s *Server) adaptProviderErrorMessage(providerType config.ProviderType, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lowerMsg := strings.ToLower(msg)
	if providerType == config.ProviderOpenAICodex && isOpenAICodexExpiredTokenError(lowerMsg) {
		return fmt.Errorf("%s. Reconnect OpenAI Codex in provider settings: /providers/openai_codex", msg)
	}
	if providerType == config.ProviderOpenAICodex && strings.Contains(lowerMsg, "insufficient_quota") {
		return fmt.Errorf("%s. OpenAI accepted the token, but this account/project has no API quota. Codex login access does not automatically grant paid API credits", msg)
	}
	if providerType == config.ProviderOpenAICodex && strings.Contains(lowerMsg, "usage_not_included") {
		return fmt.Errorf("%s. This ChatGPT account does not include Codex usage. Upgrade the ChatGPT plan for Codex access, then reconnect OAuth", msg)
	}
	return err
}

func isOpenAICodexExpiredTokenError(lowerMsg string) bool {
	return strings.Contains(lowerMsg, "token_expired") ||
		(strings.Contains(lowerMsg, "authentication token") && strings.Contains(lowerMsg, "expired")) ||
		(strings.Contains(lowerMsg, "token is expired") && strings.Contains(lowerMsg, "signing in again"))
}
