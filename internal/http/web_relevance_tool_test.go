package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/tools"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

func TestWebRelevanceRegistration(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
	server, store := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)
	for _, name := range []string{"fetch_url", "tavily_search", "exa_search", "brave_search_query"} {
		tool, ok := server.toolManager.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if _, ok := tool.Schema()["properties"].(map[string]interface{})["task_context"]; ok {
			t.Fatal("disabled by default")
		}
	}
	server.config.Tools.WebRelevance.Enabled = true
	server.toolManagerForSession(nil)
	projectDir := t.TempDir()
	project := &storage.Project{ID: "web-project", Name: "Web", Folder: &projectDir}
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	manager := server.toolManagerForSession(&session.Session{ID: "web-session", ProjectID: &project.ID})
	tool, ok := manager.Get("fetch_url")
	if !ok {
		t.Fatal("project tool missing")
	}
	if _, ok := tool.(*integrationtools.WebRelevanceTool); !ok {
		t.Fatal("project tool not wrapped")
	}
	if _, ok := tool.Schema()["properties"].(map[string]interface{})["filter_results"]; !ok {
		t.Fatal("missing recovery switch")
	}
	server.config.Tools.WebRelevance.Enabled = false
	server.toolManagerForSession(nil)
	tool, _ = server.toolManager.Get("fetch_url")
	if _, ok := tool.Schema()["properties"].(map[string]interface{})["task_context"]; ok {
		t.Fatal("flag refresh failed")
	}
}

type webRegistrationSource struct{ output string }

func (*webRegistrationSource) Name() string        { return "fetch_url" }
func (*webRegistrationSource) Description() string { return "fixture" }
func (*webRegistrationSource) Schema() map[string]interface{} {
	return map[string]interface{}{"properties": map[string]interface{}{}}
}
func (s *webRegistrationSource) Execute(context.Context, json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true, Output: s.output}, nil
}

type webFailIntegrationStore struct{ storage.Store }

func (webFailIntegrationStore) ListIntegrations() ([]*storage.Integration, error) {
	return nil, fmt.Errorf("fixture store failure")
}

func TestWebRelevanceCredentialRefresh(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	calls := 0
	fake := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		calls++
		_, _ = w.Write([]byte(`{"answers":{"item_0":{"score":4,"confidence":0.99}}}`))
	}))
	defer fake.Close()
	provider := config.Provider{APIKey: "jev-credential", BaseURL: fake.URL, OAuth: &config.OAuthConfig{AccessToken: "opaque-oauth", RefreshToken: "opaque-refresh"}, SensitiveSecrets: map[string]string{"private": "opaque-sensitive"}, EnvOverrides: map[string]string{"SERVICE_TOKEN": "opaque-env"}}
	server, _ := newClassifyRegistrationTestServer(t, provider, nil)
	server.config.Tools.WebRelevance.Enabled = true
	for _, secret := range []string{"opaque-oauth", "opaque-refresh", "opaque-sensitive", "opaque-env"} {
		server.toolManager.Register(&webRegistrationSource{output: "## Heading\n" + secret})
		server.registerWebRelevanceTools(server.toolManager)
		tool, _ := server.toolManager.Get("fetch_url")
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"task_context":"goal"}`))
		if err != nil || calls != 0 || !strings.Contains(result.Output, secret) {
			t.Fatalf("configured secret escaped: %s", secret)
		}
	}
	server.toolManager.Register(&webRegistrationSource{output: "## Heading\npublic text"})
	server.registerWebRelevanceTools(server.toolManager)
	tool, _ := server.toolManager.Get("fetch_url")
	_, _ = tool.Execute(context.Background(), json.RawMessage(`{"task_context":"goal"}`))
	if calls != 1 {
		t.Fatalf("expected live client, calls=%d", calls)
	}
	server.config.Providers["jev"] = config.Provider{}
	server.registerWebRelevanceTools(server.toolManager)
	tool, _ = server.toolManager.Get("fetch_url")
	_, _ = tool.Execute(context.Background(), json.RawMessage(`{"task_context":"goal"}`))
	if calls != 1 {
		t.Fatal("removed key still active")
	}
	server.config.Providers["jev"] = provider
	server.registerWebRelevanceTools(server.toolManager)
	server.store = webFailIntegrationStore{server.store}
	server.registerWebRelevanceTools(server.toolManager)
	tool, _ = server.toolManager.Get("fetch_url")
	_, _ = tool.Execute(context.Background(), json.RawMessage(`{"task_context":"goal"}`))
	if calls != 1 {
		t.Fatal("failed secret inventory retained stale client")
	}
}
