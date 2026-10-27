package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setJiraTestEnv(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", baseURL)
	t.Setenv("JIRA_EMAIL", "user@example.com")
	t.Setenv("JIRA_API_TOKEN", "secret-token")
}

func TestJiraCreateIssueBuildsExpectedRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody struct {
		Fields map[string]interface{} `json:"fields"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10042","key":"DEV-42","self":"https://example/rest/api/3/issue/10042"}`))
	}))
	defer server.Close()
	setJiraTestEnv(t, server.URL)

	params := `{"project_key":"DEV","summary":"Fix login redirect","description":"First line\nsecond line\n\nNew paragraph","labels":["auth"],"priority":"High","parent_key":"DEV-1","assignee_account_id":"acc-1"}`
	result, err := NewJiraCreateIssueTool(nil).Execute(context.Background(), json.RawMessage(params))
	if err != nil || !result.Success {
		t.Fatalf("create failed: %+v %v", result, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/rest/api/3/issue" {
		t.Fatalf("unexpected request %s %s", gotMethod, gotPath)
	}
	fields := gotBody.Fields
	if fields["summary"] != "Fix login redirect" {
		t.Fatalf("summary: %#v", fields)
	}
	for key, want := range map[string]string{"project": "DEV", "parent": "DEV-1"} {
		if got, _ := fields[key].(map[string]interface{}); got["key"] != want {
			t.Fatalf("%s: %#v", key, fields[key])
		}
	}
	for key, want := range map[string]string{"issuetype": "Task", "priority": "High"} {
		if got, _ := fields[key].(map[string]interface{}); got["name"] != want {
			t.Fatalf("%s: %#v", key, fields[key])
		}
	}
	if got, _ := fields["assignee"].(map[string]interface{}); got["accountId"] != "acc-1" {
		t.Fatalf("assignee: %#v", fields["assignee"])
	}
	if labels, _ := fields["labels"].([]interface{}); len(labels) != 1 || labels[0] != "auth" {
		t.Fatalf("labels: %#v", fields["labels"])
	}
	// Jira v3 only accepts Atlassian Document Format descriptions.
	description, _ := json.Marshal(fields["description"])
	if !strings.Contains(string(description), `"type":"doc"`) || strings.Count(string(description), `"type":"paragraph"`) != 2 || !strings.Contains(string(description), `"type":"hardBreak"`) {
		t.Fatalf("description is not ADF with 2 paragraphs and a line break: %s", description)
	}
	if !strings.Contains(result.Output, "DEV-42") || !strings.Contains(result.Output, server.URL+"/browse/DEV-42") {
		t.Fatalf("output should name the issue and its URL: %s", result.Output)
	}
}

func TestJiraCreateIssueOmitsUnsetOptionalFields(t *testing.T) {
	var gotBody struct {
		Fields map[string]interface{} `json:"fields"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"key":"DEV-43"}`))
	}))
	defer server.Close()
	setJiraTestEnv(t, server.URL)

	result, err := NewJiraCreateIssueTool(nil).Execute(context.Background(), json.RawMessage(`{"project_key":"DEV","summary":"Bug","issue_type":"Bug"}`))
	if err != nil || !result.Success {
		t.Fatalf("create failed: %+v %v", result, err)
	}
	// Projects without a field on their create screen reject it, even empty.
	for _, key := range []string{"description", "labels", "priority", "parent", "assignee"} {
		if _, ok := gotBody.Fields[key]; ok {
			t.Fatalf("unset %s sent: %#v", key, gotBody.Fields)
		}
	}
	if got, _ := gotBody.Fields["issuetype"].(map[string]interface{}); got["name"] != "Bug" {
		t.Fatalf("issue_type not applied: %#v", gotBody.Fields)
	}
}

func TestJiraCreateIssueValidatesBeforeCallingJira(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	setJiraTestEnv(t, server.URL)

	for _, params := range []string{`{"summary":"No project"}`, `{"project_key":"DEV"}`, `{"project_key":"DEV","summary":"   "}`} {
		result, err := NewJiraCreateIssueTool(nil).Execute(context.Background(), json.RawMessage(params))
		if err != nil || result.Success {
			t.Fatalf("expected validation failure for %s: %+v %v", params, result, err)
		}
	}
	if called {
		t.Fatal("Jira was called despite invalid parameters")
	}
}

func TestJiraCreateIssueSurfacesJiraFieldErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessages":[],"errors":{"issuetype":"Specify a valid issue type"}}`))
	}))
	defer server.Close()
	setJiraTestEnv(t, server.URL)

	result, err := NewJiraCreateIssueTool(nil).Execute(context.Background(), json.RawMessage(`{"project_key":"DEV","summary":"x","issue_type":"Nope"}`))
	if err != nil || result.Success || !strings.Contains(result.Error, "Specify a valid issue type") {
		t.Fatalf("Jira field error not surfaced: %+v %v", result, err)
	}
}

func TestJiraQueryGetIssueTypesUsesCreateMeta(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"issueTypes":[{"id":"1","name":"Task"}]}`))
	}))
	defer server.Close()
	setJiraTestEnv(t, server.URL)

	result, err := NewJiraQueryTool(nil).Execute(context.Background(), json.RawMessage(`{"operation":"get_issue_types","project_key":"DEV"}`))
	if err != nil || !result.Success {
		t.Fatalf("get_issue_types failed: %+v %v", result, err)
	}
	if gotPath != "/rest/api/3/issue/createmeta/DEV/issuetypes" {
		t.Fatalf("unexpected path %s", gotPath)
	}
}
