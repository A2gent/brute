package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/go-chi/chi/v5"
)

func (s *Server) ensureClaudeHealthCache() {
	if s.claudeHealthCache == nil {
		s.claudeHealthCache = newClaudeHealthCache()
	}
}

func (s *Server) claudeProviderSessionIdentity(ref string) string {
	normalized := config.NormalizeProviderRef(ref)
	if normalized == "" {
		normalized = string(config.ProviderAnthropic)
	}
	provider := s.config.Providers[normalized]
	return config.ClaudeProviderSessionIdentity(normalized, provider)
}

func (s *Server) claudecliOptionsForRef(ref string) claudecli.Options {
	normalized := config.NormalizeProviderRef(ref)
	if normalized == "" {
		normalized = string(config.ProviderAnthropic)
	}
	provider := s.config.Providers[normalized]
	paths := config.ResolveClaudeProviderPaths(provider)
	env := config.BuildClaudeCLIEnvironment(provider, runtime.GOOS)
	noSessionPersistence := envBoolDefault("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", false) || !s.providerSessionPersistenceForRef(normalized)
	opts := claudecli.Options{
		Executable:           paths.BinaryPath,
		WorkDir:              s.config.WorkDir,
		ConfigDir:            paths.ConfigDir,
		HomePath:             paths.HomePath,
		Environment:          env,
		Identity:             s.claudeProviderSessionIdentity(normalized),
		NoSessionPersistence: noSessionPersistence,
		PermissionMode:       strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_PERMISSION_MODE")),
		MaxBudgetUSD:         strings.TrimSpace(os.Getenv("AAGENT_CLAUDE_CLI_MAX_BUDGET_USD")),
	}
	return opts
}

func envBoolDefault(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func (s *Server) providerSessionPersistenceForRef(ref string) bool {
	if !config.IsClaudeProviderRef(ref) {
		return false
	}
	provider := s.config.Providers[config.NormalizeProviderRef(ref)]
	if provider.StatefulResponses != nil {
		return *provider.StatefulResponses
	}
	return true
}

func (s *Server) claudeHealthCacheKey(ref string, provider config.Provider) string {
	return config.NormalizeProviderRef(ref) + ":" + config.ClaudeProviderConfigFingerprint(provider)
}

func (s *Server) handleProviderHealth(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if !config.IsClaudeProviderRef(providerRef) {
		s.errorResponse(w, http.StatusBadRequest, "Health checks are only available for Claude CLI providers")
		return
	}

	s.ensureClaudeHealthCache()
	provider, ok := s.config.GetClaudeProvider(providerRef)
	if config.IsCustomClaudeInstanceRef(providerRef) && !ok {
		s.errorResponse(w, http.StatusNotFound, "Claude instance not found")
		return
	}
	cacheKey := s.claudeHealthCacheKey(providerRef, provider)
	refresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true")

	var report claudecli.HealthReport
	if !refresh {
		if cached, ok := s.claudeHealthCache.Get(cacheKey); ok {
			s.jsonResponse(w, http.StatusOK, cached)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	report = claudecli.ProbeHealth(ctx, s.claudecliOptionsForRef(providerRef), nil)
	report = s.claudeHealthCache.Set(cacheKey, report)
	s.jsonResponse(w, http.StatusOK, report)
}

func (s *Server) handleCreateClaudeInstance(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	var req CreateClaudeInstanceRequest
	if err := decodeStrictJSONBody(r, &req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	ref := config.ClaudeInstanceRefFromID(req.ID)
	if ref == "" {
		s.errorResponse(w, http.StatusBadRequest, "id is required")
		return
	}
	if _, exists := s.config.Providers[ref]; exists {
		s.errorResponse(w, http.StatusConflict, "Claude instance already exists")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.ID
	}

	provider := s.config.Providers[ref]
	provider.Name = name
	if err := s.applyClaudeInstanceRequest(&provider, req.ClaudeInstanceConfigRequest); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Model != nil {
		provider.Model = strings.TrimSpace(*req.Model)
	}
	if provider.Model == "" {
		if def := config.GetProviderDefinition(config.ProviderAnthropic); def != nil {
			provider.Model = def.DefaultModel
		}
	}

	s.config.Providers[ref] = provider
	s.ensureClaudeHealthCache()
	s.claudeHealthCache.InvalidatePrefix(ref)

	if req.Active {
		s.config.ActiveProvider = ref
		if provider.Model != "" {
			s.config.DefaultModel = provider.Model
		}
	}

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}

	s.handleListProviders(w, r)
}

func (s *Server) applyClaudeInstanceRequest(provider *config.Provider, req ClaudeInstanceConfigRequest) error {
	if req.BinaryPath != nil {
		provider.BinaryPath = strings.TrimSpace(*req.BinaryPath)
	}
	if req.ConfigDir != nil {
		provider.ClaudeConfigDir = strings.TrimSpace(*req.ConfigDir)
	}
	if req.HomePath != nil {
		provider.HomePath = strings.TrimSpace(*req.HomePath)
	}
	if req.EnvOverrides != nil {
		provider.EnvOverrides = make(map[string]string)
		for key, value := range *req.EnvOverrides {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if err := config.ValidateClaudeEnvKey(key); err != nil {
				return err
			}
			provider.EnvOverrides[key] = value
		}
	}
	if req.SensitiveSecrets != nil {
		if provider.SensitiveSecrets == nil {
			provider.SensitiveSecrets = make(map[string]string)
		}
		for key, value := range *req.SensitiveSecrets {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if err := config.ValidateClaudeEnvKey(key); err != nil {
				return err
			}
			if strings.TrimSpace(value) == "" {
				delete(provider.SensitiveSecrets, key)
				continue
			}
			provider.SensitiveSecrets[key] = value
		}
	}
	return config.ValidateClaudeProviderEnvMaps(*provider)
}

func (s *Server) claudeInstanceResponse(ref string, provider config.Provider) ProviderConfigResponse {
	def := config.GetProviderDefinition(config.ProviderAnthropic)
	model := strings.TrimSpace(provider.Model)
	if model == "" && def != nil {
		model = def.DefaultModel
	}
	return ProviderConfigResponse{
		Type:                ref,
		DisplayName:         firstNonEmpty(strings.TrimSpace(provider.Name), ref),
		DefaultURL:          "",
		RequiresKey:         false,
		DefaultModel:        def.DefaultModel,
		ContextWindow:       s.resolveContextWindowForProvider(config.ProviderAnthropic, model),
		IsActive:            config.NormalizeProviderRef(s.config.ActiveProvider) == ref,
		Configured:          s.providerConfiguredForRef(ref),
		HasAPIKey:           false,
		BaseURL:             "",
		Model:               model,
		StatefulResponses:   s.providerSessionPersistenceForRef(ref),
		BinaryPath:          strings.TrimSpace(provider.BinaryPath),
		ConfigDir:           strings.TrimSpace(provider.ClaudeConfigDir),
		HomePath:            strings.TrimSpace(provider.HomePath),
		EnvOverrides:        provider.EnvOverrides,
		SensitiveSecretKeys: config.SensitiveSecretKeysSorted(provider),
	}
}

func (s *Server) providerConfiguredForRef(ref string) bool {
	return s.providerConfiguredForUse(config.ProviderType(config.NormalizeProviderRef(ref)))
}

func (s *Server) clearActiveProviderIf(ref string) {
	if config.NormalizeProviderRef(s.config.ActiveProvider) == config.NormalizeProviderRef(ref) {
		s.config.ActiveProvider = string(config.ProviderAnthropic)
		if def := config.GetProviderDefinition(config.ProviderAnthropic); def != nil && def.DefaultModel != "" {
			s.config.DefaultModel = def.DefaultModel
		}
	}
}

func (s *Server) handleDeleteClaudeInstance(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if !config.IsCustomClaudeInstanceRef(providerRef) {
		s.errorResponse(w, http.StatusBadRequest, "Only custom Claude instances can be deleted")
		return
	}
	if config.NormalizeProviderRef(s.config.ActiveProvider) == providerRef {
		s.errorResponse(w, http.StatusBadRequest, "Cannot delete active provider. Set another provider active first.")
		return
	}

	delete(s.config.Providers, providerRef)
	s.clearActiveProviderIf(providerRef)
	s.ensureClaudeHealthCache()
	s.claudeHealthCache.InvalidatePrefix(providerRef)

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeStrictJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil && strings.TrimSpace(string(extra)) != "" {
			return fmt.Errorf("trailing data after JSON object")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleUpdateClaudeInstance(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) != "" {
		s.errorResponse(w, http.StatusForbidden, "Provider settings are managed by the parent agent in Docker safe mode")
		return
	}

	var req UpdateClaudeInstanceRequest
	if err := decodeStrictJSONBody(r, &req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	provider, ok := s.config.Providers[providerRef]
	if !ok {
		s.errorResponse(w, http.StatusNotFound, "Claude instance not found")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			s.errorResponse(w, http.StatusBadRequest, "Name cannot be empty")
			return
		}
		provider.Name = name
	}
	if err := s.applyClaudeInstanceRequest(&provider, req.ClaudeInstanceConfigRequest); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Model != nil {
		provider.Model = strings.TrimSpace(*req.Model)
	}
	if req.Active != nil && *req.Active {
		s.config.ActiveProvider = providerRef
		if provider.Model != "" {
			s.config.DefaultModel = provider.Model
		}
	}

	s.config.Providers[providerRef] = provider
	s.ensureClaudeHealthCache()
	s.claudeHealthCache.InvalidatePrefix(providerRef)

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save provider config: "+err.Error())
		return
	}
	s.handleListProviders(w, r)
}
