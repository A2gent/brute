package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderRefHelpers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trims and lowercases provider", raw: "  OpenAI_Codex  ", want: "openai_codex"},
		{name: "decodes url escaped refs", raw: "Fallback_Chain%3AMy%20Chain", want: "fallback_chain:my chain"},
		{name: "keeps invalid escapes as literal text", raw: "OpenAI%ZZ", want: "openai%zz"},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeProviderRef(tt.raw); got != tt.want {
				t.Fatalf("NormalizeProviderRef(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	if !IsFallbackAggregateRef(" FALLBACK_CHAIN%3Aresearch ") {
		t.Fatal("expected encoded fallback-chain ref to be detected")
	}
	if IsFallbackAggregateRef("openai") {
		t.Fatal("plain provider should not be detected as fallback aggregate")
	}

	if got := FallbackAggregateIDFromRef(" fallback_chain%3Aresearch-team "); got != "research-team" {
		t.Fatalf("FallbackAggregateIDFromRef returned %q", got)
	}
	if got := FallbackAggregateIDFromRef("openai"); got != "" {
		t.Fatalf("non-fallback ref should not have aggregate id, got %q", got)
	}

	if got := FallbackAggregateRefFromID(" Research Team/Prod! "); got != "fallback_chain:research-team-prod" {
		t.Fatalf("FallbackAggregateRefFromID returned %q", got)
	}
	if got := FallbackAggregateRefFromID(" !!! "); got != "" {
		t.Fatalf("empty normalized aggregate id should return empty ref, got %q", got)
	}

	if got := NormalizeToken(" Research%20Team/Prod! "); got != "research-team-prod" {
		t.Fatalf("NormalizeToken returned %q", got)
	}
}

func TestTestableProvidersExcludeAggregates(t *testing.T) {
	t.Parallel()

	for _, def := range TestableProviders() {
		if def.Type == ProviderFallback || def.Type == ProviderAutoRouter {
			t.Fatalf("TestableProviders includes aggregate provider %q", def.Type)
		}
	}

	all := SupportedProviders()
	if len(TestableProviders()) >= len(all) {
		t.Fatalf("TestableProviders() should be smaller than SupportedProviders(); got %d vs %d", len(TestableProviders()), len(all))
	}
}

func TestSupportedProviderDefinitionsAreUniqueAndDiscoverable(t *testing.T) {
	t.Parallel()

	defs := SupportedProviders()
	if len(defs) == 0 {
		t.Fatal("SupportedProviders returned no provider definitions")
	}

	seen := make(map[ProviderType]struct{}, len(defs))
	for _, def := range defs {
		if def.Type == "" {
			t.Fatal("provider definition has empty type")
		}
		if def.DisplayName == "" {
			t.Fatalf("provider %q has empty display name", def.Type)
		}
		if _, ok := seen[def.Type]; ok {
			t.Fatalf("duplicate provider definition for %q", def.Type)
		}
		seen[def.Type] = struct{}{}

		got := GetProviderDefinition(def.Type)
		if got == nil {
			t.Fatalf("GetProviderDefinition(%q) returned nil", def.Type)
		}
		if got.Type != def.Type || got.DefaultModel != def.DefaultModel || got.DefaultURL != def.DefaultURL {
			t.Fatalf("definition lookup mismatch for %q: got %#v, want %#v", def.Type, got, def)
		}
	}

	if got := GetProviderDefinition("not-a-provider"); got != nil {
		t.Fatalf("unknown provider should not resolve, got %#v", got)
	}
}

func TestGrokProviderDefinition(t *testing.T) {
	t.Parallel()

	def := GetProviderDefinition(ProviderGrok)
	if def == nil {
		t.Fatal("GetProviderDefinition(grok) returned nil")
	}
	if def.Type != ProviderGrok {
		t.Fatalf("type = %q, want %q", def.Type, ProviderGrok)
	}
	if def.DisplayName != "Grok (x.ai)" {
		t.Fatalf("display name = %q", def.DisplayName)
	}
	if def.DefaultURL != "https://api.x.ai/v1" {
		t.Fatalf("default URL = %q", def.DefaultURL)
	}
	if def.DefaultModel != "grok-4.5" {
		t.Fatalf("default model = %q", def.DefaultModel)
	}
	if !def.RequiresKey {
		t.Fatal("grok provider should require an API key")
	}
}

func TestResolveContextWindowUsesModelSpecificOpenRouterLimits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider Provider
		model    string
		want     int
	}{
		{
			name:  "owl alpha short openrouter id gets one million tokens",
			model: "openrouter/owl-alpha",
			want:  1000000,
		},
		{
			name:  "owl alpha nested openrouter id gets one million tokens",
			model: "openrouter/openrouter/owl-alpha",
			want:  1000000,
		},
		{
			name:  "other openrouter model keeps provider default",
			model: "openrouter/auto",
			want:  128000,
		},
		{
			name:     "configured provider context window wins",
			provider: Provider{ContextWindow: 64000},
			model:    "openrouter/openrouter/owl-alpha",
			want:     64000,
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ResolveContextWindow(ProviderOpenRouter, tt.provider, tt.model); got != tt.want {
				t.Fatalf("ResolveContextWindow(openrouter, %#v, %q) = %d, want %d", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestDefaultConfigUsesEnvironmentDataPathAndSafeToolDefaults(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	t.Setenv("AAGENT_DATA_PATH", dataPath)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}

	cfg := DefaultConfig()
	if cfg.DataPath != dataPath {
		t.Fatalf("DataPath = %q, want %q", cfg.DataPath, dataPath)
	}
	if cfg.WorkDir != cwd {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, cwd)
	}
	if cfg.ActiveProvider != string(ProviderKimi) || cfg.DefaultModel == "" {
		t.Fatalf("unexpected default provider/model: provider=%q model=%q", cfg.ActiveProvider, cfg.DefaultModel)
	}
	if cfg.LLMRetries != 3 {
		t.Fatalf("LLMRetries = %d, want 3", cfg.LLMRetries)
	}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, []string{"*"}) {
		t.Fatalf("CORSAllowedOrigins = %#v, want wildcard", cfg.CORSAllowedOrigins)
	}
	if cfg.Providers == nil {
		t.Fatal("Providers map should be initialized")
	}

	tools := map[string]string{
		"bash":  cfg.Tools.Bash,
		"read":  cfg.Tools.Read,
		"write": cfg.Tools.Write,
		"edit":  cfg.Tools.Edit,
		"glob":  cfg.Tools.Glob,
		"grep":  cfg.Tools.Grep,
		"task":  cfg.Tools.Task,
	}
	for name, permission := range tools {
		if permission != "allow" {
			t.Fatalf("default %s tool permission = %q, want allow", name, permission)
		}
	}
}

func TestProviderConfigHelpers(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		ActiveProvider: string(ProviderGoogle),
		Providers:      make(map[string]Provider),
	}
	cfg.SetProvider(ProviderGoogle, Provider{Model: "gemini-test"})

	if got := cfg.Providers[string(ProviderGoogle)].Name; got != string(ProviderGoogle) {
		t.Fatalf("SetProvider stored provider name %q", got)
	}
	active := cfg.GetActiveProvider()
	if active == nil || active.Model != "gemini-test" {
		t.Fatalf("GetActiveProvider returned %#v", active)
	}
	if !cfg.IsValidProvider(ProviderGoogle) {
		t.Fatal("expected google provider to be valid")
	}
	if cfg.IsValidProvider("not-a-provider") {
		t.Fatal("unexpected valid result for unknown provider")
	}

	cfg.ActiveProvider = string(ProviderOpenAI)
	if active := cfg.GetActiveProvider(); active != nil {
		t.Fatalf("missing active provider should return nil, got %#v", active)
	}
}

func TestGetConfigPathHonorsExplicitPathBeforeDataPath(t *testing.T) {
	tmp := t.TempDir()
	explicitPath := filepath.Join(tmp, "custom", "config.json")
	t.Setenv("AAGENT_CONFIG_PATH", explicitPath)
	t.Setenv("AAGENT_DATA_PATH", filepath.Join(tmp, "data"))

	if got := GetConfigPath(); got != explicitPath {
		t.Fatalf("GetConfigPath = %q, want %q", got, explicitPath)
	}
}

func TestGetConfigPathDefaultsUnderDataPath(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	t.Setenv("AAGENT_CONFIG_PATH", "")
	t.Setenv("AAGENT_DATA_PATH", dataPath)

	want := filepath.Join(dataPath, "config.json")
	if got := GetConfigPath(); got != want {
		t.Fatalf("GetConfigPath = %q, want %q", got, want)
	}
}

func TestLoadUsesEnvironmentWhenNoConfigFileExists(t *testing.T) {
	tmp := t.TempDir()
	dataPath := filepath.Join(tmp, "data")
	isolateLoadFileLookup(t, filepath.Join(tmp, "missing-config.json"))
	t.Setenv("AAGENT_PROVIDER", " OpenAI_Codex ")
	t.Setenv("AAGENT_MODEL", "gpt-test")
	t.Setenv("AAGENT_DATA_PATH", dataPath)
	t.Setenv("AAGENT_LLM_RETRIES", "5")
	t.Setenv("A2GENT_CORS_ALLOWED_ORIGINS", " https://app.example.com, https://app.example.com, http://localhost:5173 ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.ActiveProvider != string(ProviderOpenAICodex) {
		t.Fatalf("ActiveProvider = %q, want %q", cfg.ActiveProvider, ProviderOpenAICodex)
	}
	if cfg.DefaultModel != "gpt-test" || cfg.DataPath != dataPath || cfg.LLMRetries != 5 {
		t.Fatalf("Load did not apply env overrides: %#v", cfg)
	}
	wantOrigins := []string{"https://app.example.com", "http://localhost:5173"}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, wantOrigins) {
		t.Fatalf("CORSAllowedOrigins = %#v, want %#v", cfg.CORSAllowedOrigins, wantOrigins)
	}
	if info, err := os.Stat(dataPath); err != nil || !info.IsDir() {
		t.Fatalf("Load should create data directory %q, stat=%#v err=%v", dataPath, info, err)
	}
}

func TestLoadReadsConfiguredFileAndNormalizesOrigins(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")
	dataPath := filepath.Join(tmp, "configured-data")
	isolateLoadFileLookup(t, configPath)

	raw := `{
  "default_model": "gemini-file",
  "active_provider": "google",
  "llm_retries": 2,
  "data_path": ` + quoteJSON(t, dataPath) + `,
  "cors_allowed_origins": [" https://file.example.com ", "https://file.example.com"],
  "providers": {
    "google": {"model": "gemini-file"}
  }
}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.ActiveProvider != string(ProviderGoogle) || cfg.DefaultModel != "gemini-file" || cfg.LLMRetries != 2 {
		t.Fatalf("unexpected loaded config: %#v", cfg)
	}
	if active := cfg.GetActiveProvider(); active == nil || active.Model != "gemini-file" {
		t.Fatalf("loaded provider was not available as active provider: %#v", active)
	}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, []string{"https://file.example.com"}) {
		t.Fatalf("CORSAllowedOrigins = %#v", cfg.CORSAllowedOrigins)
	}
	if info, err := os.Stat(dataPath); err != nil || !info.IsDir() {
		t.Fatalf("Load should create configured data directory %q, stat=%#v err=%v", dataPath, info, err)
	}
}

func TestSaveCreatesParentDirectoryAndWritesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg := &Config{
		DefaultModel:       "test-model",
		ActiveProvider:     string(ProviderOpenAI),
		MaxSteps:           7,
		Temperature:        0.25,
		LLMRetries:         1,
		DataPath:           "/tmp/aagent-test-data",
		CORSAllowedOrigins: []string{"https://app.example.com"},
		Providers: map[string]Provider{
			string(ProviderOpenAI): {Name: string(ProviderOpenAI), Model: "gpt-test"},
		},
	}

	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), "\n  \"default_model\":") {
		t.Fatalf("expected indented JSON, got %s", string(data))
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("saved JSON did not decode: %v", err)
	}
	if decoded.DefaultModel != cfg.DefaultModel || decoded.ActiveProvider != cfg.ActiveProvider || decoded.Providers[string(ProviderOpenAI)].Model != "gpt-test" {
		t.Fatalf("decoded config mismatch: %#v", decoded)
	}
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}
	return string(data)
}

func isolateLoadFileLookup(t *testing.T, configPath string) {
	t.Helper()

	// Keep Load from falling through to a developer's real legacy config paths.
	// The test only wants the explicit fixture path plus empty temp HOME/CWD.
	t.Setenv("AAGENT_CONFIG_PATH", configPath)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Chdir(t.TempDir())
}
