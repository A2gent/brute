package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/codehostreview"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestBitbucketCreatePullRequestToolResolvesRepository(t *testing.T) {
	for _, test := range []struct {
		name, remote, owner, repository, wantPath string
	}{
		{"ssh+git remote", "ssh+git://user@bitbucket.org/acme/widgets.git", "", "", "/2.0/repositories/acme/widgets/pullrequests"},
		{"https remote", "https://bitbucket.org/acme/widgets.git", "", "", "/2.0/repositories/acme/widgets/pullrequests"},
		{"scp remote", "git@bitbucket.org:acme/widgets.git", "", "", "/2.0/repositories/acme/widgets/pullrequests"},
		{"owner override", "ssh+git://user@bitbucket.org/acme/widgets.git", "other", "", "/2.0/repositories/other/widgets/pullrequests"},
		{"repository override", "ssh+git://user@bitbucket.org/acme/widgets.git", "", "gadgets", "/2.0/repositories/acme/gadgets/pullrequests"},
		{"explicit repository without git", "", "other", "gadgets", "/2.0/repositories/other/gadgets/pullrequests"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != test.wantPath {
					t.Errorf("unexpected request %s %s, want GET %s", r.Method, r.URL, test.wantPath)
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"values":[{"id":42,"state":"OPEN","source":{"branch":{"name":"feature/create"}},"links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/42"}},"reviewers":[{"display_name":"Alice"}]}]}`))
			}))
			defer server.Close()
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now()
			if err := store.SaveIntegration(&storage.Integration{
				ID: "bb", Provider: "bitbucket", Name: "BB", Mode: "duplex", Enabled: true,
				Config: map[string]string{"email": "dev@example.com", "api_token": "secret", "api_base_url": server.URL}, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			workDir := t.TempDir()
			if test.remote != "" {
				for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", test.remote}} {
					cmd := exec.Command("git", args...)
					cmd.Dir = workDir
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git %v: %v: %s", args, err, output)
					}
				}
			}
			manager := tools.NewManager(workDir)
			Register(manager, store, nil, nil)
			tool, ok := manager.Get("bitbucket_create_pull_request")
			if !ok {
				t.Fatal("Bitbucket PR creation tool is not registered")
			}
			raw, _ := json.Marshal(BitbucketCreatePullRequestParams{SourceBranch: "feature/create", Title: "Create natively", Owner: test.owner, Repository: test.repository})
			result, err := tool.Execute(context.Background(), raw)
			if err != nil || !result.Success {
				t.Fatalf("Execute: result=%#v err=%v", result, err)
			}
			var pr codehostreview.PullRequest
			if err := json.Unmarshal([]byte(result.Output), &pr); err != nil {
				t.Fatal(err)
			}
			if pr.ID != "42" || pr.URL == "" || !reflect.DeepEqual(pr.Reviewers, []string{"Alice"}) {
				t.Fatalf("unexpected output: %s", result.Output)
			}
		})
	}
}

func TestBitbucketCreatePullRequestToolParameters(t *testing.T) {
	tool := NewBitbucketCreatePullRequestTool(nil, t.TempDir())
	if got := tool.Schema()["required"]; !reflect.DeepEqual(got, []string{"source_branch", "title"}) {
		t.Fatalf("unexpected required parameters: %v", got)
	}
	if tools.ToolGroupOf(tool.Name()) != "git_review" {
		t.Fatal("PR creation must be in the git_review tool group")
	}
	for _, test := range []struct{ input, errorText string }{
		{`{`, "Invalid parameters"},
		{`{"title":"Missing branch"}`, "source_branch and title are required"},
		{`{"source_branch":"feature/create","title":"  "}`, "source_branch and title are required"},
		{`{"source_branch":"feature/create","title":"Missing remote"}`, "failed to resolve origin remote"},
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(test.input))
		if err != nil || result.Success || !strings.Contains(result.Error, test.errorText) {
			t.Fatalf("input=%s result=%#v err=%v", test.input, result, err)
		}
	}
}
