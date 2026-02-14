package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the application configuration
type Config struct {
	DefaultModel       string              `json:"default_model"`
	ActiveProvider     string              `json:"active_provider"` // Provider reference: built-in provider or named fallback aggregate
	MaxSteps           int                 `json:"max_steps"`
	Temperature        float64             `json:"temperature"`
	DataPath           string              `json:"data_path"`
	WorkDir            string              `json:"work_dir"`
	Providers          map[string]Provider `json:"providers"`
	FallbackAggregates []FallbackAggregate `json:"fallback_aggregates,omitempty"`
	Tools              ToolsConfig         `json:"tools"`
}

// Provider configuration for LLM providers
type Provider struct {
	Name               string              `json:"name"`
	APIKey             string              `json:"api_key"`
	BaseURL            string              `json:"base_url"`
	Model              string              `json:"model"`
	FallbackChain      []string            `json:"fallback_chain,omitempty"` // Legacy provider-only fallback nodes.
	FallbackChainNodes []FallbackChainNode `json:"fallback_chain_nodes,omitempty"`
	RouterProvider     string              `json:"router_provider,omitempty"` // Provider reference used by automatic router (direct provider or fallback chain).
	RouterModel        string              `json:"router_model,omitempty"`    // Optional model override for direct router provider.
	RouterRules        []RouterRule        `json:"router_rules,omitempty"`
	ContextWindow      int                 `json:"context_window,omitempty"` // in tokens
}

// FallbackChainNode stores a single fallback step with explicit provider+model.
type FallbackChainNode struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// FallbackAggregate stores a named fallback chain that can be selected like a provider.
type FallbackAggregate struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Chain []FallbackChainNode `json:"chain"`
}

// RouterRule maps a textual task context to a target model/provider.
type RouterRule struct {
	Match    string `json:"match"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
}

// ProviderType identifies the type of provider
type ProviderType string

const (
	ProviderKimi       ProviderType = "kimi"
	ProviderOpenRouter ProviderType = "openrouter"
	ProviderLMStudio   ProviderType = "lmstudio"
	ProviderAnthropic  ProviderType = "anthropic"
	ProviderGoogle     ProviderType = "google"
	ProviderOpenAI     ProviderType = "openai"
	ProviderFallback   ProviderType = "fallback_chain"
	ProviderAutoRouter ProviderType = "automatic_router"
)

// ProviderDefinition describes a supported provider
type ProviderDefinition struct {
	Type          ProviderType
	DisplayName   string
	DefaultURL    string
	RequiresKey   bool
	DefaultModel  string
	ContextWindow int
}

// SupportedProviders returns all supported provider definitions
func SupportedProviders() []ProviderDefinition {
	return []ProviderDefinition{
		{
			Type:          ProviderAutoRouter,
			DisplayName:   "Automatic Router",
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "",
			ContextWindow: 0,
		},
		{
			Type:          ProviderFallback,
			DisplayName:   "Fallback-chain aggregate",
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "",
			ContextWindow: 0,
		},
		{
			Type:          ProviderKimi,
			DisplayName:   "Kimi (Moonshot AI)",
			DefaultURL:    "https://api.kimi.com/coding/v1",
			RequiresKey:   true,
			DefaultModel:  "kimi-k2.5",
			ContextWindow: 131072,
		},
		{
			Type:          ProviderOpenRouter,
			DisplayName:   "OpenRouter",
			DefaultURL:    "https://openrouter.ai/api/v1",
			RequiresKey:   true,
			DefaultModel:  "openrouter/auto",
			ContextWindow: 128000,
		},
		{
			Type:          ProviderLMStudio,
			DisplayName:   "LM Studio (Local)",
			DefaultURL:    "http://localhost:1234/v1",
			RequiresKey:   false,
			DefaultModel:  "",
			ContextWindow: 32768,
		},
		{
			Type:          ProviderAnthropic,
			DisplayName:   "Anthropic Claude",
			DefaultURL:    "https://api.anthropic.com/v1",
			RequiresKey:   true,
			DefaultModel:  "claude-sonnet-4-20250514",
			ContextWindow: 200000,
		},
		{
			Type:          ProviderGoogle,
			DisplayName:   "Google Gemini",
			DefaultURL:    "https://generativelanguage.googleapis.com/v1beta/openai",
			RequiresKey:   true,
			DefaultModel:  "gemini-2.0-flash",
			ContextWindow: 1048576,
		},
		{
			Type:          ProviderOpenAI,
			DisplayName:   "OpenAI",
			DefaultURL:    "https://api.openai.com/v1",
			RequiresKey:   true,
			DefaultModel:  "gpt-4.1-mini",
			ContextWindow: 128000,
		},
	}
}

// GetProviderDefinition returns the definition for a provider type
func GetProviderDefinition(ptype ProviderType) *ProviderDefinition {
	for _, p := range SupportedProviders() {
		if p.Type == ptype {
			return &p
		}
	}
	return nil
}

// ToolsConfig configures tool permissions
type ToolsConfig struct {
	Bash  string `json:"bash"` // "allow", "deny", "ask"
	Read  string `json:"read"`
	Write string `json:"write"`
	Edit  string `json:"edit"`
	Glob  string `json:"glob"`
	Grep  string `json:"grep"`
	Task  string `json:"task"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	workDir, _ := os.Getwd()

	return &Config{
		DefaultModel:   "kimi-k2.5",
		ActiveProvider: string(ProviderKimi),
		MaxSteps:       50,
		Temperature:    0.0,
		DataPath:       filepath.Join(homeDir, ".local", "share", "aagent"),
		WorkDir:        workDir,
		Providers:      make(map[string]Provider),
		Tools: ToolsConfig{
			Bash:  "allow",
			Read:  "allow",
			Write: "allow",
			Edit:  "allow",
			Glob:  "allow",
			Grep:  "allow",
			Task:  "allow",
		},
	}
}

// GetActiveProvider returns the configuration for the currently active provider
func (c *Config) GetActiveProvider() *Provider {
	if p, ok := c.Providers[c.ActiveProvider]; ok {
		return &p
	}
	return nil
}

// SetProvider sets or updates a provider configuration
func (c *Config) SetProvider(ptype ProviderType, provider Provider) {
	provider.Name = string(ptype)
	c.Providers[string(ptype)] = provider
}

// GetConfigPath returns the path where config should be saved
func GetConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config", "aagent", "config.json")
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from config file
	configPaths := []string{
		".aagent/config.json",
		filepath.Join(os.Getenv("HOME"), ".config", "aagent", "config.json"),
	}

	for _, path := range configPaths {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, err
			}
			break
		}
	}

	// Override with environment variables
	if model := os.Getenv("AAGENT_MODEL"); model != "" {
		cfg.DefaultModel = model
	}
	if dataPath := os.Getenv("AAGENT_DATA_PATH"); dataPath != "" {
		cfg.DataPath = dataPath
	}

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataPath, 0755); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
