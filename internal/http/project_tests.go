package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const (
	projectTestFrameworkRSpec  = "rspec"
	projectTestFrameworkGo     = "go"
	projectTestFrameworkJest   = "jest"
	projectTestFrameworkVitest = "vitest"
)

type ProjectTestFrameworkInfo struct {
	ID                 string `json:"id"`
	Label              string `json:"label"`
	Available          bool   `json:"available"`
	Reason             string `json:"reason,omitempty"`
	TestCount          int    `json:"test_count"`
	BranchTestCount    int    `json:"branch_test_count"`
	SupportsCoverage   bool   `json:"supports_coverage"`
	CoverageMode       string `json:"coverage_mode,omitempty"`
	SupportsIndividual bool   `json:"supports_individual"`
}

type ProjectTestNode struct {
	ID          string            `json:"id"`
	Framework   string            `json:"framework"`
	Name        string            `json:"name"`
	FullName    string            `json:"full_name"`
	Path        string            `json:"path"`
	Line        int               `json:"line,omitempty"`
	Type        string            `json:"type"`
	BranchAdded bool              `json:"branch_added,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Children    []ProjectTestNode `json:"children,omitempty"`
}

type ProjectTestFile struct {
	ID           string            `json:"id"`
	Framework    string            `json:"framework"`
	Path         string            `json:"path"`
	Package      string            `json:"package,omitempty"`
	BranchStatus string            `json:"branch_status,omitempty"`
	BranchScope  string            `json:"branch_scope,omitempty"`
	Tests        []ProjectTestNode `json:"tests"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type ProjectTestsDiscoveryResponse struct {
	RootFolder             string                     `json:"root_folder"`
	RepoPath               string                     `json:"repo_path"`
	CurrentBranch          string                     `json:"current_branch,omitempty"`
	BaseBranch             string                     `json:"base_branch,omitempty"`
	BranchChangesAvailable bool                       `json:"branch_changes_available"`
	Frameworks             []ProjectTestFrameworkInfo `json:"frameworks"`
	TestFiles              []ProjectTestFile          `json:"test_files"`
	BranchTestFiles        []ProjectTestFile          `json:"branch_test_files"`
}

type ProjectTestsRunRequest struct {
	RepoPath  string `json:"repo_path,omitempty"`
	Framework string `json:"framework,omitempty"`
	Mode      string `json:"mode,omitempty"`
	TestID    string `json:"test_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Name      string `json:"name,omitempty"`
}

type ProjectTestRunCommand struct {
	Framework  string   `json:"framework"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Display    string   `json:"display"`
	ExitCode   int      `json:"exit_code"`
	DurationMs int64    `json:"duration_ms"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type ProjectTestResult struct {
	ID         string `json:"id"`
	Framework  string `json:"framework"`
	Name       string `json:"name"`
	FullName   string `json:"full_name"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Package    string `json:"package,omitempty"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ProjectTestRunSummary struct {
	Total      int                 `json:"total"`
	Passed     int                 `json:"passed"`
	Failed     int                 `json:"failed"`
	Skipped    int                 `json:"skipped"`
	DurationMs int64               `json:"duration_ms"`
	Slowest    []ProjectTestResult `json:"slowest"`
}

type ProjectTestsRunResponse struct {
	RootFolder string                  `json:"root_folder"`
	RepoPath   string                  `json:"repo_path"`
	Mode       string                  `json:"mode"`
	Commands   []ProjectTestRunCommand `json:"commands"`
	Results    []ProjectTestResult     `json:"results"`
	Summary    ProjectTestRunSummary   `json:"summary"`
}

type ProjectTestsCoverageRequest struct {
	RepoPath  string `json:"repo_path,omitempty"`
	Framework string `json:"framework,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

type ProjectTestCoverageSegment struct {
	StartLine int  `json:"start_line"`
	EndLine   int  `json:"end_line"`
	Covered   bool `json:"covered"`
}

type ProjectTestCoverageFile struct {
	Path         string                       `json:"path"`
	Changed      bool                         `json:"changed"`
	CoveredLines int                          `json:"covered_lines"`
	TotalLines   int                          `json:"total_lines"`
	Percent      float64                      `json:"percent"`
	Segments     []ProjectTestCoverageSegment `json:"segments,omitempty"`
}

type ProjectTestCoverageFileRef struct {
	Path         string                       `json:"path"`
	Changed      bool                         `json:"changed"`
	CoveredLines int                          `json:"covered_lines"`
	TotalLines   int                          `json:"total_lines"`
	Percent      float64                      `json:"percent"`
	Segments     []ProjectTestCoverageSegment `json:"segments,omitempty"`
}

type ProjectTestCoverageMapping struct {
	TestID       string                       `json:"test_id"`
	TestName     string                       `json:"test_name"`
	Framework    string                       `json:"framework"`
	Path         string                       `json:"path,omitempty"`
	CoveredFiles []ProjectTestCoverageFileRef `json:"covered_files"`
}

type ProjectTestCoverageReport struct {
	Framework  string                       `json:"framework"`
	Supported  bool                         `json:"supported"`
	Mode       string                       `json:"mode"`
	Files      []ProjectTestCoverageFile    `json:"files"`
	Mappings   []ProjectTestCoverageMapping `json:"mappings"`
	Commands   []ProjectTestRunCommand      `json:"commands,omitempty"`
	Notes      []string                     `json:"notes,omitempty"`
	Generated  bool                         `json:"generated"`
	TotalFiles int                          `json:"total_files"`
}

type ProjectTestsCoverageResponse struct {
	RootFolder string                      `json:"root_folder"`
	RepoPath   string                      `json:"repo_path"`
	Reports    []ProjectTestCoverageReport `json:"reports"`
	Notes      []string                    `json:"notes,omitempty"`
}

type ProjectTestsBranchCacheRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectTestsBranchCacheResponse struct {
	RootFolder    string                        `json:"root_folder"`
	RepoPath      string                        `json:"repo_path"`
	CurrentBranch string                        `json:"current_branch,omitempty"`
	BaseBranch    string                        `json:"base_branch,omitempty"`
	ScopeHash     string                        `json:"scope_hash"`
	Cached        bool                          `json:"cached"`
	UpdatedAt     string                        `json:"updated_at,omitempty"`
	Discovery     ProjectTestsDiscoveryResponse `json:"discovery"`
	Run           *ProjectTestsRunResponse      `json:"run,omitempty"`
	Coverage      *ProjectTestsCoverageResponse `json:"coverage,omitempty"`
	Notes         []string                      `json:"notes,omitempty"`
}

type projectTestNodeBuilder struct {
	ID          string
	Framework   string
	Name        string
	FullName    string
	Path        string
	Line        int
	Type        string
	BranchAdded bool
	Metadata    map[string]string
	Children    []*projectTestNodeBuilder
}

type projectTestSelection struct {
	File ProjectTestFile
	Node ProjectTestNode
}

type projectTestCommandExecution struct {
	Command    ProjectTestRunCommand
	OutputPath string
}

type projectTestPlanError struct {
	message string
	status  string
}

func (e projectTestPlanError) Error() string {
	return e.message
}

func (s *Server) handleProjectTestsDiscovery(w http.ResponseWriter, r *http.Request) {
	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if !ok {
		return
	}

	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discover tests: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, discovery)
}

func (s *Server) handleProjectTestsRun(w http.ResponseWriter, r *http.Request) {
	var req ProjectTestsRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}

	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discover tests: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	response := executeProjectTests(ctx, targetRepoRoot, repoPath, discovery, req)
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectTestsCoverage(w http.ResponseWriter, r *http.Request) {
	var req ProjectTestsCoverageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}

	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discover tests: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	response := buildProjectTestsCoverage(ctx, targetRepoRoot, repoPath, discovery, req)
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectTestsBranchCache(w http.ResponseWriter, r *http.Request) {
	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if !ok {
		return
	}

	response, err := s.loadProjectTestsBranchCache(projectID, repoPath, targetRepoRoot)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectTestsBranchCacheRefresh(w http.ResponseWriter, r *http.Request) {
	var req ProjectTestsBranchCacheRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}

	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discover tests: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	runResponse := executeProjectTests(ctx, targetRepoRoot, repoPath, discovery, ProjectTestsRunRequest{
		RepoPath:  repoPath,
		Framework: "all",
		Mode:      "branch",
	})
	coverageResponse := buildProjectTestsCoverage(ctx, targetRepoRoot, repoPath, discovery, ProjectTestsCoverageRequest{
		RepoPath:  repoPath,
		Framework: "all",
		Mode:      "branch",
	})
	response, err := s.saveProjectTestsBranchCache(projectID, repoPath, discovery, runResponse, coverageResponse)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) resolveProjectTestsTarget(w http.ResponseWriter, r *http.Request, repoPathParam string) (string, string, string, bool) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return "", "", "", false
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return "", "", "", false
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPathParam)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return "", "", "", false
	}

	return projectID, normalizeProjectGitPRDescriptionRepoPath(repoPathParam), targetRepoRoot, true
}

func (s *Server) loadProjectTestsBranchCache(projectID string, repoPath string, repoRoot string) (ProjectTestsBranchCacheResponse, error) {
	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, repoRoot)
	if err != nil {
		return ProjectTestsBranchCacheResponse{}, fmt.Errorf("failed to discover tests: %w", err)
	}
	response := newProjectTestsBranchCacheResponse(repoPath, discovery)
	if s.store == nil {
		response.Notes = append(response.Notes, "Test cache store is not configured.")
		return response, nil
	}
	cache, err := s.store.GetProjectTestCache(projectID, repoPath, response.CurrentBranch, response.BaseBranch, response.ScopeHash)
	if err != nil {
		return ProjectTestsBranchCacheResponse{}, fmt.Errorf("failed to load project test cache: %w", err)
	}
	if cache == nil {
		return response, nil
	}
	applyProjectTestsBranchCache(&response, cache)
	return response, nil
}

func (s *Server) saveProjectTestsBranchCache(
	projectID string,
	repoPath string,
	discovery ProjectTestsDiscoveryResponse,
	runResponse ProjectTestsRunResponse,
	coverageResponse ProjectTestsCoverageResponse,
) (ProjectTestsBranchCacheResponse, error) {
	response := newProjectTestsBranchCacheResponse(repoPath, discovery)
	response.Cached = true
	response.Run = &runResponse
	response.Coverage = &coverageResponse
	now := time.Now().UTC()
	response.UpdatedAt = now.Format(time.RFC3339)
	if s.store == nil {
		response.Cached = false
		response.Notes = append(response.Notes, "Test cache store is not configured.")
		return response, nil
	}

	runJSON, err := json.Marshal(runResponse)
	if err != nil {
		return ProjectTestsBranchCacheResponse{}, fmt.Errorf("failed to encode test results for cache: %w", err)
	}
	coverageJSON, err := json.Marshal(coverageResponse)
	if err != nil {
		return ProjectTestsBranchCacheResponse{}, fmt.Errorf("failed to encode coverage results for cache: %w", err)
	}
	cache := &storage.ProjectTestCache{
		ProjectID:            projectID,
		RepoPath:             repoPath,
		Branch:               response.CurrentBranch,
		BaseBranch:           response.BaseBranch,
		ScopeHash:            response.ScopeHash,
		TestResponseJSON:     string(runJSON),
		CoverageResponseJSON: string(coverageJSON),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.store.SaveProjectTestCache(cache); err != nil {
		return ProjectTestsBranchCacheResponse{}, fmt.Errorf("failed to save project test cache: %w", err)
	}
	return response, nil
}

func newProjectTestsBranchCacheResponse(repoPath string, discovery ProjectTestsDiscoveryResponse) ProjectTestsBranchCacheResponse {
	branch := strings.TrimSpace(discovery.CurrentBranch)
	if branch == "" {
		branch = "HEAD"
	}
	return ProjectTestsBranchCacheResponse{
		RootFolder:    discovery.RootFolder,
		RepoPath:      repoPath,
		CurrentBranch: branch,
		BaseBranch:    strings.TrimSpace(discovery.BaseBranch),
		ScopeHash:     projectTestsBranchScopeHash(discovery),
		Cached:        false,
		Discovery:     discovery,
		Notes:         []string{},
	}
}

func applyProjectTestsBranchCache(response *ProjectTestsBranchCacheResponse, cache *storage.ProjectTestCache) {
	if response == nil || cache == nil {
		return
	}
	response.Cached = true
	if !cache.UpdatedAt.IsZero() {
		response.UpdatedAt = cache.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if raw := strings.TrimSpace(cache.TestResponseJSON); raw != "" {
		var run ProjectTestsRunResponse
		if err := json.Unmarshal([]byte(raw), &run); err != nil {
			response.Notes = append(response.Notes, "Failed to decode cached test results: "+err.Error())
		} else {
			response.Run = &run
		}
	}
	if raw := strings.TrimSpace(cache.CoverageResponseJSON); raw != "" {
		var coverage ProjectTestsCoverageResponse
		if err := json.Unmarshal([]byte(raw), &coverage); err != nil {
			response.Notes = append(response.Notes, "Failed to decode cached coverage results: "+err.Error())
		} else {
			response.Coverage = &coverage
		}
	}
}
