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

func TestCircleCIQueryToolProjectPipelinesUsesTokenHeader(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotToken string
	var gotQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("Circle-Token")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"pipe-1","number":42,"state":"created"}]}`))
	}))
	defer server.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "circleci-1",
		Provider:  "circleci",
		Name:      "CircleCI",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"api_token": "secret-token", "api_base_url": server.URL},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	tool := NewCircleCIQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"project_pipelines","project_slug":"gh/org/repo","branch":"main","max_results":5}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET request, got %s", gotMethod)
	}
	if gotPath != "/api/v2/project/gh/org/repo/pipeline" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if gotToken != "secret-token" {
		t.Fatalf("expected Circle-Token header, got %q", gotToken)
	}
	if !strings.Contains(gotQuery, "branch=main") || !strings.Contains(gotQuery, "max_results=5") {
		t.Fatalf("expected branch and max_results query params, got %q", gotQuery)
	}
	if !strings.Contains(result.Output, "pipe-1") {
		t.Fatalf("expected CircleCI response in output, got %s", result.Output)
	}
}

func TestCircleCIRecentFailedJobsAggregatesPipelinesWorkflowsAndJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/project/gh/org/repo/pipeline":
			_, _ = w.Write([]byte(`{"items":[{"id":"pipe-1","number":1}]}`))
		case "/api/v2/pipeline/pipe-1/workflow":
			_, _ = w.Write([]byte(`{"items":[{"id":"wf-1","name":"deploy","status":"failed"}]}`))
		case "/api/v2/workflow/wf-1/job":
			_, _ = w.Write([]byte(`{"items":[{"name":"deploy-prod","job_number":99,"status":"failed","type":"build"},{"name":"hold","status":"success","type":"approval"}]}`))
		default:
			t.Fatalf("unexpected CircleCI path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "circleci-1",
		Provider:  "circleci",
		Name:      "CircleCI",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"api_token": "secret-token", "api_base_url": server.URL},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	tool := NewCircleCIQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"recent_failed_jobs","project_slug":"gh/org/repo"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if !strings.Contains(result.Output, "deploy-prod") || !strings.Contains(result.Output, "job_number") {
		t.Fatalf("expected failed job details in output, got %s", result.Output)
	}
}

func TestCircleCIQueryToolRequiresProjectSlugForProjectOperations(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "circleci-1",
		Provider:  "circleci",
		Name:      "CircleCI",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"api_token": "secret-token"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	tool := NewCircleCIQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"project_pipelines"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "project_slug is required") {
		t.Fatalf("expected project_slug validation error, got %#v", result)
	}
}
