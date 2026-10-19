// hook_webhook_connect.go provisions public webhook URLs for event-triggered loops.
// GitHub/Bitbucket cannot reach a local Brute, so the URL lives on Square (a2gent.net)
// and events are relayed back over the existing A2A tunnel.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/codehostreview"
	"github.com/A2gent/brute/internal/hooks"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

var squareHTTPClient = &http.Client{Timeout: 20 * time.Second}

func (s *Server) squareRequest(ctx context.Context, method, path string, payload interface{}, target interface{}) error {
	settings, err := s.store.GetSettings()
	if err != nil {
		return err
	}
	registryURL, apiKey, err := s.resolveA2ARegistryConnection(settings)
	if err != nil {
		return fmt.Errorf("connect this agent to a2gent.net first: %w", err)
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, registryURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := squareHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("square request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("square returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if target != nil {
		return json.Unmarshal(raw, target)
	}
	return nil
}

func (s *Server) revokeSquareWebhookEndpoint(ctx context.Context, endpointID string) error {
	err := s.squareRequest(ctx, http.MethodDelete, "/agents/me/webhooks/"+endpointID, nil, nil)
	if err != nil && strings.Contains(err.Error(), "returned 404") {
		return nil
	}
	return err
}

// handleConnectJobWebhook creates (or re-creates) the Square endpoint for a hook.
func (s *Server) handleConnectJobWebhook(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(chi.URLParam(r, "jobID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}
	previousEndpointID := job.WebhookEndpointID
	var created struct {
		Endpoint struct {
			ID string `json:"id"`
		} `json:"endpoint"`
		URLWithSecret  string `json:"url_with_secret"`
		ApprovalStatus string `json:"approval_status"`
	}
	if err := s.squareRequest(r.Context(), http.MethodPost, "/agents/me/webhooks", map[string]string{"name": "Hook: " + job.Name}, &created); err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	if job.WebhookSigningSecret == "" {
		if job.WebhookSigningSecret, err = newHookSigningSecret(); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	job.TriggerType = jobTriggerWebhook
	job.NextRunAt = nil
	job.WebhookEndpointID = created.Endpoint.ID
	job.WebhookURL = created.URLWithSecret
	job.UpdatedAt = time.Now()
	if err := s.saveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save job: "+err.Error())
		return
	}
	// Revoke the old URL only after the new one is saved, so a failed rotation keeps the hook working.
	if previousEndpointID != "" {
		if err := s.revokeSquareWebhookEndpoint(r.Context(), previousEndpointID); err != nil {
			logging.Warn("Failed to revoke previous Square webhook endpoint %s: %v", previousEndpointID, err)
		}
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"job": s.jobToResponse(job), "approval_status": created.ApprovalStatus})
}

func (s *Server) handleDisconnectJobWebhook(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(chi.URLParam(r, "jobID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}
	if job.WebhookEndpointID != "" {
		if err := s.revokeSquareWebhookEndpoint(r.Context(), job.WebhookEndpointID); err != nil {
			s.errorResponse(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	job.WebhookEndpointID, job.WebhookURL = "", ""
	job.UpdatedAt = time.Now()
	if err := s.saveJob(job); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save job: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.jobToResponse(job))
}

var githubRemotePattern = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// detectRepository maps the project's origin remote to a code host repository so the
// UI can prefill Bitbucket registration or deep-link to GitHub's "Add webhook" page.
func detectRepository(remote string) *codehostreview.Repository {
	remote = strings.TrimSpace(remote)
	if m := githubRemotePattern.FindStringSubmatch(remote); m != nil {
		return &codehostreview.Repository{Provider: hooks.SourceGitHub, Owner: m[1], Name: m[2]}
	}
	if repo, err := codehostreview.ParseRepositoryRemote(remote); err == nil {
		return &repo
	}
	return nil
}

func (s *Server) jobRepository(job *storage.RecurringJob) *codehostreview.Repository {
	if job.ProjectID == nil {
		return nil
	}
	root, err := s.resolveProjectRootFolder(*job.ProjectID)
	if err != nil {
		return nil
	}
	remote, err := runGitCommand(root, "remote", "get-url", "origin")
	if err != nil {
		return nil
	}
	return detectRepository(remote)
}

func (s *Server) handleGetJobWebhookRepository(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(chi.URLParam(r, "jobID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"repository": s.jobRepository(job)})
}

// bitbucketWebhookEvents maps normalized trigger events to Bitbucket event keys.
func bitbucketWebhookEvents(triggerEvents string) []string {
	mapping := map[string][]string{
		hooks.TypeCIFailed:    {"repo:commit_status_updated", "repo:commit_status_created"},
		hooks.TypeCISucceeded: {"repo:commit_status_updated", "repo:commit_status_created"},
		hooks.TypeCIStarted:   {"repo:commit_status_created"},
		hooks.TypePush:        {"repo:push"},
		hooks.TypePROpened:    {"pullrequest:created"},
		hooks.TypePRUpdated:   {"pullrequest:updated"},
		hooks.TypePRMerged:    {"pullrequest:fulfilled"},
		hooks.TypePRComment:   {"pullrequest:comment_created"},
		hooks.TypeIssueOpened: {"issue:created"},
	}
	requested := hooks.SplitList(triggerEvents)
	if len(requested) == 0 {
		requested = []string{hooks.TypeCIFailed, hooks.TypePush, hooks.TypePROpened, hooks.TypePRComment}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range requested {
		keys, ok := mapping[e]
		if !ok && strings.Contains(e, ":") {
			keys = []string{e} // already a raw Bitbucket event key
		}
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func (s *Server) handleRegisterJobBitbucketWebhook(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(chi.URLParam(r, "jobID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}
	if job.WebhookURL == "" {
		s.errorResponse(w, http.StatusBadRequest, "Connect the hook to a2gent.net first")
		return
	}
	var req struct {
		Repository string `json:"repository"` // "workspace/repo"; defaults to the project origin
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	repo := s.jobRepository(job)
	if owner, name, ok := strings.Cut(strings.TrimSpace(req.Repository), "/"); ok {
		repo = &codehostreview.Repository{Provider: hooks.SourceBitbucket, Owner: owner, Name: name}
	}
	if repo == nil || repo.Provider != hooks.SourceBitbucket {
		s.errorResponse(w, http.StatusBadRequest, "Bitbucket repository is required (workspace/repo)")
		return
	}
	uuid, err := codehostreview.NewService(s.store, nil).CreateRepositoryWebhook(r.Context(), codehostreview.CreateRepositoryWebhookRequest{
		Repository: *repo, URL: job.WebhookURL, Secret: job.WebhookSigningSecret,
		Events: bitbucketWebhookEvents(job.TriggerEvents), Description: "A2gent hook: " + job.Name,
	})
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusCreated, map[string]string{"uuid": uuid, "repository": repo.Owner + "/" + repo.Name})
}
