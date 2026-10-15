package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/codehostreview"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type BitbucketCreatePullRequestTool struct {
	store   storage.Store
	client  *http.Client
	workDir string
}

type BitbucketCreatePullRequestParams struct {
	SourceBranch      string `json:"source_branch"`
	Title             string `json:"title"`
	Description       string `json:"description,omitempty"`
	DestinationBranch string `json:"destination_branch,omitempty"`
	Owner             string `json:"owner,omitempty"`
	Repository        string `json:"repository,omitempty"`
	IntegrationID     string `json:"integration_id,omitempty"`
	IntegrationName   string `json:"integration_name,omitempty"`
}

func NewBitbucketCreatePullRequestTool(store storage.Store, workDir string) *BitbucketCreatePullRequestTool {
	return &BitbucketCreatePullRequestTool{store: store, workDir: workDir, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *BitbucketCreatePullRequestTool) Name() string { return "bitbucket_create_pull_request" }

func (t *BitbucketCreatePullRequestTool) Description() string {
	return "Create a Bitbucket pull request, or return the existing open PR for the source branch. Defaults to the session project's origin remote and the repository's main branch. Applies default reviewers except the author and closes the source branch on merge. The integration token needs write:pullrequest scope."
}

func (t *BitbucketCreatePullRequestTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source_branch":      map[string]interface{}{"type": "string", "description": "Source branch (must already be pushed)"},
			"title":              map[string]interface{}{"type": "string", "description": "Pull request title"},
			"description":        map[string]interface{}{"type": "string", "description": "Pull request description (Markdown)"},
			"destination_branch": map[string]interface{}{"type": "string", "description": "Target branch; defaults to the repository main branch"},
			"owner":              map[string]interface{}{"type": "string", "description": "Workspace; defaults to the session project's origin remote"},
			"repository":         map[string]interface{}{"type": "string", "description": "Repository slug; defaults to the session project's origin remote"},
			"integration_id":     map[string]interface{}{"type": "string", "description": "Specific Bitbucket integration ID (optional)"},
			"integration_name":   map[string]interface{}{"type": "string", "description": "Specific Bitbucket integration name (optional)"},
		},
		"required": []string{"source_branch", "title"},
	}
}

func (t *BitbucketCreatePullRequestTool) Execute(ctx context.Context, raw json.RawMessage) (*tools.Result, error) {
	var params BitbucketCreatePullRequestParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return &tools.Result{Success: false, Error: "Invalid parameters: " + err.Error()}, nil
	}
	if strings.TrimSpace(params.SourceBranch) == "" || strings.TrimSpace(params.Title) == "" {
		return &tools.Result{Success: false, Error: "source_branch and title are required"}, nil
	}
	repository := codehostreview.Repository{Provider: "bitbucket", Owner: strings.TrimSpace(params.Owner), Name: strings.TrimSpace(params.Repository)}
	if repository.Owner == "" || repository.Name == "" {
		cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
		cmd.Dir = t.workDir
		remote, err := cmd.Output()
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to resolve origin remote: %v; pass owner and repository explicitly", err)}, nil
		}
		inferred, err := codehostreview.ParseRepositoryRemote(string(remote))
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		if repository.Owner == "" {
			repository.Owner = inferred.Owner
		}
		if repository.Name == "" {
			repository.Name = inferred.Name
		}
	}

	pullRequest, err := codehostreview.NewService(t.store, t.client).CreatePullRequest(ctx, codehostreview.CreatePullRequestRequest{
		Repository: repository, SourceBranch: params.SourceBranch, Title: params.Title, Description: params.Description,
		DestinationBranch: params.DestinationBranch, IntegrationID: params.IntegrationID, IntegrationName: params.IntegrationName,
	})
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	encoded, err := json.MarshalIndent(pullRequest, "", "  ")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: string(encoded)}, nil
}
