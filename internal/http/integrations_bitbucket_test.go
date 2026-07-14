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
