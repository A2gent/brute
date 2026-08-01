package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds the application configuration
type Config struct {
	DefaultModel       string              `json:"default_model"`
	ActiveProvider     string              `json:"active_provider"` // Provider reference: built-in provider or named fallback aggregate
	MaxSteps           int                 `json:"max_steps"`
	Temperature        float64             `json:"temperature"`
	LLMRetries         int                 `json:"llm_retries"` // Number of retries per LLM provider on transient errors (default 3)
	DataPath           string              `json:"data_path"`
	WorkDir            string              `json:"work_dir"`
	CORSAllowedOrigins []string            `json:"cors_allowed_origins,omitempty"` // Allowed web origins for HTTP API CORS. Default: ["*"].
	Providers          map[string]Provider `json:"providers"`
	FallbackAggregates []FallbackAggregate `json:"fallback_aggregates,omitempty"`
	Tools              ToolsConfig         `json:"tools"`
}

// Provider configuration for LLM providers
type Provider struct {
	Name                  string              `json:"name"`
	APIKey                string              `json:"api_key"`
	BaseURL               string              `json:"base_url"`
	Model                 string              `json:"model"`
	PromptCacheKey        string              `json:"prompt_cache_key,omitempty"`
	ReasoningEffort       string              `json:"reasoning_effort,omitempty"`
	TextVerbosity         string              `json:"text_verbosity,omitempty"`
	ServiceTier           string              `json:"service_tier,omitempty"`
	MaxTokens             int                 `json:"max_tokens,omitempty"`
	StatefulResponses     *bool               `json:"stateful_responses,omitempty"`
	FallbackChain         []string            `json:"fallback_chain,omitempty"` // Legacy provider-only fallback nodes.
	FallbackChainNodes    []FallbackChainNode `json:"fallback_chain_nodes,omitempty"`
	RouterProvider        string              `json:"router_provider,omitempty"` // Provider reference used by automatic router (direct provider or fallback chain).
	RouterModel           string              `json:"router_model,omitempty"`    // Optional model override for direct router provider.
	RouterReasoningEffort string              `json:"router_reasoning_effort,omitempty"`
	RouterRules           []RouterRule        `json:"router_rules,omitempty"`
	ContextWindow         int                 `json:"context_window,omitempty"` // in tokens

	// Claude CLI instance fields (anthropic and anthropic:<id> providers).
	BinaryPath       string            `json:"binary_path,omitempty"`
	ClaudeConfigDir  string            `json:"claude_config_dir,omitempty"`
	HomePath         string            `json:"home_path,omitempty"`
	EnvOverrides     map[string]string `json:"env_overrides,omitempty"`
	SensitiveSecrets map[string]string `json:"sensitive_secrets,omitempty"`

	// OAuth support
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// OAuthConfig stores OAuth tokens for a provider
type OAuthConfig struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp
}

// FallbackChainNode stores a single fallback step with explicit provider+model.
type FallbackChainNode struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// FallbackAggregate stores a named fallback chain that can be selected like a provider.
type FallbackAggregate struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Chain []FallbackChainNode `json:"chain"`
}

// RouterRule maps a textual task context to a target model/provider.
type RouterRule struct {
	Match           string `json:"match"`
	Provider        string `json:"provider"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ProviderType identifies the type of provider
type ProviderType string

const (
	ProviderKimi        ProviderType = "kimi"
	ProviderKimiCLI     ProviderType = "kimi_cli"
	ProviderOpenRouter  ProviderType = "openrouter"
	ProviderOpenCodeZen ProviderType = "opencode_zen"
	ProviderLMStudio    ProviderType = "lmstudio"
	ProviderAnthropic   ProviderType = "anthropic"
	ProviderCursor      ProviderType = "cursor"
	ProviderGoogle      ProviderType = "google"
	ProviderOpenAI      ProviderType = "openai"
	ProviderOpenAICodex ProviderType = "openai_codex"
	ProviderGrok        ProviderType = "grok"
	ProviderFallback    ProviderType = "fallback_chain"
	ProviderAutoRouter  ProviderType = "automatic_router"
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
			Type:          ProviderKimiCLI,
			DisplayName:   "Kimi (Kimi Code CLI)",
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "kimi-code/kimi-for-coding",
			ContextWindow: 262144,
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
			Type:          ProviderOpenCodeZen,
			DisplayName:   "OpenCode Zen",
			DefaultURL:    "https://opencode.ai/zen/v1",
			RequiresKey:   false,
			DefaultModel:  "big-pickle",
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
			DisplayName:   "Anthropic Claude (Claude CLI)",
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "claude-opus-4-8",
			ContextWindow: 200000,
		},
		{
			Type:          ProviderCursor,
			DisplayName:   "Cursor Composer (Cursor CLI)",
			DefaultURL:    "",
			RequiresKey:   false,
			DefaultModel:  "composer-2.5",
			ContextWindow: 0,
		},
		{
			Type:          ProviderGoogle,
			DisplayName:   "Google Gemini",
			DefaultURL:    "https://generativelanguage.googleapis.com/v1beta/openai",
			RequiresKey:   true,
			DefaultModel:  "gemini-3-flash-preview",
			ContextWindow: 1048576,
		},
		{
			Type:          ProviderOpenAI,
			DisplayName:   "OpenAI",
			DefaultURL:    "https://api.openai.com/v1",
			RequiresKey:   true,
			DefaultModel:  "gpt-5.5",
			ContextWindow: 1000000,
		},
		{
			Type:          ProviderOpenAICodex,
			DisplayName:   "OpenAI (Codex OAuth)",
			DefaultURL:    "https://chatgpt.com/backend-api/codex",
			RequiresKey:   true,
			DefaultModel:  "gpt-5.5",
			ContextWindow: 1000000,
		},
		{
			Type:          ProviderGrok,
			DisplayName:   "Grok (x.ai)",
			DefaultURL:    "https://api.x.ai/v1",
			RequiresKey:   true,
			DefaultModel:  "grok-4.5",
			ContextWindow: 131072,
		},
	}
}

// TestableProviders returns providers that support direct connectivity tests.
// Fallback chains and the auto router are aggregates, not single LLM endpoints.
func TestableProviders() []ProviderDefinition {
	all := SupportedProviders()
	out := make([]ProviderDefinition, 0, len(all))
	for _, def := range all {
		if def.Type == ProviderFallback || def.Type == ProviderAutoRouter {
			continue
		}
		out = append(out, def)
	}
	return out
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
	workDir, _ := os.Getwd()

	return &Config{
		DefaultModel:   "kimi-k2.5",
		ActiveProvider: string(ProviderKimi),
		// Complex coding runs routinely exceed the old 50-step budget.
		MaxSteps:    100,
		Temperature: 0.0,
		LLMRetries:  3,
		DataPath:    resolveDataPath(),
		WorkDir:     workDir,
		CORSAllowedOrigins: []string{
			"*",
		},
		Providers: make(map[string]Provider),
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

func resolveDataPath() string {
	if dataPath := os.Getenv("AAGENT_DATA_PATH"); dataPath != "" {
		return dataPath
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "share", "aagent")
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

// IsValidProvider checks if a provider type is valid/supported
func (c *Config) IsValidProvider(ptype ProviderType) bool {
	return GetProviderDefinition(ptype) != nil
}

// GetConfigPath returns the path where config should be saved
func GetConfigPath() string {
	if path := os.Getenv("AAGENT_CONFIG_PATH"); path != "" {
		return path
	}
	return filepath.Join(resolveDataPath(), "config.json")
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Override with environment variables
	if provider := NormalizeProviderRef(os.Getenv("AAGENT_PROVIDER")); provider != "" {
		cfg.ActiveProvider = provider
	}
	if model := os.Getenv("AAGENT_MODEL"); model != "" {
		cfg.DefaultModel = model
	}
	if dataPath := os.Getenv("AAGENT_DATA_PATH"); dataPath != "" {
		cfg.DataPath = dataPath
	}
	if retriesStr := os.Getenv("AAGENT_LLM_RETRIES"); retriesStr != "" {
		if retries, err := strconv.Atoi(retriesStr); err == nil && retries >= 0 {
			cfg.LLMRetries = retries
		}
	}
	if origins := normalizeCORSAllowedOrigins(os.Getenv("A2GENT_CORS_ALLOWED_ORIGINS")); len(origins) > 0 {
		cfg.CORSAllowedOrigins = origins
	} else if origin := strings.TrimSpace(os.Getenv("A2GENT_CORS_ALLOWED_ORIGIN")); origin != "" {
		cfg.CORSAllowedOrigins = []string{origin}
	}

	// Try to load from config file. Prefer single-folder location next to DB
	// while retaining legacy paths for backward compatibility.
	homeDir, _ := os.UserHomeDir()
	configPaths := []string{
		GetConfigPath(),
		".aagent/config.json",
		filepath.Join(homeDir, ".config", "aagent", "config.json"),
	}

	for _, path := range configPaths {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, err
			}
			break
		}
	}

	// Apply after file load so operators can raise the budget without editing
	// a secrets-bearing config.json (and without fighting a stale max_steps value).
	if maxStepsStr := strings.TrimSpace(os.Getenv("AAGENT_MAX_STEPS")); maxStepsStr != "" {
		if maxSteps, err := strconv.Atoi(maxStepsStr); err == nil && maxSteps > 0 {
			cfg.MaxSteps = maxSteps
		}
	}

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataPath, 0755); err != nil {
		return nil, err
	}
	cfg.CORSAllowedOrigins = normalizeCORSAllowedOriginsOrDefault(cfg.CORSAllowedOrigins)

	return cfg, nil
}

// EffectiveCORSAllowedOrigins returns normalized configured origins, or wildcard fallback.
func (c *Config) EffectiveCORSAllowedOrigins() []string {
	if c == nil {
		return []string{"*"}
	}
	return normalizeCORSAllowedOriginsOrDefault(c.CORSAllowedOrigins)
}

func normalizeCORSAllowedOriginsOrDefault(origins []string) []string {
	normalized := normalizeCORSAllowedOrigins(strings.Join(origins, ","))
	if len(normalized) == 0 {
		return []string{"*"}
	}
	return normalized
}

func normalizeCORSAllowedOrigins(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		out = append(out, origin)
	}
	return out
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

	// Preserve the last on-disk configuration before replacing it. This
	// makes accidental sparse saves recoverable and avoids exposing secrets via
	// permissive default file modes.
	previous, err := os.ReadFile(path)
	if err == nil {
		if err := writePrivateFileAtomically(path+".bak", previous); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return writePrivateFileAtomically(path, data)
}

func writePrivateFileAtomically(path string, data []byte) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0600); err != nil {
		retErr = err
		return retErr
	}
	if _, err := tmp.Write(data); err != nil {
		retErr = err
		return retErr
	}
	if err := tmp.Sync(); err != nil {
		retErr = err
		return retErr
	}
	if err := tmp.Close(); err != nil {
		retErr = err
		return retErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		retErr = err
		return retErr
	}
	return nil
}
