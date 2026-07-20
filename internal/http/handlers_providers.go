// handlers_providers.go keeps provider-facing HTTP handlers focused without changing logic.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/llm/cursorcli"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/A2gent/brute/internal/llm/kimicli"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/go-chi/chi/v5"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	definitions := config.SupportedProviders()
	resp := make([]ProviderConfigResponse, 0, len(definitions)+len(s.config.FallbackAggregates))
	proxyBaseURL := normalizeOpenAIBaseURL(strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")))
	proxyManaged := proxyBaseURL != ""

	for _, def := range definitions {
		existing := s.config.Providers[string(def.Type)]
		if def.Type == config.ProviderFallback {
			chain := normalizeFallbackChainNodes(existing.FallbackChainNodes)
			if len(chain) == 0 && len(existing.FallbackChain) > 0 {
				chain = legacyProvidersToFallbackNodes(existing.FallbackChain, s.resolveModelForProvider)
			}
			isActive := config.NormalizeProviderRef(s.config.ActiveProvider) == string(def.Type)
			if !isActive && len(chain) == 0 {

				continue
			}
			resp = append(resp, ProviderConfigResponse{
				Type:          string(def.Type),
				DisplayName:   def.DisplayName,
				DefaultURL:    def.DefaultURL,
				RequiresKey:   def.RequiresKey,
				DefaultModel:  def.DefaultModel,
				ContextWindow: s.resolveContextWindowForProvider(def.Type, ""),
				IsActive:      isActive,
				Configured:    s.fallbackChainIsConfigured(chain),
				HasAPIKey:     false,
				BaseURL:       "",
				Model:         "",
				ProxyManaged:  proxyManaged,
				ProxyBaseURL:  proxyBaseURL,
				FallbackChain: chain,
			})
			continue
		}
		if def.Type == config.ProviderAutoRouter {
			rules := normalizeRouterRules(existing.RouterRules)
			resp = append(resp, ProviderConfigResponse{
				Type:           string(def.Type),
				DisplayName:    def.DisplayName,
				DefaultURL:     def.DefaultURL,
				RequiresKey:    def.RequiresKey,
				DefaultModel:   def.DefaultModel,
				ContextWindow:  s.resolveContextWindowForProvider(def.Type, ""),
				IsActive:       config.NormalizeProviderRef(s.config.ActiveProvider) == string(def.Type),
				Configured:     s.autoRouterConfigured(existing),
				HasAPIKey:      false,
				BaseURL:        "",
				Model:          "",
				ProxyManaged:   proxyManaged,
				ProxyBaseURL:   proxyBaseURL,
				FallbackChain:  nil,
				RouterProvider: config.NormalizeProviderRef(existing.RouterProvider),
				RouterModel:    strings.TrimSpace(existing.RouterModel),
				RouterRules:    rules,
			})
			continue
		}

		baseURL := strings.TrimSpace(existing.BaseURL)
		if baseURL == "" {
			baseURL = def.DefaultURL
		}
		model := strings.TrimSpace(existing.Model)
		if model == "" {
			model = def.DefaultModel
		}

		configured := baseURL != ""
		hasAPIKey := strings.TrimSpace(existing.APIKey) != ""
		hasOAuth := s.providerSupportsOAuth(def.Type) && existing.OAuth != nil && existing.OAuth.AccessToken != ""

		if def.Type == config.ProviderAnthropic {
			baseURL = ""
			configured = s.providerConfiguredForRef(string(def.Type))
			hasAPIKey = false
			hasOAuth = false
		}
		if def.Type == config.ProviderKimiCLI {
			baseURL = ""
			configured = s.providerConfiguredForUse(def.Type)
			hasAPIKey = false
			hasOAuth = false
		}
		if def.Type == config.ProviderCursor {
			baseURL = ""
			configured = s.providerConfiguredForUse(def.Type)
			hasAPIKey = hasAPIKey || strings.TrimSpace(s.apiKeyFromEnv(def.Type)) != ""
		}

		if def.RequiresKey {

			configured = configured && (hasAPIKey || hasOAuth)
		}

		resp = append(resp, ProviderConfigResponse{
			Type:              string(def.Type),
			DisplayName:       def.DisplayName,
			DefaultURL:        def.DefaultURL,
			RequiresKey:       def.RequiresKey,
			DefaultModel:      def.DefaultModel,
			ContextWindow:     s.resolveContextWindowForProvider(def.Type, model),
			IsActive:          s.config.ActiveProvider == string(def.Type),
			Configured:        configured,
			HasAPIKey:         hasAPIKey,
			BaseURL:           baseURL,
			Model:             model,
			PromptCacheKey:    strings.TrimSpace(existing.PromptCacheKey),
			ReasoningEffort:   strings.TrimSpace(existing.ReasoningEffort),
			TextVerbosity:     strings.TrimSpace(existing.TextVerbosity),
			ServiceTier:       strings.TrimSpace(existing.ServiceTier),
			MaxTokens:         existing.MaxTokens,
			StatefulResponses: s.providerStatefulResponsesForConfig(def.Type, existing.StatefulResponses),
			ProxyManaged:      proxyManaged,
			ProxyBaseURL:      proxyBaseURL,
			FallbackChain:     nil,
		})
	}

	for _, ref := range s.config.ListConfiguredClaudeInstanceRefs() {
		provider := s.config.Providers[ref]
		resp = append(resp, s.claudeInstanceResponse(ref, provider))
	}

	for _, aggregate := range s.config.FallbackAggregates {
		providerRef := config.FallbackAggregateRefFromID(aggregate.ID)
		chain := normalizeFallbackChainNodes(aggregate.Chain)
		resp = append(resp, ProviderConfigResponse{
			Type:          providerRef,
			DisplayName:   strings.TrimSpace(aggregate.Name),
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "",
			ContextWindow: s.resolveContextWindowForProvider(config.ProviderType(providerRef), ""),
			IsActive:      config.NormalizeProviderRef(s.config.ActiveProvider) == providerRef,
			Configured:    s.fallbackChainIsConfigured(chain),
			HasAPIKey:     false,
			BaseURL:       "",
			Model:         "",
			ProxyManaged:  proxyManaged,
			ProxyBaseURL:  proxyBaseURL,
			FallbackChain: chain,
		})
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if config.IsCustomClaudeInstanceRef(providerRef) {
		s.handleUpdateClaudeInstance(w, r)
		return
	}

	providerType := config.ProviderType(providerRef)

	var req UpdateProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if config.IsFallbackAggregateRef(string(providerType)) {
		aggregate, _ := s.findFallbackAggregateByRef(string(providerType))
		if aggregate == nil {
			s.errorResponse(w, http.StatusNotFound, "Fallback aggregate not found: "+string(providerType))
			return
		}

		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				s.errorResponse(w, http.StatusBadRequest, "Name cannot be empty")
				return
			}
			aggregate.Name = name
		}
		if req.FallbackChain != nil {
			chain, err := s.normalizeAndValidateFallbackChain(*req.FallbackChain)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			aggregate.Chain = chain
		}
		if req.Active != nil && *req.Active {
			s.config.ActiveProvider = string(providerType)
		}
		if err := s.config.Save(config.GetConfigPath()); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
			return
		}
		s.handleListProviders(w, r)
		return
	}

	def := config.GetProviderDefinition(providerType)
	if def == nil {
		s.errorResponse(w, http.StatusBadRequest, "Unsupported provider: "+string(providerType))
		return
	}

	provider := s.config.Providers[string(providerType)]
	provider.Name = string(providerType)
	if providerType == config.ProviderFallback {
		if req.FallbackChain != nil {
			chain, err := s.normalizeAndValidateFallbackChain(*req.FallbackChain)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			provider.FallbackChainNodes = chain
			provider.FallbackChain = nil
		}
		provider.APIKey = ""
		provider.BaseURL = ""
		provider.Model = ""
		provider.RouterProvider = ""
		provider.RouterModel = ""
		provider.RouterRules = nil
	} else if providerType == config.ProviderAutoRouter {
		if req.RouterProvider != nil {
			provider.RouterProvider = config.NormalizeProviderRef(*req.RouterProvider)
		}
		if req.RouterModel != nil {
			provider.RouterModel = strings.TrimSpace(*req.RouterModel)
		}
		if req.RouterRules != nil {
			rules, err := s.normalizeAndValidateRouterRules(*req.RouterRules)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
			provider.RouterRules = rules
		}
		if err := s.validateAutoRouterProvider(provider); err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		provider.APIKey = ""
		provider.BaseURL = ""
		provider.Model = ""
		provider.FallbackChain = nil
		provider.FallbackChainNodes = nil
	} else if providerType == config.ProviderAnthropic || providerType == config.ProviderKimiCLI {
		if req.Model != nil {
			provider.Model = strings.TrimSpace(*req.Model)
		}
		if provider.Model == "" {
			provider.Model = def.DefaultModel
		}
		provider.APIKey = ""
		provider.BaseURL = ""
		provider.OAuth = nil
		provider.RouterProvider = ""
		provider.RouterModel = ""
		provider.RouterRules = nil
		provider.FallbackChain = nil
		provider.FallbackChainNodes = nil
	} else {
		if req.APIKey != nil {
			provider.APIKey = strings.TrimSpace(*req.APIKey)
		}
		if req.BaseURL != nil {
			baseURL := strings.TrimSpace(*req.BaseURL)
			if providerType == config.ProviderLMStudio || providerType == config.ProviderOpenRouter || providerType == config.ProviderGoogle || providerType == config.ProviderOpenAI || providerType == config.ProviderGrok {
				baseURL = normalizeOpenAIBaseURL(baseURL)
			}
			provider.BaseURL = baseURL
		}
		if req.Model != nil {
			provider.Model = normalizeModelForProvider(providerType, *req.Model)
		}
		if req.PromptCacheKey != nil {
			provider.PromptCacheKey = strings.TrimSpace(*req.PromptCacheKey)
		}
		if req.ReasoningEffort != nil {
			provider.ReasoningEffort = strings.TrimSpace(*req.ReasoningEffort)
		}
		if req.TextVerbosity != nil {
			provider.TextVerbosity = strings.TrimSpace(*req.TextVerbosity)
		}
		if req.ServiceTier != nil {
			provider.ServiceTier = strings.TrimSpace(*req.ServiceTier)
		}
		if req.MaxTokens != nil {
			if *req.MaxTokens > 0 {
				provider.MaxTokens = *req.MaxTokens
			} else {
				provider.MaxTokens = 0
			}
		}
		if req.StatefulResponses != nil {
			provider.StatefulResponses = req.StatefulResponses
		}

		if provider.BaseURL == "" {
			provider.BaseURL = def.DefaultURL
		}
		if provider.Model == "" {
			provider.Model = def.DefaultModel
		}
		provider.RouterProvider = ""
		provider.RouterModel = ""
		provider.RouterRules = nil
	}

	s.config.SetProvider(providerType, provider)

	if req.Active != nil && *req.Active {
		s.config.ActiveProvider = string(providerType)
		if providerType != config.ProviderAutoRouter && provider.Model != "" {
			s.config.DefaultModel = provider.Model
		}
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleSetActiveProvider(w http.ResponseWriter, r *http.Request) {
	var req SetActiveProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	providerType := config.ProviderType(config.NormalizeProviderRef(req.Provider))
	def := config.GetProviderDefinition(providerType)
	if def == nil && !s.providerRefExists(string(providerType)) {
		s.errorResponse(w, http.StatusBadRequest, "Unsupported provider: "+req.Provider)
		return
	}

	s.config.ActiveProvider = string(providerType)
	provider := s.config.Providers[string(providerType)]
	if def != nil && providerType != config.ProviderAutoRouter && provider.Model != "" {
		s.config.DefaultModel = provider.Model
	} else if def != nil && providerType != config.ProviderAutoRouter && def.DefaultModel != "" {
		s.config.DefaultModel = def.DefaultModel
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleCreateFallbackAggregate(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	var req CreateFallbackAggregateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}

	chain, err := s.normalizeAndValidateFallbackChain(req.FallbackChain)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	id := config.NormalizeToken(name)
	if id == "" {
		id = "aggregate"
	}
	baseID := id
	suffix := 2
	for s.findFallbackAggregateByID(id) != nil {
		id = fmt.Sprintf("%s-%d", baseID, suffix)
		suffix++
	}

	aggregate := config.FallbackAggregate{
		ID:    id,
		Name:  name,
		Chain: chain,
	}
	s.config.FallbackAggregates = append(s.config.FallbackAggregates, aggregate)
	if req.Active {
		s.config.ActiveProvider = config.FallbackAggregateRefFromID(id)
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if config.IsCustomClaudeInstanceRef(providerRef) {
		s.handleDeleteClaudeInstance(w, r)
		return
	}
	if providerRef != string(config.ProviderFallback) && !config.IsFallbackAggregateRef(providerRef) {
		s.errorResponse(w, http.StatusBadRequest, "Only fallback aggregates can be deleted")
		return
	}

	if config.NormalizeProviderRef(s.config.ActiveProvider) == providerRef {
		s.errorResponse(w, http.StatusBadRequest, "Cannot delete active provider. Set another provider active first.")
		return
	}

	jobs, err := s.store.ListJobs()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to check jobs: "+err.Error())
		return
	}
	for _, job := range jobs {
		if config.NormalizeProviderRef(job.LLMProvider) == providerRef {
			s.errorResponse(w, http.StatusConflict, fmt.Sprintf("Cannot delete provider: recurring job %q (%s) uses it", job.Name, job.ID))
			return
		}
	}

	if providerRef == string(config.ProviderFallback) {
		provider := s.config.Providers[string(config.ProviderFallback)]
		provider.FallbackChain = nil
		provider.FallbackChainNodes = nil
		s.config.SetProvider(config.ProviderFallback, provider)
	} else {
		aggregate, index := s.findFallbackAggregateByRef(providerRef)
		if aggregate == nil || index < 0 {
			s.errorResponse(w, http.StatusNotFound, "Fallback aggregate not found: "+providerRef)
			return
		}
		s.config.FallbackAggregates = append(s.config.FallbackAggregates[:index], s.config.FallbackAggregates[index+1:]...)
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListLMStudioModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderLMStudio, "LM Studio")
}

func (s *Server) handleListKimiModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderKimi, "Kimi")
}

func (s *Server) handleListGoogleModels(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderGoogle)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderGoogle)
	}

	baseURL := normalizeOpenAIBaseURL(provider.BaseURL)
	if baseURL == "" {
		baseURL = normalizeOpenAIBaseURL(config.GetProviderDefinition(config.ProviderGoogle).DefaultURL)
	}

	models, err := gemini.ListModels(apiKey, baseURL)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list Google models: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: models,
	})
}

func (s *Server) handleListOpenAIModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenAI, "OpenAI")
}

func (s *Server) handleListOpenAICodexModels(w http.ResponseWriter, r *http.Request) {
	models := openaicodex.ListModelCatalog(r.Context(), s.openAICodexModelCatalogOptions())
	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{Models: models})
}

// openAICodexModelCatalogOptions resolves credentials used for the Codex model
// catalog. API-key mode uses /models; OAuth mode uses the verified curated list.
func (s *Server) openAICodexModelCatalogOptions() openaicodex.ModelCatalogOptions {
	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		if def := config.GetProviderDefinition(config.ProviderOpenAICodex); def != nil {
			baseURL = def.DefaultURL
		}
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderOpenAICodex)
	}

	opts := openaicodex.ModelCatalogOptions{
		BaseURL: baseURL,
		APIKey:  apiKey,
	}
	if apiKey == "" && provider.OAuth != nil {
		opts.AccessToken = strings.TrimSpace(provider.OAuth.AccessToken)
	}
	return opts
}

func (s *Server) handleListOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenRouter, "OpenRouter")
}

func (s *Server) handleListOpenCodeZenModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderOpenCodeZen, "OpenCode Zen")
}

func (s *Server) handleListGrokModels(w http.ResponseWriter, r *http.Request) {
	s.handleListOpenAICompatibleModels(w, r, config.ProviderGrok, "Grok")
}

func (s *Server) handleListKimiCLIModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: kimicli.ListModelCatalog(ctx, s.config.WorkDir),
	})
}

func (s *Server) handleListCursorModels(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderCursor)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderCursor)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: cursorcli.ListModelCatalog(ctx, cursorcli.Options{
			WorkDir: s.config.WorkDir,
			APIKey:  apiKey,
		}),
	})
}

func (s *Server) handleListAnthropicModels(w http.ResponseWriter, r *http.Request) {
	// Resolve an API key so the catalog can be pulled live from the official
	// Anthropic Models API; falls back to a curated current list when absent.
	provider := s.config.Providers[string(config.ProviderAnthropic)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderAnthropic)
	}

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{
		Models: anthropic.CLIModels(apiKey),
	})
}

func (s *Server) handleTestClaudeProvider(w http.ResponseWriter, r *http.Request, providerRef string) {
	if !s.providerConfiguredForRef(providerRef) {
		s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{
			Success: false,
			Message: "Claude CLI executable was not found. Install Claude Code or configure binary_path.",
		})
		return
	}

	s.ensureClaudeHealthCache()
	provider, _ := s.config.GetClaudeProvider(providerRef)
	cacheKey := s.claudeHealthCacheKey(providerRef, provider)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	report := claudecli.ProbeHealth(ctx, s.claudecliOptionsForRef(providerRef), nil)
	report = s.claudeHealthCache.Set(cacheKey, report)

	switch report.Status {
	case claudecli.HealthHealthy:
		msg := "Claude CLI is healthy"
		if report.Version != "" {
			msg = fmt.Sprintf("Claude CLI is healthy (%s)", report.Version)
		}
		s.jsonResponse(w, http.StatusOK, ProviderTestResponse{Success: true, Message: msg})
	case claudecli.HealthDegraded:
		msg := "Claude CLI is installed but not authenticated"
		if report.Error != "" {
			msg = report.Error
		}
		s.jsonResponse(w, http.StatusBadGateway, ProviderTestResponse{Success: false, Message: msg})
	default:
		msg := "Claude CLI is unavailable"
		if report.Error != "" {
			msg = report.Error
		}
		s.jsonResponse(w, http.StatusBadGateway, ProviderTestResponse{Success: false, Message: msg})
	}
}

func (s *Server) handleListOpenAICompatibleModels(w http.ResponseWriter, r *http.Request, providerType config.ProviderType, providerName string) {
	def := config.GetProviderDefinition(providerType)
	baseURL := normalizeOpenAIBaseURL(r.URL.Query().Get("base_url"))
	if baseURL == "" {
		provider := s.config.Providers[string(providerType)]
		baseURL = normalizeOpenAIBaseURL(provider.BaseURL)
	}
	if baseURL == "" && def != nil {
		baseURL = normalizeOpenAIBaseURL(def.DefaultURL)
	}
	if baseURL == "" {
		s.errorResponse(w, http.StatusBadRequest, providerName+" base URL is not configured")
		return
	}

	provider := s.config.Providers[string(providerType)]
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
	if def != nil && def.RequiresKey && apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, providerName+" API key is not configured")
		return
	}

	client := lmstudio.NewClient(apiKey, "", baseURL)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to fetch models from "+providerName+": "+err.Error())
		return
	}

	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		modelID := strings.TrimSpace(model.ID)
		if modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	sort.Strings(modelIDs)

	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{Models: modelIDs})
}

// handleTestProvider tests a provider by sending a simple "hello" message
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if config.IsClaudeProviderRef(providerRef) {
		s.handleTestClaudeProvider(w, r, providerRef)
		return
	}

	providerType := config.ProviderType(providerRef)

	def := config.GetProviderDefinition(providerType)
	if def == nil {
		s.jsonResponse(w, http.StatusNotFound, ProviderTestResponse{Success: false, Message: "Unknown provider"})
		return
	}

	if providerType == config.ProviderFallback || providerType == config.ProviderAutoRouter {
		s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{Success: false, Message: "Cannot test aggregate providers directly"})
		return
	}

	if !s.providerConfiguredForUse(providerType) {
		if providerType == config.ProviderAnthropic {
			s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{
				Success: false,
				Message: "Claude CLI executable was not found. Install Claude Code or set AAGENT_CLAUDE_CLI_PATH.",
			})
			return
		}
		if providerType == config.ProviderKimiCLI {
			s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{
				Success: false,
				Message: "Kimi CLI executable was not found. Install Kimi Code CLI or set AAGENT_KIMI_CLI_PATH.",
			})
			return
		}

		provider := s.config.Providers[string(providerType)]
		if s.providerSupportsOAuth(providerType) && provider.OAuth != nil {
			s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{
				Success: false,
				Message: "Provider OAuth credentials are incomplete — please reconnect OAuth in provider settings",
			})
		} else {
			message := "Provider is not configured — add an API key"
			if s.providerSupportsOAuth(providerType) {
				message = "Provider is not configured — add an API key or connect via OAuth"
			}
			s.jsonResponse(w, http.StatusBadRequest, ProviderTestResponse{
				Success: false,
				Message: message,
			})
		}
		return
	}

	client, err := s.createBaseLLMClient(providerType, "")
	if err != nil {
		s.jsonResponse(w, http.StatusBadGateway, ProviderTestResponse{Success: false, Message: "Failed to create client: " + err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	req := &llm.ChatRequest{
		Model: s.resolveModelForProvider(providerType),
		Messages: []llm.Message{
			{Role: "user", Content: "hello"},
		},
		Temperature: 0.7,
		// Reasoning models (e.g. z-ai/glm-5.2) spend tokens on hidden reasoning
		// before producing any visible content. A small budget gets fully
		// consumed by reasoning, yielding empty content with finish_reason
		// "length". max_tokens is a ceiling, not a target — the model stops
		// early on a "hello", so a generous cap costs nothing but protects
		// reasoning-heavy models from truncating. Matches defaultMaxTokens.
		MaxTokens: 4096,
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		adaptedErr := s.adaptProviderErrorMessage(providerType, err)
		s.jsonResponse(w, http.StatusBadGateway, ProviderTestResponse{Success: false, Message: "Request failed: " + adaptedErr.Error()})
		return
	}

	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		message := "Empty response from provider"
		if resp.StopReason != "" {
			message = fmt.Sprintf("Empty response from provider (finish_reason: %s). Check if model name is correct.", resp.StopReason)
		} else {
			message = "Empty response from provider. Check if the model name is correct or if the model requires a specific prompt."
		}
		s.jsonResponse(w, http.StatusBadGateway, ProviderTestResponse{Success: false, Message: message})
		return
	}

	s.jsonResponse(w, http.StatusOK, ProviderTestResponse{
		Success: true,
		Message: fmt.Sprintf("Success! Response: %s", strings.TrimSpace(resp.Content)),
	})
}

// ProviderConfiguredForUse reports whether the provider has enough configuration for a real request.
func (s *Server) ProviderConfiguredForUse(providerType config.ProviderType) bool {
	return s.providerConfiguredForUse(providerType)
}

// TestAllProviders runs connectivity tests for every testable provider concurrently.
func (s *Server) TestAllProviders(ctx context.Context) []ProviderTestResult {
	testableProviders := config.TestableProviders()

	results := make([]ProviderTestResult, 0, len(testableProviders))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, def := range testableProviders {
		wg.Add(1)
		go func(def config.ProviderDefinition) {
			defer wg.Done()

			start := time.Now()
			result := ProviderTestResult{
				Provider: string(def.Type),
			}

			if !s.providerConfiguredForUse(def.Type) {
				result.Success = false
				result.Message = "Not configured"
				result.Duration = time.Since(start).Milliseconds()
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			client, err := s.createBaseLLMClient(def.Type, "")
			if err != nil {
				result.Success = false
				result.Message = "Failed to create client: " + err.Error()
				result.Duration = time.Since(start).Milliseconds()
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			req := &llm.ChatRequest{
				Model: s.resolveModelForProvider(def.Type),
				Messages: []llm.Message{
					{Role: "user", Content: "hello"},
				},
				Temperature: 0.7,
				MaxTokens:   100,
			}

			resp, err := client.Chat(ctx, req)
			if err != nil {
				result.Success = false
				result.Message = "Request failed: " + err.Error()
				result.Duration = time.Since(start).Milliseconds()
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			if resp.Content == "" {
				result.Success = false
				result.Message = "Empty response"
				result.Duration = time.Since(start).Milliseconds()
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
				return
			}

			result.Success = true
			result.Message = strings.TrimSpace(resp.Content)
			result.Duration = time.Since(start).Milliseconds()
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(def)
	}

	wg.Wait()

	return results
}

// handleTestAllProviders tests all configured providers concurrently
func (s *Server) handleTestAllProviders(w http.ResponseWriter, r *http.Request) {
	results := s.TestAllProviders(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
	})
}
