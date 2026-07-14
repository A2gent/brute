package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/codehostreview"
)

type ProjectCodeReviewReplyRequest struct {
	RepoPath        string `json:"repo_path,omitempty"`
	PullRequestID   string `json:"pull_request_id"`
	CommentID       string `json:"comment_id"`
	Body            string `json:"body"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
}

func (s *Server) handleGetProjectCodeReview(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	repo, branch, err := s.resolveProjectCodeReviewTarget(projectID, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	service := codehostreview.NewService(s.store, nil)
	review, err := service.GetReview(r.Context(), codehostreview.GetReviewRequest{
		Repository: repo, Branch: branch,
		PullRequestID: strings.TrimSpace(r.URL.Query().Get("pullRequestID")),
		IntegrationID: strings.TrimSpace(r.URL.Query().Get("integrationID")),
	})
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, review)
}

func (s *Server) handleReplyProjectCodeReview(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	var req ProjectCodeReviewReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	repo, _, err := s.resolveProjectCodeReviewTarget(projectID, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	service := codehostreview.NewService(s.store, nil)
	comment, err := service.Reply(r.Context(), codehostreview.ReplyRequest{
		Repository: repo, PullRequestID: req.PullRequestID, CommentID: req.CommentID, Body: req.Body,
		IntegrationID: req.IntegrationID, IntegrationName: req.IntegrationName,
	})
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusCreated, comment)
}

func (s *Server) resolveProjectCodeReviewTarget(projectID, repoPath string) (codehostreview.Repository, string, error) {
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		return codehostreview.Repository{}, "", err
	}
	targetRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPath)
	if err != nil {
		return codehostreview.Repository{}, "", err
	}
	remote, err := runGitCommand(targetRoot, "remote", "get-url", "origin")
	if err != nil {
		return codehostreview.Repository{}, "", errors.New("failed to resolve origin remote: " + err.Error())
	}
	repository, err := codehostreview.ParseRepositoryRemote(remote)
	if err != nil {
		return codehostreview.Repository{}, "", err
	}
	branch, err := runGitCommand(targetRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return codehostreview.Repository{}, "", errors.New("failed to resolve current branch: " + err.Error())
	}
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" {
		branch = inferProjectGitDetachedBranch(targetRoot)
	}
	return repository, branch, nil
}
