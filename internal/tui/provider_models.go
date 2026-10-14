package tui

import (
	"context"
	"fmt"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openai"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
)

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

	// For OpenAI API-key usage, merge the curated newest-first catalog with live /models.
	if m.appConfig.ActiveProvider == string(config.ProviderOpenAI) {
		return m.fetchOpenAIModels()
	}

	// For other providers, show known models
	return m.showStaticModels()
}

// fetchOpenAICodexModels loads the Codex model catalog using the same shared
// discovery the web API uses: curated fallback plus live /models (API key or OAuth).
func (m Model) fetchOpenAICodexModels() (tea.Model, tea.Cmd) {
	opts := openaicodex.ModelCatalogOptions{}
	if provider := m.appConfig.GetActiveProvider(); provider != nil {
		opts.BaseURL = strings.TrimSpace(provider.BaseURL)
		opts.APIKey = strings.TrimSpace(provider.APIKey)
		if opts.APIKey == "" && provider.OAuth != nil {
			opts.AccessToken = strings.TrimSpace(provider.OAuth.AccessToken)
		}
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

func (m Model) fetchOpenAIModels() (tea.Model, tea.Cmd) {
	opts := openai.ModelCatalogOptions{}
	if provider := m.appConfig.GetActiveProvider(); provider != nil {
		opts.BaseURL = strings.TrimSpace(provider.BaseURL)
		opts.APIKey = strings.TrimSpace(provider.APIKey)
	}
	if opts.BaseURL == "" {
		if def := config.GetProviderDefinition(config.ProviderOpenAI); def != nil {
			opts.BaseURL = def.DefaultURL
		}
	}
	if opts.APIKey == "" {
		opts.APIKey = providerAPIKeyFromEnv(config.ProviderOpenAI)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.availableModels = openai.ListModelCatalog(ctx, opts)

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
