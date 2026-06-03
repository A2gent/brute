package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func (s *Server) handleProjectGitBranchChanges(w http.ResponseWriter, r *http.Request) {
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

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	branchChangesTarget := projectGitBranchChangesTarget(targetRepoRoot)
	response := ProjectGitBranchChangesResponse{
		RootFolder:    targetRepoRoot,
		CurrentBranch: branchChangesTarget.CurrentBranch,
		BaseBranch:    branchChangesTarget.BaseBranch,
		Available:     branchChangesTarget.Available,
		Files:         []ProjectGitCommitFile{},
	}
	if !branchChangesTarget.Available {
		s.jsonResponse(w, http.StatusOK, response)
		return
	}

	statusOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "diff", "--name-status", "--find-renames", branchChangesTarget.BaseRef+"...HEAD")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changed files: "+err.Error())
		return
	}
	statuses := parseProjectGitCommitFileStatuses(statusOutput)

	statsOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "diff", "--numstat", "--find-renames", branchChangesTarget.BaseRef+"...HEAD")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch file stats: "+err.Error())
		return
	}
	response.Files = mergeProjectGitCommitFiles(statuses, statsOutput)
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectGitBranchDiff(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	pathParam := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathParam == "" {
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

	branchChangesTarget := projectGitBranchChangesTarget(targetRepoRoot)
	if !branchChangesTarget.Available {
		s.errorResponse(w, http.StatusBadRequest, "Branch changes are available only on feature branches with a master or main branch")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, pathParam)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, err := runGitCommandPreserveLeading(targetRepoRoot, "diff", "--no-color", "--find-renames", branchChangesTarget.BaseRef+"...HEAD", "--", normalizedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch file diff: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitBranchDiffResponse{
		CurrentBranch: branchChangesTarget.CurrentBranch,
		BaseBranch:    branchChangesTarget.BaseBranch,
		Path:          normalizedPath,
		Preview:       preview,
	})
}

func (s *Server) handleProjectGitHistory(w http.ResponseWriter, r *http.Request) {
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

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	limit := 120
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsedLimit, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsedLimit <= 0 {
			s.errorResponse(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsedLimit > 500 {
			parsedLimit = 500
		}
		limit = parsedLimit
	}

	currentBranch := ""
	if value, branchErr := runGitCommand(targetRepoRoot, "rev-parse", "--abbrev-ref", "HEAD"); branchErr == nil {
		currentBranch = strings.TrimSpace(value)
	}

	branches, err := buildProjectGitBranches(targetRepoRoot, currentBranch)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git branches: "+err.Error())
		return
	}

	commitsOutput, err := runGitCommandPreserveLeading(
		targetRepoRoot,
		"log",
		"--decorate=short",
		"--date=iso-strict",
		"--pretty=format:%H%x1f%h%x1f%s%x1f%an%x1f%aI%x1f%D%x1f%P",
		"-n",
		strconv.Itoa(limit),
	)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git history: "+err.Error())
		return
	}

	commits := parseProjectGitHistoryCommits(commitsOutput, currentBranch)
	s.jsonResponse(w, http.StatusOK, ProjectGitHistoryResponse{
		RootFolder:    targetRepoRoot,
		CurrentBranch: currentBranch,
		Branches:      branches,
		Commits:       commits,
	})
}

func (s *Server) handleProjectGitCommitFiles(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	commitHash := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commitHash == "" {
		s.errorResponse(w, http.StatusBadRequest, "commit is required")
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

	statusOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "diff-tree", "--no-commit-id", "--name-status", "-r", "--root", commitHash)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit files: "+err.Error())
		return
	}
	statuses := parseProjectGitCommitFileStatuses(statusOutput)

	statsOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "show", "--numstat", "--format=", "--no-color", commitHash)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit file stats: "+err.Error())
		return
	}
	files := mergeProjectGitCommitFiles(statuses, statsOutput)

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitFilesResponse{
		Commit: commitHash,
		Files:  files,
	})
}

func (s *Server) handleProjectGitCommitDiff(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	commitHash := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commitHash == "" {
		s.errorResponse(w, http.StatusBadRequest, "commit is required")
		return
	}

	pathParam := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathParam == "" {
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

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, pathParam)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, err := runGitCommandPreserveLeading(targetRepoRoot, "show", "--no-color", "--pretty=format:", commitHash, "--", normalizedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit diff: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitDiffResponse{
		Commit:  commitHash,
		Path:    normalizedPath,
		Preview: preview,
	})
}

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
