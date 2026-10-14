package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestSentryQueryToolListIssuesUsesBearerTokenAndOrg(t *testing.T) {
	var gotPath, gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"id":"1","title":"boom"}]`))
	}))
	defer server.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "sentry-1", Provider: "sentry", Name: "Sentry", Mode: "notify_only", Enabled: true,
		Config:    map[string]string{"api_token": "secret", "organization": "acme", "base_url": server.URL},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	result, err := NewSentryQueryTool(store).Execute(context.Background(), json.RawMessage(`{"operation":"list_issues","max_results":5}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if gotPath != "/api/0/organizations/acme/issues/" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if !strings.Contains(gotQuery, "limit=5") || !strings.Contains(gotQuery, "query=is%3Aunresolved") {
		t.Fatalf("unexpected query %q", gotQuery)
	}
	if !strings.Contains(result.Output, "boom") {
		t.Fatalf("expected issue in output, got %q", result.Output)
	}
}

func TestSentryQueryToolRequiresIssueIDAndRejectsUnknownOperation(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "sentry-1", Provider: "sentry", Name: "Sentry", Mode: "notify_only", Enabled: true,
		Config:    map[string]string{"api_token": "secret", "organization": "acme"},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}
	tool := NewSentryQueryTool(store)
	for _, in := range []string{`{"operation":"get_issue"}`, `{"operation":"resolve_issue"}`} {
		result, err := tool.Execute(context.Background(), json.RawMessage(in))
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure for %s", in)
		}
	}
}
