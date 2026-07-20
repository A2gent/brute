package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func writeFakeClaudeCLI(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
case "$1" in
--version) echo "claude 9.9.9"; exit 0 ;;
--help) echo "models opus"; exit 0 ;;
esac
if [ "$1" = "auth" ] && [ "$2" = "status" ] && [ "$3" = "--json" ]; then
  echo '{"loggedIn":true,"email":"bob@example.com","subscription":"max"}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func newProviderTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewServer(cfg, nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
}

func TestListProvidersIncludesCustomClaudeInstancesWithoutSecrets(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers["anthropic:work"] = config.Provider{
		Name:             "Work Claude",
		Model:            "claude-opus-4-8",
		BinaryPath:       "/bin/claude",
		ClaudeConfigDir:  "/cfg/work",
		EnvOverrides:     map[string]string{"FOO": "bar"},
		SensitiveSecrets: map[string]string{"ANTHROPIC_API_KEY": "secret-value"},
	}
	server := &Server{config: cfg}

	req := httptest.NewRequest(http.MethodGet, "/providers/", nil)
	rec := httptest.NewRecorder()
	server.handleListProviders(rec, req)

	var providers []ProviderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var work *ProviderConfigResponse
	for i := range providers {
		if providers[i].Type == "anthropic:work" {
			work = &providers[i]
			break
		}
	}
	if work == nil {
		t.Fatal("missing anthropic:work provider")
	}
	if work.DisplayName != "Work Claude" {
		t.Fatalf("display name = %q", work.DisplayName)
	}
	if work.ConfigDir != "/cfg/work" {
		t.Fatalf("config dir = %q", work.ConfigDir)
	}
	if len(work.SensitiveSecretKeys) != 1 || work.SensitiveSecretKeys[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("secret keys = %v", work.SensitiveSecretKeys)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret-value") {
		t.Fatal("response leaked secret value")
	}
}

func TestClaudeHealthCacheInvalidatePrefix(t *testing.T) {
	cache := newClaudeHealthCache()
	report := claudecli.HealthReport{Status: claudecli.HealthHealthy}
	cache.Set("anthropic:work:abc", report)
	cache.Set("anthropic:personal:def", report)
	cache.Set("openai:xyz", report)

	cache.InvalidatePrefix("anthropic:work")
	if _, ok := cache.Get("anthropic:work:abc"); ok {
		t.Fatal("expected anthropic:work cache entry to be invalidated")
	}
	if _, ok := cache.Get("anthropic:personal:def"); !ok {
		t.Fatal("expected unrelated anthropic instance cache to remain")
	}
	if _, ok := cache.Get("openai:xyz"); !ok {
		t.Fatal("expected unrelated provider cache to remain")
	}
}

func TestClaudeHealthCacheKeyUsesResolvedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	server := &Server{}

	fromTilde := server.claudeHealthCacheKey("anthropic:work", config.Provider{ClaudeConfigDir: "~/.claude-work"})
	fromAbsolute := server.claudeHealthCacheKey("anthropic:work", config.Provider{ClaudeConfigDir: filepath.Join(home, ".claude-work")})
	if fromTilde != fromAbsolute {
		t.Fatalf("equivalent config dirs produced different health cache keys: tilde=%q absolute=%q", fromTilde, fromAbsolute)
	}
}

func TestClaudeCLIOptionsUseResolvedPathsWithoutMutatingUserConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(work)

	provider := config.Provider{
		BinaryPath:      "~/bin/claude",
		ClaudeConfigDir: "relative-config",
		HomePath:        "~/claude-home",
	}
	cfg := config.DefaultConfig()
	cfg.Providers["anthropic:work"] = provider
	server := &Server{config: cfg}

	opts := server.claudecliOptionsForRef("anthropic:work")
	if opts.Executable != filepath.Join(home, "bin", "claude") {
		t.Fatalf("executable = %q", opts.Executable)
	}
	if opts.ConfigDir != filepath.Join(work, "relative-config") {
		t.Fatalf("config dir = %q", opts.ConfigDir)
	}
	if opts.HomePath != filepath.Join(home, "claude-home") {
		t.Fatalf("home path = %q", opts.HomePath)
	}
	stored := cfg.Providers["anthropic:work"]
	if stored.BinaryPath != provider.BinaryPath || stored.ClaudeConfigDir != provider.ClaudeConfigDir || stored.HomePath != provider.HomePath {
		t.Fatalf("stored user paths were mutated: %+v", stored)
	}
}

func TestCreateClaudeInstanceAndHealthEndpoint(t *testing.T) {
	tmp := t.TempDir()
	binary := writeFakeClaudeCLI(t, tmp)
	t.Setenv("PATH", tmp)

	cfg := config.DefaultConfig()
	server := newProviderTestServer(t, cfg)

	body := `{"id":"work","name":"Work","binary_path":"` + binary + `","config_dir":"/cfg/work","sensitive_secrets":{"ANTHROPIC_API_KEY":"k"}}`
	req := httptest.NewRequest(http.MethodPost, "/providers/claude-instances", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/providers/anthropic%3Awork/health?refresh=true", nil)
	healthRec := httptest.NewRecorder()
	server.router.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", healthRec.Code, healthRec.Body.String())
	}
}

func TestHealthForMissingCustomClaudeInstanceReturnsNotFound(t *testing.T) {
	cfg := config.DefaultConfig()
	server := newProviderTestServer(t, cfg)
	req := httptest.NewRequest(http.MethodGet, "/providers/anthropic%3Amissing/health", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCreateClaudeInstanceDuplicateReturnsConflict(t *testing.T) {
	tmp := t.TempDir()
	binary := writeFakeClaudeCLI(t, tmp)
	t.Setenv("PATH", tmp)

	cfg := config.DefaultConfig()
	cfg.Providers["anthropic:work"] = config.Provider{Name: "Work"}
	server := newProviderTestServer(t, cfg)

	body := `{"id":"work","name":"Work 2","binary_path":"` + binary + `"}`
	req := httptest.NewRequest(http.MethodPost, "/providers/claude-instances", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateClaudeInstanceRejectsTrailingJSON(t *testing.T) {
	cfg := config.DefaultConfig()
	server := newProviderTestServer(t, cfg)
	body := `{"id":"work","name":"Work"}{"extra":true}`
	req := httptest.NewRequest(http.MethodPost, "/providers/claude-instances", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUpdateClaudeInstanceEnvOverridesReplaceMap(t *testing.T) {
	tmp := t.TempDir()
	binary := writeFakeClaudeCLI(t, tmp)
	t.Setenv("PATH", tmp)

	cfg := config.DefaultConfig()
	cfg.Providers["anthropic:work"] = config.Provider{
		Name:         "Work",
		BinaryPath:   binary,
		EnvOverrides: map[string]string{"KEEP": "1", "DROP": "old"},
	}
	server := newProviderTestServer(t, cfg)

	body := `{"env_overrides":{"KEEP":"2"}}`
	req := httptest.NewRequest(http.MethodPut, "/providers/anthropic%3Awork", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := server.config.Providers["anthropic:work"].EnvOverrides
	if got["KEEP"] != "2" {
		t.Fatalf("KEEP = %q, want 2", got["KEEP"])
	}
	if _, ok := got["DROP"]; ok {
		t.Fatalf("DROP should be removed on replacement, got %#v", got)
	}
}

func TestDeleteClaudeInstanceRefusesBaseAnthropic(t *testing.T) {
	cfg := config.DefaultConfig()
	server := newProviderTestServer(t, cfg)
	req := httptest.NewRequest(http.MethodDelete, "/providers/anthropic", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestClaudeHealthCacheHonorsTTL(t *testing.T) {
	cache := newClaudeHealthCache()
	cache.ttl = 30 * time.Second
	report := claudecli.HealthReport{Status: claudecli.HealthHealthy, Version: "1"}
	cache.Set("key", report)
	if _, ok := cache.Get("key"); !ok {
		t.Fatal("expected cached value")
	}
	cache.items["key"] = claudeHealthCacheEntry{
		report:    report,
		expiresAt: time.Now().UTC().Add(-time.Second),
	}
	if _, ok := cache.Get("key"); ok {
		t.Fatal("expected expired cache miss")
	}
}

func TestAnthropicTestUsesHealthProbeNotInference(t *testing.T) {
	tmp := t.TempDir()
	binary := writeFakeClaudeCLI(t, tmp)
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderAnthropic)] = config.Provider{BinaryPath: binary}
	server := &Server{config: cfg, claudeHealthCache: newClaudeHealthCache()}

	req := httptest.NewRequest(http.MethodPost, "/providers/anthropic/test", nil)
	rec := httptest.NewRecorder()
	server.handleTestClaudeProvider(rec, req, string(config.ProviderAnthropic))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ProviderTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success || !strings.Contains(resp.Message, "healthy") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestDeleteCustomClaudeInstanceClearsConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers["anthropic:work"] = config.Provider{Name: "Work"}
	server := newProviderTestServer(t, cfg)

	req := httptest.NewRequest(http.MethodDelete, "/providers/anthropic%3Awork", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := server.config.Providers["anthropic:work"]; ok {
		t.Fatal("instance should be removed")
	}
}
