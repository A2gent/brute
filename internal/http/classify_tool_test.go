package http

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func newClassifyRegistrationTestServer(t *testing.T, provider config.Provider, integrations []*storage.Integration) (*Server, *storage.SQLiteStore) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, integration := range integrations {
		integration.CreatedAt = time.Now()
		integration.UpdatedAt = integration.CreatedAt
		if err := store.SaveIntegration(integration); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.WorkDir = t.TempDir()
	cfg.DataPath = t.TempDir()
	cfg.Providers["jev"] = provider
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), session.NewManager(store), store, speechcache.New(0), 0)
	return server, store
}

func TestClassifyRegistrationRequiresCredentials(t *testing.T) {
	cases := []struct {
		name         string
		providerKey  string
		envKey       string
		integrations []*storage.Integration
		want         bool
	}{
		{name: "absent key"},
		{name: "whitespace keys", providerKey: " \t", envKey: " \n"},
		{name: "configured provider", providerKey: " provider-key ", want: true},
		{name: "environment fallback", envKey: " env-key ", want: true},
		{name: "enabled integration", integrations: []*storage.Integration{{ID: "jev", Provider: "jev", Enabled: true, Config: map[string]string{"api_key": " integration-key "}}}, want: true},
		{name: "disabled integration", integrations: []*storage.Integration{{ID: "jev", Provider: "jev", Enabled: false, Config: map[string]string{"api_key": "integration-key"}}}},
		{name: "blank integration key", integrations: []*storage.Integration{{ID: "jev", Provider: "jev", Enabled: true, Config: map[string]string{"api_key": " \t"}}}},
		{name: "unrelated integration", integrations: []*storage.Integration{{ID: "other", Provider: "openai", Enabled: true, Config: map[string]string{"api_key": "other-key"}}}},
		{name: "skip disabled and blank integrations", integrations: []*storage.Integration{
			{ID: "disabled", Provider: "jev", Config: map[string]string{"api_key": "disabled-key"}},
			{ID: "blank", Provider: "jev", Enabled: true, Config: map[string]string{"api_key": " "}},
			{ID: "usable", Provider: "jev", Enabled: true, Config: map[string]string{"api_key": "usable-key"}},
		}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", tc.envKey)
			t.Setenv(disableToolsByDefaultSettingKey, "false")
			t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
			server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: tc.providerKey}, tc.integrations)
			if _, got := server.toolManager.Get("classify"); got != tc.want {
				t.Fatalf("classify registered = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyRegistrationUsesProjectManagerWorkDir(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
	server, store := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)
	projectDir := t.TempDir()
	project := &storage.Project{ID: "classify-project", Name: "Classify project", Folder: &projectDir}
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	manager := server.toolManagerForSession(&session.Session{ID: "classify-session", ProjectID: &project.ID})
	if got := manager.WorkDir(); got != projectDir {
		t.Fatalf("project manager workdir = %q, want %q", got, projectDir)
	}
	tool, ok := manager.Get("classify")
	if !ok {
		t.Fatal("classify not registered for project manager")
	}
	// Verify the constructor received this manager's cwd, not the server's cwd.
	workDir := reflect.ValueOf(tool).Elem().FieldByName("workDir")
	if !workDir.IsValid() || workDir.Kind() != reflect.String || workDir.String() != projectDir {
		t.Fatalf("classify tool does not use project workdir %q", projectDir)
	}
}

func TestClassifyCredentialsPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		provider  config.Provider
		envKey    string
		wantKey   string
		wantModel string
		wantURL   string
	}{
		{name: "provider preferred", provider: config.Provider{APIKey: " provider-key ", Model: " provider-model ", BaseURL: " https://provider.example/v1 "}, envKey: "env-key", wantKey: "provider-key", wantModel: "provider-model", wantURL: "https://provider.example/v1"},
		{name: "env preferred to integration", provider: config.Provider{APIKey: " \t", Model: " provider-model ", BaseURL: " https://provider.example/v1 "}, envKey: " env-key ", wantKey: "env-key", wantModel: "provider-model", wantURL: "https://provider.example/v1"},
		{name: "integration fallback", provider: config.Provider{Model: "unused-model", BaseURL: "https://unused.example"}, envKey: " \t", wantKey: "integration-key", wantModel: "integration-model", wantURL: "https://integration.example/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", tc.envKey)
			server, _ := newClassifyRegistrationTestServer(t, tc.provider, []*storage.Integration{{
				ID: "jev", Provider: "jev", Enabled: true,
				Config: map[string]string{"api_key": " integration-key ", "model": " integration-model ", "base_url": " https://integration.example/v1 "},
			}})
			key, model, baseURL := server.resolveClassifyCredentials()
			if key != tc.wantKey || model != tc.wantModel || baseURL != tc.wantURL {
				t.Fatalf("credentials = (%q, %q, %q), want (%q, %q, %q)", key, model, baseURL, tc.wantKey, tc.wantModel, tc.wantURL)
			}
		})
	}
}

func TestClassifyCredentialsNilConfigAndStore(t *testing.T) {
	for _, key := range []string{"", " env-key "} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("TYPESAFE_API_KEY", key)
			server := &Server{}
			gotKey, model, baseURL := server.resolveClassifyCredentials()
			wantKey := ""
			if key != "" {
				wantKey = "env-key"
			}
			if gotKey != wantKey || model != "" || baseURL != "" {
				t.Fatalf("credentials = (%q, %q, %q), want (%q, empty, empty)", gotKey, model, baseURL, wantKey)
			}
		})
	}
}

func TestClassifyRegistrationRemovesToolWhenCredentialsDisappear(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)
	if _, ok := server.toolManager.Get("classify"); !ok {
		t.Fatal("classify not registered with credentials")
	}
	server.config.Providers["jev"] = config.Provider{}
	server.registerServerBackedTools(server.toolManager)
	if _, ok := server.toolManager.Get("classify"); ok {
		t.Fatal("classify remains registered without credentials")
	}
}

func TestClassifyRegistrationRefreshesForSession(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	server.config.Providers["jev"] = config.Provider{APIKey: "new-key"}
	if _, ok := server.toolManagerForSession(nil).Get("classify"); !ok {
		t.Fatal("new key not reflected in session manager")
	}
	server.config.Providers["jev"] = config.Provider{}
	if _, ok := server.toolManagerForSession(nil).Get("classify"); ok {
		t.Fatal("removed key still exposed")
	}
}

func TestClassifyIntegrationCanBeCreatedThroughHTTP(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	request := httptest.NewRequest("POST", "/integrations", strings.NewReader(`{"provider":"jev","mode":"notify_only","config":{"api_key":"key"}}`))
	recorder := httptest.NewRecorder()
	server.handleCreateIntegration(recorder, request)
	if recorder.Code != 201 {
		t.Fatalf("create: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := server.toolManagerForSession(nil).Get("classify"); !ok {
		t.Fatal("created integration not registered")
	}
}
