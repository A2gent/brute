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

func TestHandleUpdateIntegrationPreservesMaskedBitbucketToken(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "bitbucket", Provider: "bitbucket", Name: "Bitbucket", Mode: "duplex", Enabled: true,
		Config:    map[string]string{"email": "dev@example.com", "api_token": "secret-token", "workspace": "acme"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	body, _ := json.Marshal(IntegrationRequest{
		Provider: "bitbucket", Name: "Bitbucket", Mode: "duplex", Enabled: boolPointer(true),
		Config: map[string]string{"email": "dev@example.com", "api_token": "***", "workspace": "acme"},
	})
	req := httptest.NewRequest(http.MethodPut, "/integrations/bitbucket", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetIntegration("bitbucket")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Config["api_token"] != "secret-token" {
		t.Fatalf("expected stored token to be preserved, got %q", updated.Config["api_token"])
	}
}

func boolPointer(value bool) *bool { return &value }

func TestBitbucketIntegrationExposesPullRequestTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "bitbucket", Provider: "bitbucket", Name: "Bitbucket", Mode: "duplex", Enabled: true,
		Config: map[string]string{"email": "dev@example.com", "api_token": "secret"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	rec := httptest.NewRecorder()
	server.handleListIntegrationBackedSkills(rec, httptest.NewRequest(http.MethodGet, "/skills/integration-backed", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response IntegrationBackedSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Skills) != 1 || len(response.Skills[0].Tools) != 2 {
		t.Fatalf("unexpected integration tools: %#v", response.Skills)
	}
	found := map[string]bool{}
	for _, tool := range response.Skills[0].Tools {
		found[tool.Name] = true
		if tool.Name == "bitbucket_create_pull_request" && !strings.Contains(tool.Description, "write:pullrequest") {
			t.Fatalf("missing token scope hint: %s", tool.Description)
		}
	}
	if !found["code_review"] || !found["bitbucket_create_pull_request"] {
		t.Fatalf("missing Bitbucket tools: %v", found)
	}
}
