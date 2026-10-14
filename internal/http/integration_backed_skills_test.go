package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestHandleListIntegrationBackedSkills_IncludesTavilyPerplexityJiraCircleCIAndAppSignalTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	for _, integration := range []*storage.Integration{
		{
			ID:        "integ-tavily",
			Provider:  "tavily",
			Name:      "Tavily",
			Mode:      "notify_only",
			Enabled:   true,
			Config:    map[string]string{"api_key": "tvly-test"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "integ-perplexity",
			Provider:  "perplexity",
			Name:      "Perplexity",
			Mode:      "notify_only",
			Enabled:   true,
			Config:    map[string]string{"api_key": "pplx-test"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       "integ-jira",
			Provider: "jira",
			Name:     "Jira",
			Mode:     "notify_only",
			Enabled:  true,
			Config: map[string]string{
				"base_url":  "https://example.atlassian.net",
				"email":     "user@example.com",
				"api_token": "jira-token",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "integ-circleci",
			Provider:  "circleci",
			Name:      "CircleCI",
			Mode:      "notify_only",
			Enabled:   true,
			Config:    map[string]string{"api_token": "circle-token"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "integ-appsignal",
			Provider:  "appsignal",
			Name:      "AppSignal",
			Mode:      "notify_only",
			Enabled:   true,
			Config:    map[string]string{"api_key": "mcp-test"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	} {
		if err := store.SaveIntegration(integration); err != nil {
			t.Fatalf("failed to save integration %s: %v", integration.Provider, err)
		}
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	req := httptest.NewRequest(http.MethodGet, "/skills/integration-backed", nil)
	rec := httptest.NewRecorder()
	server.handleListIntegrationBackedSkills(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationBackedSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	found := map[string]IntegrationBackedSkill{}
	for _, skill := range resp.Skills {
		found[skill.Provider] = skill
	}

	if got, ok := found["tavily"]; !ok {
		t.Fatalf("expected tavily integration-backed skill in response: %#v", resp.Skills)
	} else if len(got.Tools) != 1 || got.Tools[0].Name != "tavily_search" {
		t.Fatalf("expected tavily_search tool, got %#v", got.Tools)
	}

	if got, ok := found["perplexity"]; !ok {
		t.Fatalf("expected perplexity integration-backed skill in response: %#v", resp.Skills)
	} else if len(got.Tools) != 1 || got.Tools[0].Name != "perplexity_search" {
		t.Fatalf("expected perplexity_search tool, got %#v", got.Tools)
	}

	if got, ok := found["jira"]; !ok {
		t.Fatalf("expected jira integration-backed skill in response: %#v", resp.Skills)
	} else if len(got.Tools) != 1 || got.Tools[0].Name != "jira_query" {
		t.Fatalf("expected jira_query tool, got %#v", got.Tools)
	}

	if got, ok := found["circleci"]; !ok {
		t.Fatalf("expected circleci integration-backed skill in response: %#v", resp.Skills)
	} else if len(got.Tools) != 1 || got.Tools[0].Name != "circleci_query" {
		t.Fatalf("expected circleci_query tool, got %#v", got.Tools)
	}

	if got, ok := found["appsignal"]; !ok {
		t.Fatalf("expected appsignal integration-backed skill in response: %#v", resp.Skills)
	} else if len(got.Tools) != 1 || got.Tools[0].Name != "appsignal_query" {
		t.Fatalf("expected appsignal_query tool, got %#v", got.Tools)
	}
}

func TestIntegrationCreateAcceptsTavilyProviderAndMasksSecret(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "tavily",
		Mode:     "notify_only",
		Config: map[string]string{
			"api_key": "tvly-secret",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "tavily" {
		t.Fatalf("expected provider tavily, got %q", resp.Provider)
	}
	if resp.Name != "Tavily" {
		t.Fatalf("expected default name Tavily, got %q", resp.Name)
	}
	if got := resp.Config["api_key"]; got != "***" {
		t.Fatalf("expected masked api_key in response, got %q", got)
	}
}

func TestIntegrationCreateAcceptsJiraProviderAndMasksSecret(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "jira",
		Mode:     "notify_only",
		Config: map[string]string{
			"base_url":  "https://example.atlassian.net",
			"email":     "user@example.com",
			"api_token": "jira-secret",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "jira" {
		t.Fatalf("expected provider jira, got %q", resp.Provider)
	}
	if resp.Name != "Jira" {
		t.Fatalf("expected default name Jira, got %q", resp.Name)
	}
	if got := resp.Config["api_token"]; got != "***" {
		t.Fatalf("expected masked api_token in response, got %q", got)
	}
}

func TestIntegrationCreateAcceptsCircleCIProviderAndMasksSecret(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "circleci",
		Mode:     "notify_only",
		Config: map[string]string{
			"api_token": "circleci-secret",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "circleci" {
		t.Fatalf("expected provider circleci, got %q", resp.Provider)
	}
	if resp.Name != "CircleCI" {
		t.Fatalf("expected default name CircleCI, got %q", resp.Name)
	}
	if got := resp.Config["api_token"]; got != "***" {
		t.Fatalf("expected masked api_token in response, got %q", got)
	}
}

func TestIntegrationCreateAcceptsAppSignalProviderAndMasksSecret(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "appsignal",
		Mode:     "notify_only",
		Config: map[string]string{
			"api_key": "mcp-secret",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "appsignal" {
		t.Fatalf("expected provider appsignal, got %q", resp.Provider)
	}
	if resp.Name != "AppSignal" {
		t.Fatalf("expected default name AppSignal, got %q", resp.Name)
	}
	if got := resp.Config["api_key"]; got != "***" {
		t.Fatalf("expected masked api_key in response, got %q", got)
	}
}

func TestIntegrationCreateAcceptsSentryProviderAndMasksSecret(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "sentry",
		Mode:     "notify_only",
		Config:   map[string]string{"api_token": "sntrys_secret", "organization": "acme"},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "sentry" || resp.Name != "Sentry" {
		t.Fatalf("unexpected provider/name: %q/%q", resp.Provider, resp.Name)
	}
	if got := resp.Config["api_token"]; got != "***" {
		t.Fatalf("expected masked api_token, got %q", got)
	}
	if got := resp.Config["organization"]; got != "acme" {
		t.Fatalf("expected organization preserved, got %q", got)
	}
}

func TestHandleTestIntegrationDraftSentryDoesNotRequireSavedIntegration(t *testing.T) {
	var gotAuth string
	sentry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/0/organizations/acme/projects/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer sentry.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	post := func(org string) (int, IntegrationTestResponse) {
		body, _ := json.Marshal(IntegrationRequest{
			Provider: "sentry", Mode: "notify_only",
			Config: map[string]string{"api_token": "tok", "organization": org, "base_url": sentry.URL},
		})
		rec := httptest.NewRecorder()
		server.handleTestIntegrationDraft(rec, httptest.NewRequest(http.MethodPost, "/integrations/test", bytes.NewReader(body)))
		var resp IntegrationTestResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	if code, resp := post("acme"); code != http.StatusOK || !resp.Success {
		t.Fatalf("expected success, got %d %+v", code, resp)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if code, resp := post("other"); code != http.StatusBadGateway || resp.Success {
		t.Fatalf("expected failure for wrong org, got %d %+v", code, resp)
	}
	if list, _ := store.ListIntegrations(); len(list) != 0 {
		t.Fatalf("draft test must not persist integrations, got %d", len(list))
	}
}

func TestHandleTestIntegrationDraftCloudflareReportsAccountAndDomains(t *testing.T) {
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
		case "/accounts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"acc1","name":"Me"}]}`))
		case "/zones":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"example.com"}],"result_info":{"total_count":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cf.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	post := func(token string) (int, IntegrationTestResponse) {
		body, _ := json.Marshal(IntegrationRequest{
			Provider: "cloudflare", Mode: "notify_only",
			Config: map[string]string{"api_token": token, "base_url": cf.URL},
		})
		rec := httptest.NewRecorder()
		server.handleTestIntegrationDraft(rec, httptest.NewRequest(http.MethodPost, "/integrations/test", bytes.NewReader(body)))
		var resp IntegrationTestResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	code, resp := post("good")
	if code != http.StatusOK || !resp.Success || !strings.Contains(resp.Message, "Me (ID acc1)") || !strings.Contains(resp.Message, "example.com") {
		t.Fatalf("expected success summary, got %d %+v", code, resp)
	}
	if code, resp := post("bad"); code != http.StatusBadGateway || resp.Success || !strings.Contains(resp.Message, "Invalid API Token") {
		t.Fatalf("expected failure for bad token, got %d %+v", code, resp)
	}
}
