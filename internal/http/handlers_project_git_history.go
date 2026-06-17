package http

import (
	"net/http"
	"strconv"
	"strings"
)

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
		s.jsonResponse(w, http.StatusOK, ProjectGitHistoryResponse{
			RootFolder:    targetRepoRoot,
			CurrentBranch: "",
			Branches:      []ProjectGitBranch{},
			Commits:       []ProjectGitHistoryCommit{},
		})
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
