package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/codexauth"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/autorouter"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/llm/cursorcli"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/logging"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) showProviderSelection() (tea.Model, tea.Cmd) {
	m.showProviderMenu = true
	m.providerMenuIndex = 0
	m.providerMenuStep = 0
	m.providerInput = ""

	// Find current provider in list
	providers := config.SupportedProviders()
	for i, p := range providers {
		if string(p.Type) == m.appConfig.ActiveProvider {
			m.providerMenuIndex = i
			break
		}
	}

	return m, nil
}

// showModelsSelection shows the models selection menu
func (m Model) showModelsSelection() (tea.Model, tea.Cmd) {
	// Check if we have a valid provider configured
	if m.appConfig == nil || m.appConfig.ActiveProvider == "" {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "No provider configured. Use /provider first.",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	// For LM Studio, fetch models from the API
	if m.appConfig.ActiveProvider == string(config.ProviderLMStudio) {
		return m.fetchLMStudioModels()
	}

	// For OpenAI Codex, fetch the live catalog (curated + account-discovered).
	if m.appConfig.ActiveProvider == string(config.ProviderOpenAICodex) {
		return m.fetchOpenAICodexModels()
	}

	// For other providers, show known models
	return m.showStaticModels()
}

// fetchOpenAICodexModels loads the Codex model catalog using the same shared
// discovery the web API uses, so the terminal and web stay in sync (curated list
// for OAuth, plus live /models discovery in API-key mode).
func (m Model) fetchOpenAICodexModels() (tea.Model, tea.Cmd) {
	opts := openaicodex.ModelCatalogOptions{}
	if provider := m.appConfig.GetActiveProvider(); provider != nil {
		opts.BaseURL = strings.TrimSpace(provider.BaseURL)
		opts.APIKey = strings.TrimSpace(provider.APIKey)
	}
	if opts.BaseURL == "" {
		if def := config.GetProviderDefinition(config.ProviderOpenAICodex); def != nil {
			opts.BaseURL = def.DefaultURL
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.availableModels = openaicodex.ListModelCatalog(ctx, opts)

	m.showModelsMenu = true
	m.modelsMenuIndex = 0
	for i, model := range m.availableModels {
		if model == m.appConfig.DefaultModel {
			m.modelsMenuIndex = i
			break
		}
	}
	return m, nil
}

// fetchLMStudioModels fetches models from LM Studio API
func (m Model) fetchLMStudioModels() (tea.Model, tea.Cmd) {
	provider := m.appConfig.GetActiveProvider()
	baseURL := "http://localhost:1234/v1"
	if provider != nil && provider.BaseURL != "" {
		baseURL = provider.BaseURL
	}

	client := lmstudio.NewClient("", "", baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   fmt.Sprintf("Failed to fetch models from LM Studio: %v", err),
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.availableModels = make([]string, len(models))
	for i, model := range models {
		m.availableModels[i] = model.ID
	}

	if len(m.availableModels) == 0 {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "No models loaded in LM Studio. Please load a model first.",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	m.showModelsMenu = true
	m.modelsMenuIndex = 0

	// Find current model in list
	currentModel := m.appConfig.DefaultModel
	for i, model := range m.availableModels {
		if model == currentModel {
			m.modelsMenuIndex = i
			break
		}
	}

	return m, nil
}

// showStaticModels shows models for providers with known model lists
func (m Model) showStaticModels() (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(config.ProviderType(m.appConfig.ActiveProvider))
	if providerDef == nil {
		m.messages = append(m.messages, message{
			role:      "error",
			content:   "Unknown provider",
			timestamp: time.Now(),
		})
		m.viewport.SetContent(m.renderMessages())
		return m, nil
	}

	// Define known models for each provider
	switch config.ProviderType(m.appConfig.ActiveProvider) {
	case config.ProviderKimi:
		m.availableModels = []string{"kimi-k2.5", "kimi-k2", "kimi-for-coding"}
	case config.ProviderOpenRouter:
		m.availableModels = []string{
			"openrouter/auto",
			"anthropic/claude-opus-4-8",
			"anthropic/claude-sonnet-5",
			"anthropic/claude-opus-4-6",
			"anthropic/claude-sonnet-4-6",
			"openai/gpt-4.1",
			"openai/gpt-4.1-mini",
			"google/gemini-3-flash-preview",
			"google/gemini-2.5-pro",
			"meta-llama/llama-4-maverick",
		}
	case config.ProviderCursor:
		m.availableModels = []string{"composer-2.5", "composer-latest", "auto"}
	case config.ProviderAnthropic:
		// Pull the live catalog from the official Anthropic Models API when an
		// API key is available (config or ANTHROPIC_API_KEY); otherwise fall
		// back to a curated current list. CLI aliases lead the list.
		apiKey := ""
		if provider := m.appConfig.GetActiveProvider(); provider != nil {
			apiKey = strings.TrimSpace(provider.APIKey)
		}
		if apiKey == "" {
			apiKey = providerAPIKeyFromEnv(config.ProviderAnthropic)
		}
		m.availableModels = anthropic.CLIModels(apiKey)
	case config.ProviderGoogle:
		m.availableModels = []string{
			"gemini-3-pro-preview",
			"gemini-3-flash-preview",
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.5-flash-image",
			"gemini-2.5-flash-lite",
			"gemini-2.0-flash",
			"gemini-2.0-flash-lite",
		}
	case config.ProviderOpenAI:
		m.availableModels = []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4o-mini"}
	default:
		m.availableModels = []string{providerDef.DefaultModel}
	}

	m.showModelsMenu = true
	m.modelsMenuIndex = 0

	// Find current model
	for i, model := range m.availableModels {
		if model == m.appConfig.DefaultModel {
			m.modelsMenuIndex = i
			break
		}
	}

	return m, nil
}
func (m Model) selectProvider(providerType config.ProviderType) (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(providerType)
	if providerDef == nil {
		return m, nil
	}

	m.selectedProviderType = string(providerType)

	// Check if provider is already configured with credentials
	existingProvider := m.appConfig.Providers[string(providerType)]

	// For LM Studio, prompt for URL
	if providerType == config.ProviderLMStudio {
		if existingProvider.BaseURL == "" {
			m.providerMenuStep = 2 // Go to URL input
			m.providerInput = providerDef.DefaultURL
			return m, nil
		}
	}

	// For providers requiring credentials
	if providerDef.RequiresKey && !providerHasCredentials(providerType, existingProvider) {
		m.providerMenuStep = 1 // Go to API key input
		return m, nil
	}

	// Provider is ready, activate it
	return m.activateProvider(providerType)
}

// activateProvider activates the selected provider
func (m Model) activateProvider(providerType config.ProviderType) (tea.Model, tea.Cmd) {
	providerDef := config.GetProviderDefinition(providerType)
	if providerDef == nil {
		return m, nil
	}

	m.appConfig.ActiveProvider = string(providerType)
	m.appConfig.DefaultModel = providerDef.DefaultModel
	provider := m.appConfig.Providers[string(providerType)]
	m.contextWindow = config.ResolveContextWindow(providerType, provider, providerDef.DefaultModel)
	m.agentConfig.Model = providerDef.DefaultModel
	m.agentConfig.ContextWindow = m.contextWindow

	// Save config
	if err := m.appConfig.Save(config.GetConfigPath()); err != nil {
		logging.Error("Failed to save config: %v", err)
	}

	// Create new LLM client for this provider
	m.llmClient = m.createLLMClient(providerType)

	// Update agent with new client
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)

	m.showProviderMenu = false
	m.providerMenuStep = 0

	m.messages = append(m.messages, message{
		role:      "system",
		content:   fmt.Sprintf("Switched to %s (model: %s)", providerDef.DisplayName, m.appConfig.DefaultModel),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())

	return m, nil
}

// createLLMClient creates an LLM client for the given provider type
func (m Model) createLLMClient(providerType config.ProviderType) llm.Client {
	defaultClient := anthropic.NewClientWithBaseURL("", "kimi-k2.5", "https://api.kimi.com/coding/v1")
	if m.appConfig == nil {
		return defaultClient
	}

	createDirectClient := func(targetType config.ProviderType, modelOverride string) (llm.Client, string, error) {
		providerDef := config.GetProviderDefinition(targetType)
		if providerDef == nil || targetType == config.ProviderFallback || targetType == config.ProviderAutoRouter {
			return nil, "", fmt.Errorf("unsupported provider: %s", targetType)
		}

		provider := m.appConfig.Providers[string(targetType)]
		apiKey := strings.TrimSpace(provider.APIKey)
		if apiKey == "" && providerSupportsOAuth(targetType) && provider.OAuth != nil {
			apiKey = strings.TrimSpace(provider.OAuth.AccessToken)
		}
		if apiKey == "" && providerSupportsOAuth(targetType) {
			if oauth, _, err := codexauth.Load(""); err == nil && oauth != nil {
				provider.OAuth = oauth
				m.appConfig.Providers[string(targetType)] = provider
				apiKey = strings.TrimSpace(oauth.AccessToken)
			}
		}
		if apiKey == "" {
			apiKey = providerAPIKeyFromEnv(targetType)
		}

		baseURL := strings.TrimSpace(provider.BaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(providerDef.DefaultURL)
		}
		envURLKeys := []string{strings.ToUpper(string(targetType)) + "_BASE_URL"}
		if targetType == config.ProviderLMStudio {
			// Accept both legacy and explicit snake_case key for LM Studio.
			envURLKeys = append([]string{"LM_STUDIO_BASE_URL"}, envURLKeys...)
		}
		for _, key := range envURLKeys {
			if envURL := strings.TrimSpace(os.Getenv(key)); envURL != "" {
				baseURL = envURL
				break
			}
		}
		if envURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); envURL != "" && targetType == config.ProviderKimi {
			baseURL = envURL
		}
		if targetType == config.ProviderOpenAICodex {
			lower := strings.ToLower(strings.TrimSpace(baseURL))
			if lower == "" || strings.Contains(lower, "api.openai.com") {
				baseURL = strings.TrimSpace(providerDef.DefaultURL)
			}
		}

		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(provider.Model)
		}
		if model == "" {
			model = strings.TrimSpace(providerDef.DefaultModel)
		}
		if model == "" {
			model = strings.TrimSpace(m.appConfig.DefaultModel)
		}

		switch targetType {
		case config.ProviderGoogle:
			// Google Gemini uses a dedicated client with OpenAI-compatible API + Gemini extensions
			return gemini.NewClient(apiKey, model, baseURL), model, nil
		case config.ProviderLMStudio, config.ProviderOpenRouter, config.ProviderOpenAI:
			// Other OpenAI-compatible providers
			return lmstudio.NewClient(apiKey, model, baseURL), model, nil
		case config.ProviderOpenAICodex:
			return openaicodex.NewClientWithOptions(apiKey, model, baseURL, openaicodex.Options{
				PromptCacheKey:    provider.PromptCacheKey,
				ReasoningEffort:   provider.ReasoningEffort,
				TextVerbosity:     provider.TextVerbosity,
				ServiceTier:       provider.ServiceTier,
				MaxTokens:         provider.MaxTokens,
				StatefulResponses: false,
			}), model, nil
		case config.ProviderCursor:
			return cursorcli.NewClientWithOptions(model, cursorcli.Options{WorkDir: m.appConfig.WorkDir, APIKey: apiKey}), model, nil
		case config.ProviderAnthropic:
			return claudecli.NewClient(model, m.appConfig.WorkDir), model, nil
		default:
			return anthropic.NewClientWithBaseURL(apiKey, model, baseURL), model, nil
		}
	}

	createClientForProvider := func(providerRef string, modelOverride string) (llm.Client, string, error) {
		normalizedRef := config.NormalizeProviderRef(providerRef)
		if normalizedRef == "" {
			return nil, "", fmt.Errorf("provider reference is empty")
		}
		targetType := config.ProviderType(normalizedRef)
		if targetType == config.ProviderAutoRouter {
			return nil, "", fmt.Errorf("automatic_router cannot be used as nested target")
		}
		if normalizedRef == string(config.ProviderFallback) || config.IsFallbackAggregateRef(normalizedRef) {
			var chain []config.FallbackChainNode
			if normalizedRef == string(config.ProviderFallback) {
				fallbackProvider := m.appConfig.Providers[string(config.ProviderFallback)]
				chain = fallbackProvider.FallbackChainNodes
				if len(chain) == 0 {
					for _, raw := range fallbackProvider.FallbackChain {
						nodeType := config.ProviderType(config.NormalizeProviderRef(raw))
						if nodeType == "" || nodeType == config.ProviderFallback {
							continue
						}
						model := strings.TrimSpace(m.appConfig.Providers[string(nodeType)].Model)
						if model == "" {
							if nodeDef := config.GetProviderDefinition(nodeType); nodeDef != nil {
								model = strings.TrimSpace(nodeDef.DefaultModel)
							}
						}
						if model == "" {
							model = strings.TrimSpace(m.appConfig.DefaultModel)
						}
						if model == "" {
							continue
						}
						chain = append(chain, config.FallbackChainNode{Provider: string(nodeType), Model: model})
					}
				}
			} else {
				id := config.FallbackAggregateIDFromRef(normalizedRef)
				for _, aggregate := range m.appConfig.FallbackAggregates {
					if config.NormalizeToken(aggregate.ID) == id {
						chain = aggregate.Chain
						break
					}
				}
			}

			nodes := make([]fallback.Node, 0, len(chain))
			seen := make(map[string]struct{}, len(chain))
			for _, rawNode := range chain {
				nodeType := config.ProviderType(config.NormalizeProviderRef(rawNode.Provider))
				model := strings.TrimSpace(rawNode.Model)
				if nodeType == "" || nodeType == config.ProviderFallback || model == "" {
					continue
				}
				seenKey := string(nodeType) + "::" + model
				if _, exists := seen[seenKey]; exists {
					continue
				}
				seen[seenKey] = struct{}{}
				client, _, err := createDirectClient(nodeType, model)
				if err != nil {
					return nil, "", fmt.Errorf("fallback node %s/%s is unavailable: %w", nodeType, model, err)
				}
				nodes = append(nodes, fallback.Node{
					Name:   string(nodeType),
					Model:  model,
					Client: client,
				})
			}
			if len(nodes) < 2 {
				return nil, "", fmt.Errorf("%s requires at least two valid fallback model nodes", normalizedRef)
			}
			retries := m.appConfig.LLMRetries
			if retries <= 0 {
				retries = fallback.DefaultMaxRetries
			}
			return fallback.NewClient(nodes, fallback.WithMaxRetries(retries)), "", nil
		}

		client, model, err := createDirectClient(targetType, modelOverride)
		if err != nil {
			return nil, model, err
		}
		retries := m.appConfig.LLMRetries
		if retries <= 0 {
			retries = retry.DefaultMaxRetries
		}
		return retry.Wrap(client, retry.WithMaxRetries(retries)), model, nil
	}

	providerRef := config.NormalizeProviderRef(string(providerType))
	if providerRef == string(config.ProviderAutoRouter) {
		return autorouter.New(m.appConfig, createClientForProvider)
	}
	client, _, err := createClientForProvider(providerRef, "")
	if err != nil {
		logging.Warn("Failed to create LLM client for %s: %v", providerType, err)
		return defaultClient
	}
	return client
}

func (m Model) validateActiveProviderConfig() error {
	if m.appConfig == nil {
		return nil
	}
	providerType := config.ProviderType(strings.TrimSpace(m.appConfig.ActiveProvider))
	def := config.GetProviderDefinition(providerType)
	if def == nil {
		return fmt.Errorf("unknown active provider: %s", providerType)
	}
	if providerType == config.ProviderFallback {
		fallbackProvider := m.appConfig.Providers[string(config.ProviderFallback)]
		if len(fallbackProvider.FallbackChain) < 2 {
			return fmt.Errorf("fallback chain requires at least two providers")
		}
		return nil
	}
	if providerType == config.ProviderAutoRouter {
		provider := m.appConfig.Providers[string(config.ProviderAutoRouter)]
		routerProvider := config.NormalizeProviderRef(provider.RouterProvider)
		if routerProvider == "" {
			return fmt.Errorf("automatic router requires router_provider")
		}
		if routerProvider == string(config.ProviderAutoRouter) {
			return fmt.Errorf("automatic router cannot use automatic_router as router provider")
		}
		hasRule := false
		for _, rule := range provider.RouterRules {
			if strings.TrimSpace(rule.Match) == "" || strings.TrimSpace(rule.Provider) == "" {
				continue
			}
			hasRule = true
			if config.NormalizeProviderRef(rule.Provider) == string(config.ProviderAutoRouter) {
				return fmt.Errorf("routing rule %q cannot target automatic_router", strings.TrimSpace(rule.Match))
			}
		}
		if !hasRule {
			return fmt.Errorf("automatic router requires at least one routing rule")
		}
		return nil
	}
	if !def.RequiresKey {
		return nil
	}

	provider := m.appConfig.Providers[string(providerType)]
	if providerSupportsOAuth(providerType) && provider.OAuth != nil && strings.TrimSpace(provider.OAuth.AccessToken) != "" {
		return nil
	}
	if providerSupportsOAuth(providerType) {
		if oauth, _, err := codexauth.Load(""); err == nil && oauth != nil && strings.TrimSpace(oauth.AccessToken) != "" {
			provider.OAuth = oauth
			m.appConfig.Providers[string(providerType)] = provider
			return nil
		}
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = providerAPIKeyFromEnv(providerType)
	}
	if apiKey == "" {
		envName := providerAPIKeyEnvName(providerType)
		if envName != "" {
			return fmt.Errorf("%s API key is missing. Configure provider settings (/provider) or set %s", def.DisplayName, envName)
		}
		return fmt.Errorf("%s API key is missing. Configure provider settings with /provider", def.DisplayName)
	}
	return nil
}

func providerAPIKeyFromEnv(providerType config.ProviderType) string {
	envName := providerAPIKeyEnvName(providerType)
	if envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	if providerType == config.ProviderGoogle {
		return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	return ""
}

func providerAPIKeyEnvName(providerType config.ProviderType) string {
	switch providerType {
	case config.ProviderKimi:
		return "KIMI_API_KEY"
	case config.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case config.ProviderCursor:
		return "CURSOR_API_KEY"
	case config.ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
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

func providerSupportsOAuth(providerType config.ProviderType) bool {
	return providerType == config.ProviderOpenAICodex
}

func providerHasCredentials(providerType config.ProviderType, provider config.Provider) bool {
	if strings.TrimSpace(provider.APIKey) != "" {
		return true
	}
	if providerSupportsOAuth(providerType) && provider.OAuth != nil && strings.TrimSpace(provider.OAuth.AccessToken) != "" {
		return true
	}
	if providerSupportsOAuth(providerType) {
		if oauth, _, err := codexauth.Load(""); err == nil && oauth != nil && strings.TrimSpace(oauth.AccessToken) != "" {
			return true
		}
	}
	return providerAPIKeyFromEnv(providerType) != ""
}

func (m Model) providerUsageHint(providerType config.ProviderType) string {
	if m.appConfig == nil {
		return ""
	}

	switch providerType {
	case config.ProviderOpenAI:
		provider := m.appConfig.Providers[string(providerType)]
		if !providerHasCredentials(providerType, provider) {
			return "usage left: configure API key first"
		}
		return "usage left: unavailable (check OpenAI dashboard)"
	case config.ProviderOpenAICodex:
		provider := m.appConfig.Providers[string(providerType)]
		if !providerHasCredentials(providerType, provider) {
			return "usage left: connect Codex OAuth/API key first"
		}
		return "usage left: unavailable (check ChatGPT/OpenAI limits)"
	case config.ProviderAnthropic:
		if !claudecli.IsAvailable() {
			return "usage left: Claude CLI unavailable"
		}
		return "usage left: unavailable (Claude CLI reports per-run usage only)"
	default:
		return ""
	}
}

// saveProviderCredentials saves the API key or URL for a provider
func (m Model) saveProviderCredentials() (tea.Model, tea.Cmd) {
	providerType := config.ProviderType(m.selectedProviderType)
	providerDef := config.GetProviderDefinition(providerType)

	provider := m.appConfig.Providers[m.selectedProviderType]
	if provider.Name == "" {
		provider.Name = m.selectedProviderType
	}

	if m.providerMenuStep == 1 {
		// Saving API key
		provider.APIKey = m.providerInput
	} else if m.providerMenuStep == 2 {
		// Saving URL
		provider.BaseURL = m.providerInput
	}

	if provider.Model == "" && providerDef != nil {
		provider.Model = providerDef.DefaultModel
	}

	m.appConfig.SetProvider(providerType, provider)

	// Check if we need more credentials
	if providerType == config.ProviderLMStudio {
		// LM Studio doesn't require API key, URL is enough
		return m.activateProvider(providerType)
	}

	if providerDef.RequiresKey && !providerHasCredentials(providerType, provider) {
		m.providerMenuStep = 1
		m.providerInput = ""
		return m, nil
	}

	// All credentials gathered, activate provider
	return m.activateProvider(providerType)
}

// selectModel selects a model for the current provider
func (m Model) selectModel(modelName string) (tea.Model, tea.Cmd) {
	m.appConfig.DefaultModel = modelName

	// Update provider config
	provider := m.appConfig.Providers[m.appConfig.ActiveProvider]
	provider.Model = modelName
	m.appConfig.SetProvider(config.ProviderType(m.appConfig.ActiveProvider), provider)

	// Keep TUI context accounting aligned with the HTTP backend resolver.
	m.contextWindow = config.ResolveContextWindow(config.ProviderType(m.appConfig.ActiveProvider), provider, modelName)
	m.agentConfig.Model = modelName
	m.agentConfig.ContextWindow = m.contextWindow

	// Save config
	if err := m.appConfig.Save(config.GetConfigPath()); err != nil {
		logging.Error("Failed to save config: %v", err)
	}

	// Recreate LLM client with new model
	m.llmClient = m.createLLMClient(config.ProviderType(m.appConfig.ActiveProvider))
	m.agent = agent.New(m.agentConfig, m.llmClient, m.toolManager, m.sessionManager)

	m.showModelsMenu = false

	m.messages = append(m.messages, message{
		role:      "system",
		content:   fmt.Sprintf("Model switched to: %s", modelName),
		timestamp: time.Now(),
	})
	m.viewport.SetContent(m.renderMessages())

	return m, nil
}

// renderProviderMenu renders the provider selection menu
