package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestJiraQueryToolUsesEnvCredentialsForReadOnlyMyself(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotUser string
	var gotPass string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"abc","displayName":"Test User"}`))
	}))
	defer server.Close()

	t.Setenv("JIRA_BASE_URL", server.URL)
	t.Setenv("JIRA_EMAIL", "user@example.com")
	t.Setenv("JIRA_API_TOKEN", "secret-token")

	tool := NewJiraQueryTool(nil)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"myself"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET request, got %s", gotMethod)
	}
	if gotPath != "/rest/api/3/myself" {
		t.Fatalf("expected /rest/api/3/myself path, got %s", gotPath)
	}
	if gotUser != "user@example.com" || gotPass != "secret-token" {
		t.Fatalf("unexpected basic auth user/pass: %q/%q", gotUser, gotPass)
	}
	if !strings.Contains(result.Output, "Test User") {
		t.Fatalf("expected formatted Jira response in output, got %s", result.Output)
	}
}

func TestJiraQueryToolSearchIssuesBuildsExpectedRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotContentType string
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[],"maxResults":5}`))
	}))
	defer server.Close()

	tool := NewJiraQueryTool(nil)
	t.Setenv("JIRA_BASE_URL", server.URL)
	t.Setenv("JIRA_EMAIL", "user@example.com")
	t.Setenv("JIRA_API_TOKEN", "secret-token")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"search_issues","jql":"project = ABC","max_results":5,"fields":"summary,status","next_page_token":"abc123"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST request, got %s", gotMethod)
	}
	if gotPath != "/rest/api/3/search/jql" {
		t.Fatalf("expected /rest/api/3/search/jql path, got %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("expected JSON content type, got %s", gotContentType)
	}
	if gotBody["jql"] != "project = ABC" {
		t.Fatalf("expected jql in body, got %#v", gotBody)
	}
	if gotBody["maxResults"] != float64(5) {
		t.Fatalf("expected maxResults=5 in body, got %#v", gotBody)
	}
	if gotBody["nextPageToken"] != "abc123" {
		t.Fatalf("expected nextPageToken in body, got %#v", gotBody)
	}
	fields, ok := gotBody["fields"].([]interface{})
	if !ok || len(fields) != 2 || fields[0] != "summary" || fields[1] != "status" {
		t.Fatalf("expected fields array in body, got %#v", gotBody)
	}
}

func TestJiraCredentialsFromIntegrationRequiresFields(t *testing.T) {
	_, err := credentialsFromIntegration(&storage.Integration{
		Provider: "jira",
		Name:     "Jira",
		Config: map[string]string{
			"base_url": "https://example.atlassian.net",
			"email":    "user@example.com",
		},
	})
	if err == nil {
		t.Fatalf("expected missing api_token error")
	}
	if !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
