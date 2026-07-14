package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/codehostreview"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// CodeReviewTool exposes the same provider-neutral review service used by Caesar.
// Adding GitHub later only requires another service adapter, not a second tool schema.
type CodeReviewTool struct {
	store  storage.Store
	client *http.Client
}

type CodeReviewParams struct {
	Operation       string `json:"operation"`
	Provider        string `json:"provider"`
	Owner           string `json:"owner"`
	Repository      string `json:"repository"`
	Branch          string `json:"branch,omitempty"`
	PullRequestID   string `json:"pull_request_id,omitempty"`
	CommentID       string `json:"comment_id,omitempty"`
	Body            string `json:"body,omitempty"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
}

func NewCodeReviewTool(store storage.Store) *CodeReviewTool {
	return &CodeReviewTool{store: store, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *CodeReviewTool) Name() string { return "code_review" }

func (t *CodeReviewTool) Description() string {
	return "Read pull request comments and reply to review threads using configured code-host integrations such as Bitbucket."
}

func (t *CodeReviewTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation":        map[string]interface{}{"type": "string", "enum": []string{"get_review", "list_comments", "reply"}, "description": "Review operation to perform"},
			"provider":         map[string]interface{}{"type": "string", "enum": []string{"bitbucket"}, "description": "Code hosting provider"},
			"owner":            map[string]interface{}{"type": "string", "description": "Workspace or repository owner"},
			"repository":       map[string]interface{}{"type": "string", "description": "Repository slug or name"},
			"branch":           map[string]interface{}{"type": "string", "description": "Source branch used to find an open pull request"},
			"pull_request_id":  map[string]interface{}{"type": "string", "description": "Pull request ID; optional when branch is provided"},
			"comment_id":       map[string]interface{}{"type": "string", "description": "Parent comment ID for reply"},
			"body":             map[string]interface{}{"type": "string", "description": "Reply body"},
			"integration_id":   map[string]interface{}{"type": "string", "description": "Specific integration ID (optional)"},
			"integration_name": map[string]interface{}{"type": "string", "description": "Specific integration name (optional)"},
		},
		"required": []string{"operation", "provider", "owner", "repository"},
	}
}

func (t *CodeReviewTool) Execute(ctx context.Context, raw json.RawMessage) (*tools.Result, error) {
	var params CodeReviewParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return &tools.Result{Success: false, Error: "Invalid parameters: " + err.Error()}, nil
	}
	repository := codehostreview.Repository{Provider: params.Provider, Owner: params.Owner, Name: params.Repository}
	service := codehostreview.NewService(t.store, t.client)
	var output interface{}
	var err error
	switch strings.ToLower(strings.TrimSpace(params.Operation)) {
	case "get_review", "list_comments":
		output, err = service.GetReview(ctx, codehostreview.GetReviewRequest{
			Repository: repository, Branch: params.Branch, PullRequestID: params.PullRequestID,
			IntegrationID: params.IntegrationID, IntegrationName: params.IntegrationName,
		})
	case "reply":
		output, err = service.Reply(ctx, codehostreview.ReplyRequest{
			Repository: repository, PullRequestID: params.PullRequestID, CommentID: params.CommentID, Body: params.Body,
			IntegrationID: params.IntegrationID, IntegrationName: params.IntegrationName,
		})
	default:
		err = fmt.Errorf("unsupported operation %q", params.Operation)
	}
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return &tools.Result{Success: true, Output: string(encoded)}, nil
}

var _ tools.Tool = (*CodeReviewTool)(nil)
