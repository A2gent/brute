package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

func (s *Server) handleProjectGitPush(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	output, err := runGitCommand(targetRepoRoot, "push")
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no upstream branch") || strings.Contains(lower, "has no upstream branch") {
			branchName, branchErr := runGitCommand(targetRepoRoot, "rev-parse", "--abbrev-ref", "HEAD")
			if branchErr != nil {
				s.errorResponse(w, http.StatusBadRequest, "Push failed (no upstream), and current branch could not be detected: "+branchErr.Error())
				return
			}
			branch := strings.TrimSpace(branchName)
			if branch == "" || branch == "HEAD" {
				s.errorResponse(w, http.StatusBadRequest, "Push failed: no upstream branch and current branch is detached")
				return
			}
			output, err = runGitCommand(targetRepoRoot, "push", "--set-upstream", "origin", branch)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Failed to push with upstream setup: "+err.Error())
				return
			}
			s.jsonResponse(w, http.StatusOK, ProjectGitPushResponse{Output: output})
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to push: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitPushResponse{Output: output})
}

func (s *Server) handleProjectGitPull(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitPullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	strategy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("strategy")))
	args := []string{"pull"}
	switch strategy {
	case "", "auto":
		// default git pull strategy (respect repo config)
	case "rebase":
		args = append(args, "--rebase")
	case "ff-only":
		args = append(args, "--ff-only")
	case "merge":
		args = append(args, "--no-rebase")
	default:
		s.errorResponse(w, http.StatusBadRequest, "Unsupported pull strategy")
		return
	}

	output, err := runGitCommand(targetRepoRoot, args...)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to pull: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitPullResponse{Output: output})
}

func (s *Server) handleProjectGitCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		s.errorResponse(w, http.StatusBadRequest, "branch is required")
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	args := []string{"checkout", branch}
	if req.Create {
		args = []string{"checkout", "-b", branch}
	}
	remotePrefix := "remotes/"
	if !req.Create && strings.HasPrefix(branch, remotePrefix) {
		args = []string{"checkout", "--track", strings.TrimPrefix(branch, remotePrefix)}
	} else if slashIndex := strings.Index(branch, "/"); slashIndex > 0 {
		remoteName := branch[:slashIndex]
		localName := branch[slashIndex+1:]
		if remoteName != "" && localName != "" {
			if _, err := runGitCommand(targetRepoRoot, "rev-parse", "--verify", "refs/remotes/"+branch); err == nil {
				if _, localErr := runGitCommand(targetRepoRoot, "rev-parse", "--verify", "refs/heads/"+localName); localErr != nil {
					args = []string{"checkout", "--track", branch}
				}
			}
		}
	}

	output, err := runGitCommand(targetRepoRoot, args...)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to checkout branch: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitPullResponse{Output: output})
}

func (s *Server) handleProjectGitInit(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusConflict, "Target folder is already a Git repository")
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "init"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to initialize git repository: "+err.Error())
		return
	}
	// Ensure first push can auto-establish upstream for new branches.
	if _, err := runGitCommand(targetRepoRoot, "config", "push.autoSetupRemote", "true"); err != nil {
		logging.Warn("Failed to set push.autoSetupRemote for %s: %v", targetRepoRoot, err)
	}

	remoteURL := strings.TrimSpace(req.RemoteURL)
	if remoteURL != "" {
		if _, err := runGitCommand(targetRepoRoot, "remote", "add", "origin", remoteURL); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Repository initialized, but failed to add remote origin: "+err.Error())
			return
		}
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitInitResponse{
		RootFolder: targetRepoRoot,
		HasGit:     true,
		RemoteURL:  remoteURL,
	})
}
