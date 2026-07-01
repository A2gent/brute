package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestHandleListIntegrationBackedSkills_IncludesTavilyPerplexityAndJiraTools(t *testing.T) {
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
