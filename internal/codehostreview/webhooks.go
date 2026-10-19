package codehostreview

import (
	"context"
	"fmt"
	"net/http"
)

// CreateRepositoryWebhookRequest registers a repository webhook (e.g. pointing at a
// Square relay URL) so CI and PR events reach an event-triggered loop.
type CreateRepositoryWebhookRequest struct {
	Repository      Repository
	URL             string
	Secret          string
	Events          []string
	Description     string
	IntegrationID   string
	IntegrationName string
}

// CreateRepositoryWebhook creates a Bitbucket repository webhook. The token needs
// the webhook (read:webhook:bitbucket, write:webhook:bitbucket) scopes.
func (s *Service) CreateRepositoryWebhook(ctx context.Context, request CreateRepositoryWebhookRequest) (string, error) {
	repo, err := validateRepository(request.Repository)
	if err != nil {
		return "", err
	}
	if len(request.Events) == 0 || request.URL == "" {
		return "", fmt.Errorf("webhook url and events are required")
	}
	creds, err := s.resolveBitbucketCredentials(repo, request.IntegrationID, request.IntegrationName)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"description": request.Description,
		"url":         request.URL,
		"active":      true,
		"events":      request.Events,
	}
	if request.Secret != "" {
		payload["secret"] = request.Secret
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := s.bitbucketRequest(ctx, creds, http.MethodPost, bitbucketRepositoryPath(repo)+"/hooks", nil, payload, &created); err != nil {
		return "", err
	}
	return created.UUID, nil
}
