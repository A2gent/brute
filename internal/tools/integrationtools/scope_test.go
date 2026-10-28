package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestFilterIntegrations(t *testing.T) {
	all := []*storage.Integration{
		{ID: "jira-global", Provider: "jira"},
		{ID: "jira-a", Provider: "jira", ProjectID: "a"},
		{ID: "jira-b", Provider: "jira", ProjectID: "b"},
		{ID: "sentry-global", Provider: "sentry"},
		{ID: "sentry-b", Provider: "sentry", ProjectID: "b"},
	}
	cases := []struct {
		name  string
		scope Scope
		want  []string
	}{
		{"no project sees only global", Scope{}, []string{"jira-global", "sentry-global"}},
		{"project shadows global of same provider", Scope{ProjectID: "a"}, []string{"jira-a", "sentry-global"}},
		{"other project never visible", Scope{ProjectID: "b"}, []string{"jira-b", "sentry-b"}},
		{"project without own integrations keeps global", Scope{ProjectID: "c"}, []string{"jira-global", "sentry-global"}},
		{"project only drops global", Scope{ProjectID: "a", ProjectOnly: true}, []string{"jira-a"}},
		{"project only without project sees nothing", Scope{ProjectOnly: true}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, item := range FilterIntegrations(all, tc.scope) {
				got = append(got, item.ID)
			}
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJiraQueryUsesScopedProjectIntegration(t *testing.T) {
	var gotUser string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _, _ = r.BasicAuth()
		_, _ = w.Write([]byte(`{"accountId":"abc"}`))
	}))
	defer server.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Now()
	for _, item := range []*storage.Integration{
		{ID: "global", Provider: "jira", Name: "Global", Enabled: true, Config: map[string]string{"base_url": server.URL, "email": "global@example.com", "api_token": "t"}, CreatedAt: now, UpdatedAt: now},
		{ID: "proj", Provider: "jira", Name: "Project", Enabled: true, ProjectID: "p1", Config: map[string]string{"base_url": server.URL, "email": "project@example.com", "api_token": "t"}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.SaveIntegration(item); err != nil {
			t.Fatalf("SaveIntegration: %v", err)
		}
	}

	tool := NewJiraQueryTool(store)
	ctx := WithScope(context.Background(), Scope{ProjectID: "p1"})
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"myself"}`))
	if err != nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	if gotUser != "project@example.com" {
		t.Fatalf("expected project integration credentials, got %q", gotUser)
	}

	// Another project's integration must not be reachable by explicit id.
	other := WithScope(context.Background(), Scope{ProjectID: "p2"})
	result, err = tool.Execute(other, json.RawMessage(`{"operation":"myself","integration_id":"proj"}`))
	if err == nil && result.Success {
		t.Fatalf("expected out-of-scope integration_id to fail")
	}
	if msg := result.Error; err == nil && !strings.Contains(msg, "not found") {
		t.Fatalf("expected not found error, got %q", msg)
	}
}
