package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

func (s *Server) handleProjectGitStatus(w http.ResponseWriter, r *http.Request) {
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

	repoPath := strings.TrimSpace(r.URL.Query().Get("repoPath"))
	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if !projectHasGitMetadata(targetRepoRoot) {
		s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
			RootFolder: targetRepoRoot,
			HasGit:     false,
			Files:      []ProjectGitChangedFile{},
		})
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}

	currentBranch, baseBranch, branchChangesAvailable := projectGitBranchChangesAvailability(targetRepoRoot)
	s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
		RootFolder:              targetRepoRoot,
		HasGit:                  true,
		CurrentBranch:           currentBranch,
		BranchChangesAvailable:  branchChangesAvailable,
		BranchChangesBaseBranch: baseBranch,
		Files:                   parseGitPorcelain(porcelainOutput),
	})
}

func (s *Server) handleProjectGitCommit(w http.ResponseWriter, r *http.Request) {
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

	var req ProjectGitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	message := strings.TrimSpace(req.Message)
	if message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Commit message is required")
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}
	changedFiles := parseGitPorcelain(porcelainOutput)
	if len(changedFiles) == 0 {
		s.errorResponse(w, http.StatusConflict, "No changed files")
		return
	}

	stagedOutput, err := runGitCommand(targetRepoRoot, "diff", "--cached", "--name-only")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to inspect staged files: "+err.Error())
		return
	}
	stagedFiles := splitNonEmptyLines(stagedOutput)
	if len(stagedFiles) == 0 {
		s.errorResponse(w, http.StatusConflict, "No staged files to commit")
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "commit", "-m", message); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "nothing to commit") || strings.Contains(lower, "no changes added to commit") {
			s.errorResponse(w, http.StatusConflict, "No staged files to commit")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to create commit: "+err.Error())
		return
	}

	commit, err := runGitCommand(targetRepoRoot, "rev-parse", "--short", "HEAD")
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Commit created, but failed to read commit hash: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitResponse{
		RootFolder:     targetRepoRoot,
		Commit:         strings.TrimSpace(commit),
		FilesCommitted: len(stagedFiles),
	})
}

func (s *Server) handleProjectGitStageFile(w http.ResponseWriter, r *http.Request) {
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

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "add", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitStageAllFiles(w http.ResponseWriter, r *http.Request) {
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

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// Stage all tracked/untracked changes, including deletions.
	if _, err := runGitCommand(targetRepoRoot, "add", "--all"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stage all files: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitUnstageFile(w http.ResponseWriter, r *http.Request) {
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

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "restore", "--staged", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to unstage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitDiscardFile(w http.ResponseWriter, r *http.Request) {
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

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	statusOutput, statusErr := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1", "--", normalizedPath)
	if statusErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read file git status: "+statusErr.Error())
		return
	}
	statusFiles := parseGitPorcelain(statusOutput)
	var fileStatus *ProjectGitChangedFile
	for i := range statusFiles {
		if filepath.ToSlash(strings.TrimSpace(statusFiles[i].Path)) == filepath.ToSlash(normalizedPath) {
			fileStatus = &statusFiles[i]
			break
		}
	}
	if fileStatus == nil {
		s.errorResponse(w, http.StatusConflict, "File has no changes to discard")
		return
	}

	fullPath := filepath.Join(targetRepoRoot, filepath.FromSlash(normalizedPath))
	if fileStatus.Untracked || fileStatus.IndexStatus == "A" {
		if _, rmErr := runGitCommand(targetRepoRoot, "rm", "--cached", "--ignore-unmatch", "--", normalizedPath); rmErr != nil {
			logging.Warn("git rm --cached ignore-unmatch failed for discard path %s: %v", normalizedPath, rmErr)
		}
		if removeErr := os.Remove(fullPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Failed to discard file: "+removeErr.Error())
			return
		}
		s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	if _, restoreErr := runGitCommand(targetRepoRoot, "restore", "--staged", "--worktree", "--", normalizedPath); restoreErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discard file changes: "+restoreErr.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitFileDiff(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "path is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, err := buildGitFileDiffPreview(targetRepoRoot, normalizedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to load file diff: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitFileDiffResponse{
		Path:    normalizedPath,
		Preview: preview,
	})
}

func splitNonEmptyLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
