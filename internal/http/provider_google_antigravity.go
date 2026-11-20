package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/antigravitycli"
)

func (s *Server) newAntigravityClient(model, workDir string) llm.Client {
	return antigravitycli.NewClientWithOptions(model, antigravitycli.Options{WorkDir: workDir})
}

func (s *Server) applyGoogleAuthMode(provider *config.Provider, def *config.ProviderDefinition, req UpdateProviderRequest) error {
	if req.AuthMode == nil {
		return nil
	}
	mode, err := config.NormalizeGoogleAuthMode(*req.AuthMode)
	if err != nil {
		return err
	}
	previous, _ := config.NormalizeGoogleAuthMode(provider.AuthMode)
	provider.AuthMode = mode
	if mode == previous || req.Model != nil {
		return nil
	}
	if mode == config.GoogleAuthModeAntigravity {
		provider.Model = antigravitycli.DefaultModel
	} else {
		provider.Model = def.DefaultModel
	}
	return nil
}

func (s *Server) decorateGoogleProviderResponse(resp *ProviderConfigResponse) {
	resp.AuthMode = config.GoogleAuthModeAPIKey
	resp.AntigravityInstalled = antigravitycli.IsAvailable()
	resp.AntigravityInstallCommand = antigravitycli.InstallCommand
	if !s.config.GoogleUsesAntigravity() {
		return
	}
	resp.AuthMode = config.GoogleAuthModeAntigravity
	resp.Configured = resp.AntigravityInstalled
	resp.BaseURL = ""
}

func (s *Server) listAntigravityModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{Models: antigravitycli.ListModelCatalog(ctx, "")})
}

func antigravityNotInstalledMessage() string {
	return "Antigravity CLI (agy) was not found. Install it with: " + strings.TrimSpace(antigravitycli.InstallCommand)
}
