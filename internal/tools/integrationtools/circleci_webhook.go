package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/A2gent/brute/internal/storage"
)

// CreateCircleCIWorkflowWebhook registers a project webhook for workflow-completed events.
// It is not exposed through CircleCIQueryTool, which stays read-only for agents.
func CreateCircleCIWorkflowWebhook(ctx context.Context, store storage.Store, projectSlug, name, webhookURL, signingSecret string) (string, error) {
	t := NewCircleCIQueryTool(store)
	creds, err := t.resolveCredentials(ctx, "", "")
	if err != nil {
		return "", err
	}
	slug, err := requireCircleCIProjectSlug(projectSlug)
	if err != nil {
		return "", err
	}
	raw, err := t.circleCIGetBytes(ctx, creds, "/api/v2/project/"+escapeCircleCIProjectSlug(slug), nil)
	if err != nil {
		return "", err
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &project); err != nil || project.ID == "" {
		return "", fmt.Errorf("CircleCI project %s has no id", slug)
	}
	raw, err = t.circleCIDo(ctx, creds, http.MethodPost, "/api/v2/webhook", nil, map[string]any{
		"name": name, "events": []string{"workflow-completed"}, "url": webhookURL,
		"verify-tls": true, "signing-secret": signingSecret,
		"scope": map[string]string{"type": "project", "id": project.ID},
	})
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("failed to decode CircleCI webhook response: %w", err)
	}
	return created.ID, nil
}
