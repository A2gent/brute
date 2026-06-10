package http

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const (
	projectTestFrameworkRSpec = "rspec"
	projectTestFrameworkGo    = "go"
	projectTestFrameworkJest  = "jest"
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

var (
	rspecTestLinePattern = regexp.MustCompile("^\\s*(?:RSpec\\.)?(describe|context|feature|scenario|it|specify|example)\\s*(?:\\(?\\s*)?(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`|([^, do{]+))")
	goTestFuncPattern    = regexp.MustCompile(`^\s*func\s+(Test[A-Za-z0-9_]+)\s*\(\s*t\s+\*testing\.T\s*\)`)
	goSubtestPattern     = regexp.MustCompile("\\bt\\.Run\\(\\s*(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`)")
	jestTestLinePattern  = regexp.MustCompile("\\b(describe|it|test)\\s*(?:\\.\\w+)?\\s*\\(\\s*(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`)")
	gitDiffHunkPattern   = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
)

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
	response.Cached = true
	response.UpdatedAt = cache.UpdatedAt.UTC().Format(time.RFC3339)
	if strings.TrimSpace(cache.TestResponseJSON) != "" {
		var run ProjectTestsRunResponse
		if err := json.Unmarshal([]byte(cache.TestResponseJSON), &run); err == nil {
			response.Run = &run
		} else {
			response.Notes = append(response.Notes, "Cached branch test results could not be decoded.")
		}
	}
	if strings.TrimSpace(cache.CoverageResponseJSON) != "" {
		var coverage ProjectTestsCoverageResponse
		if err := json.Unmarshal([]byte(cache.CoverageResponseJSON), &coverage); err == nil {
			response.Coverage = &coverage
		} else {
			response.Notes = append(response.Notes, "Cached branch coverage results could not be decoded.")
		}
	}
	if response.Run == nil && response.Coverage == nil {
		response.Cached = false
	}
}

func projectTestsBranchScopeHash(discovery ProjectTestsDiscoveryResponse) string {
	hash := sha256.New()
	writeProjectTestsScopeHashPart(hash, "branch", discovery.CurrentBranch)
	writeProjectTestsScopeHashPart(hash, "base", discovery.BaseBranch)
	writeProjectTestsScopeHashPart(hash, "repo", discovery.RepoPath)
	if payload, err := json.Marshal(discovery.BranchTestFiles); err == nil {
		writeProjectTestsScopeHashPart(hash, "branch-tests", string(payload))
	}
	if payload, err := json.Marshal(changedCodeFileSet(discovery)); err == nil {
		writeProjectTestsScopeHashPart(hash, "changed-code", string(payload))
	}
	if projectHasGitMetadata(discovery.RootFolder) {
		target := projectGitBranchChangesTarget(discovery.RootFolder)
		if target.Available {
			if diff, err := runGitCommandPreserveLeading(discovery.RootFolder, "diff", "--no-color", "--find-renames", target.BaseRef+"...HEAD"); err == nil {
				writeProjectTestsScopeHashPart(hash, "diff", diff)
			}
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func writeProjectTestsScopeHashPart(hash hashWriter, label string, value string) {
	_, _ = hash.Write([]byte(label))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func buildProjectTestsDiscovery(_ string, repoPath string, repoRoot string) (ProjectTestsDiscoveryResponse, error) {
	target := projectGitBranchChangesTargetInfo{}
	branchStatuses := map[string]string{}
	branchChangedCodeFiles := map[string]bool{}
	if projectHasGitMetadata(repoRoot) {
		target = projectGitBranchChangesTarget(repoRoot)
		if target.Available {
			files, err := loadProjectGitBranchChangedFiles(repoRoot, target)
			if err != nil {
				return ProjectTestsDiscoveryResponse{}, err
			}
			for _, file := range files {
				branchStatuses[file.Path] = file.Status
				if classifyProjectTestFile(file.Path) == "" {
					branchChangedCodeFiles[file.Path] = true
				}
			}
		}
	}

	detections := detectProjectTestFrameworks(repoRoot)
	testFiles := make([]ProjectTestFile, 0)

	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if shouldSkipProjectTestDir(name) && path != repoRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		framework := classifyProjectTestFile(rel)
		if framework == "" {
			return nil
		}

		status := branchStatuses[rel]
		addedLines := map[int]bool{}
		if status == "A" {
			addedLines[-1] = true
		} else if target.Available && status != "" {
			addedLines = projectGitAddedLineNumbers(repoRoot, target, rel)
		}

		nodes := parseProjectTestFile(repoRoot, rel, framework, addedLines)
		testFile := ProjectTestFile{
			ID:        framework + ":" + rel,
			Framework: framework,
			Path:      rel,
			Tests:     nodes,
		}
		if framework == projectTestFrameworkGo {
			testFile.Package = goPackageFromTestPath(rel)
		}
		if status != "" {
			testFile.BranchStatus = status
			testFile.BranchScope = projectTestBranchScope(status)
		}
		testFiles = append(testFiles, testFile)
		return nil
	})
	if err != nil {
		return ProjectTestsDiscoveryResponse{}, err
	}

	sort.SliceStable(testFiles, func(i, j int) bool {
		if testFiles[i].Framework != testFiles[j].Framework {
			return testFiles[i].Framework < testFiles[j].Framework
		}
		return testFiles[i].Path < testFiles[j].Path
	})

	branchTestFiles := make([]ProjectTestFile, 0)
	testCounts := map[string]int{}
	branchTestCounts := map[string]int{}
	for _, file := range testFiles {
		count := countProjectTestNodes(file.Tests)
		testCounts[file.Framework] += count
		if file.BranchStatus != "" {
			branchTestFiles = append(branchTestFiles, file)
			branchTestCounts[file.Framework] += count
		}
	}

	frameworks := make([]ProjectTestFrameworkInfo, 0, 3)
	for _, id := range []string{projectTestFrameworkRSpec, projectTestFrameworkGo, projectTestFrameworkJest} {
		info := detections[id]
		if testCounts[id] > 0 {
			info.Available = true
			if info.Reason == "" {
				info.Reason = "Detected test files"
			}
		}
		info.TestCount = testCounts[id]
		info.BranchTestCount = branchTestCounts[id]
		info.SupportsIndividual = true
		switch id {
		case projectTestFrameworkGo:
			info.SupportsCoverage = true
			info.CoverageMode = "per-test branch mapping and aggregate coverprofile"
		case projectTestFrameworkJest:
			info.SupportsCoverage = true
			info.CoverageMode = "aggregate Istanbul coverage"
		case projectTestFrameworkRSpec:
			info.SupportsCoverage = false
			info.CoverageMode = "requires project SimpleCov setup"
		}
		frameworks = append(frameworks, info)
	}

	_ = branchChangedCodeFiles
	return ProjectTestsDiscoveryResponse{
		RootFolder:             repoRoot,
		RepoPath:               repoPath,
		CurrentBranch:          target.CurrentBranch,
		BaseBranch:             target.BaseBranch,
		BranchChangesAvailable: target.Available,
		Frameworks:             frameworks,
		TestFiles:              testFiles,
		BranchTestFiles:        branchTestFiles,
	}, nil
}

func detectProjectTestFrameworks(repoRoot string) map[string]ProjectTestFrameworkInfo {
	frameworks := map[string]ProjectTestFrameworkInfo{
		projectTestFrameworkRSpec: {ID: projectTestFrameworkRSpec, Label: "RSpec"},
		projectTestFrameworkGo:    {ID: projectTestFrameworkGo, Label: "Go test"},
		projectTestFrameworkJest:  {ID: projectTestFrameworkJest, Label: "Jest"},
	}

	if projectTestFileExists(repoRoot, ".rspec") || projectTestDirExists(repoRoot, "spec") || projectTestFileContains(repoRoot, "Gemfile", "rspec") {
		info := frameworks[projectTestFrameworkRSpec]
		info.Available = true
		info.Reason = "RSpec project markers found"
		frameworks[projectTestFrameworkRSpec] = info
	}
	if projectTestFileExists(repoRoot, "go.mod") {
		info := frameworks[projectTestFrameworkGo]
		info.Available = true
		info.Reason = "go.mod found"
		frameworks[projectTestFrameworkGo] = info
	}
	if projectTestPackageJSONHasJest(repoRoot) {
		info := frameworks[projectTestFrameworkJest]
		info.Available = true
		info.Reason = "package.json references jest"
		frameworks[projectTestFrameworkJest] = info
	}

	return frameworks
}

func classifyProjectTestFile(relPath string) string {
	path := filepath.ToSlash(relPath)
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(lower, "_spec.rb") && (strings.HasPrefix(lower, "spec/") || strings.Contains(lower, "/spec/")) {
		return projectTestFrameworkRSpec
	}
	if strings.HasSuffix(lower, "_test.go") {
		return projectTestFrameworkGo
	}
	if strings.Contains(lower, "/__tests__/") || strings.HasPrefix(lower, "__tests__/") {
		if isJestTestExtension(base) {
			return projectTestFrameworkJest
		}
	}
	for _, marker := range []string{".test.", ".spec."} {
		if strings.Contains(base, marker) && isJestTestExtension(base) {
			return projectTestFrameworkJest
		}
	}
	return ""
}

func isJestTestExtension(base string) bool {
	for _, suffix := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func parseProjectTestFile(repoRoot string, relPath string, framework string, addedLines map[int]bool) []ProjectTestNode {
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	if err != nil || len(content) > 2*1024*1024 {
		return []ProjectTestNode{}
	}
	switch framework {
	case projectTestFrameworkRSpec:
		return parseRSpecTests(relPath, string(content), addedLines)
	case projectTestFrameworkGo:
		return parseGoTests(relPath, string(content), addedLines)
	case projectTestFrameworkJest:
		return parseJestTests(relPath, string(content), addedLines)
	default:
		return []ProjectTestNode{}
	}
}

func parseRSpecTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	return parseIndentedTests(relPath, content, projectTestFrameworkRSpec, func(line string) (string, string, bool) {
		matches := rspecTestLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return "", "", false
		}
		kind := matches[1]
		name := firstNonEmptyProjectTestMatch(matches, 2)
		if name == "" {
			name = kind
		}
		nodeType := "group"
		if kind == "it" || kind == "specify" || kind == "example" || kind == "scenario" {
			nodeType = "test"
		}
		return nodeType, name, true
	}, addedLines)
}

func parseJestTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	return parseIndentedTests(relPath, content, projectTestFrameworkJest, func(line string) (string, string, bool) {
		matches := jestTestLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return "", "", false
		}
		kind := matches[1]
		name := firstNonEmptyProjectTestMatch(matches, 2)
		if name == "" {
			name = kind
		}
		nodeType := "test"
		if kind == "describe" {
			nodeType = "group"
		}
		return nodeType, name, true
	}, addedLines)
}

func parseIndentedTests(relPath string, content string, framework string, matcher func(string) (string, string, bool), addedLines map[int]bool) []ProjectTestNode {
	roots := make([]*projectTestNodeBuilder, 0)
	stack := make([]*projectTestNodeBuilder, 0)
	indents := make([]int, 0)

	lines := strings.Split(content, "\n")
	for index, line := range lines {
		nodeType, name, ok := matcher(line)
		if !ok {
			continue
		}
		lineNumber := index + 1
		indent := leadingProjectTestIndent(line)
		for len(stack) > 0 && indent <= indents[len(indents)-1] {
			stack = stack[:len(stack)-1]
			indents = indents[:len(indents)-1]
		}
		fullName := name
		if len(stack) > 0 {
			parts := make([]string, 0, len(stack)+1)
			for _, parent := range stack {
				parts = append(parts, parent.Name)
			}
			parts = append(parts, name)
			fullName = strings.Join(parts, " / ")
		}
		node := &projectTestNodeBuilder{
			ID:          projectTestNodeID(framework, relPath, lineNumber),
			Framework:   framework,
			Name:        name,
			FullName:    fullName,
			Path:        relPath,
			Line:        lineNumber,
			Type:        nodeType,
			BranchAdded: addedLines[-1] || addedLines[lineNumber],
		}
		if len(stack) == 0 {
			roots = append(roots, node)
		} else {
			stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, node)
		}
		if nodeType == "group" {
			stack = append(stack, node)
			indents = append(indents, indent)
		}
	}

	return projectTestBuildersToNodes(roots)
}

func parseGoTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	roots := make([]*projectTestNodeBuilder, 0)
	var current *projectTestNodeBuilder
	subtestStack := make([]*projectTestNodeBuilder, 0)
	subtestIndents := make([]int, 0)
	lines := strings.Split(content, "\n")

	for index, line := range lines {
		lineNumber := index + 1
		if matches := goTestFuncPattern.FindStringSubmatch(line); matches != nil {
			name := matches[1]
			current = &projectTestNodeBuilder{
				ID:          projectTestNodeID(projectTestFrameworkGo, relPath, lineNumber),
				Framework:   projectTestFrameworkGo,
				Name:        name,
				FullName:    name,
				Path:        relPath,
				Line:        lineNumber,
				Type:        "test",
				BranchAdded: addedLines[-1] || addedLines[lineNumber],
				Metadata: map[string]string{
					"package": goPackageFromTestPath(relPath),
				},
			}
			roots = append(roots, current)
			subtestStack = subtestStack[:0]
			subtestIndents = subtestIndents[:0]
			continue
		}
		if current == nil {
			continue
		}
		matches := goSubtestPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		indent := leadingProjectTestIndent(line)
		for len(subtestStack) > 0 && indent <= subtestIndents[len(subtestIndents)-1] {
			subtestStack = subtestStack[:len(subtestStack)-1]
			subtestIndents = subtestIndents[:len(subtestIndents)-1]
		}
		name := firstNonEmptyProjectTestMatch(matches, 1)
		if name == "" {
			name = "subtest"
		}
		parent := current
		fullParts := []string{current.Name}
		if len(subtestStack) > 0 {
			parent = subtestStack[len(subtestStack)-1]
			fullParts = strings.Split(parent.FullName, " / ")
		}
		fullParts = append(fullParts, name)
		node := &projectTestNodeBuilder{
			ID:          projectTestNodeID(projectTestFrameworkGo, relPath, lineNumber),
			Framework:   projectTestFrameworkGo,
			Name:        name,
			FullName:    strings.Join(fullParts, " / "),
			Path:        relPath,
			Line:        lineNumber,
			Type:        "test",
			BranchAdded: addedLines[-1] || addedLines[lineNumber],
			Metadata: map[string]string{
				"package": goPackageFromTestPath(relPath),
			},
		}
		parent.Children = append(parent.Children, node)
		subtestStack = append(subtestStack, node)
		subtestIndents = append(subtestIndents, indent)
	}

	return projectTestBuildersToNodes(roots)
}

func projectTestBuildersToNodes(builders []*projectTestNodeBuilder) []ProjectTestNode {
	nodes := make([]ProjectTestNode, 0, len(builders))
	for _, builder := range builders {
		node := ProjectTestNode{
			ID:          builder.ID,
			Framework:   builder.Framework,
			Name:        builder.Name,
			FullName:    builder.FullName,
			Path:        builder.Path,
			Line:        builder.Line,
			Type:        builder.Type,
			BranchAdded: builder.BranchAdded,
			Metadata:    builder.Metadata,
			Children:    projectTestBuildersToNodes(builder.Children),
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func executeProjectTests(ctx context.Context, repoRoot string, repoPath string, discovery ProjectTestsDiscoveryResponse, req ProjectTestsRunRequest) ProjectTestsRunResponse {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		if strings.TrimSpace(req.TestID) != "" || strings.TrimSpace(req.Path) != "" {
			mode = "test"
		} else {
			mode = "project"
		}
	}

	response := ProjectTestsRunResponse{
		RootFolder: repoRoot,
		RepoPath:   repoPath,
		Mode:       mode,
		Commands:   []ProjectTestRunCommand{},
		Results:    []ProjectTestResult{},
	}

	frameworks := projectTestTargetFrameworks(discovery, req.Framework)
	if len(frameworks) == 0 {
		response.Results = append(response.Results, ProjectTestResult{
			ID:        "no-framework",
			Framework: strings.TrimSpace(req.Framework),
			Name:      "No test framework detected",
			FullName:  "No test framework detected",
			Status:    "failed",
			Error:     "No supported test framework was detected for this project.",
		})
		response.Summary = summarizeProjectTestResults(response.Results)
		return response
	}

	for _, framework := range frameworks {
		results, commands := executeProjectFrameworkTests(ctx, repoRoot, discovery, framework, mode, req)
		response.Results = append(response.Results, results...)
		response.Commands = append(response.Commands, commands...)
	}
	response.Summary = summarizeProjectTestResults(response.Results)
	for _, command := range response.Commands {
		response.Summary.DurationMs += command.DurationMs
	}
	return response
}

func executeProjectFrameworkTests(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, framework string, mode string, req ProjectTestsRunRequest) ([]ProjectTestResult, []ProjectTestRunCommand) {
	var command string
	var args []string
	var outputPath string
	var planErr error

	switch framework {
	case projectTestFrameworkRSpec:
		command, args, planErr = buildRSpecTestCommand(repoRoot, discovery, mode, req)
	case projectTestFrameworkGo:
		command, args, planErr = buildGoTestCommand(discovery, mode, req)
	case projectTestFrameworkJest:
		command, args, outputPath, planErr = buildJestTestCommand(repoRoot, discovery, mode, req)
	default:
		planErr = fmt.Errorf("unsupported framework %q", framework)
	}
	if planErr != nil {
		result := ProjectTestResult{
			ID:        framework + ":plan-error",
			Framework: framework,
			Name:      framework,
			FullName:  framework,
			Status:    "failed",
			Error:     planErr.Error(),
		}
		return []ProjectTestResult{result}, []ProjectTestRunCommand{}
	}

	execution := runProjectTestCommand(ctx, repoRoot, framework, command, args, nil)
	execution.OutputPath = outputPath

	results := parseProjectTestCommandResults(repoRoot, discovery, framework, execution)
	return results, []ProjectTestRunCommand{execution.Command}
}

func buildRSpecTestCommand(repoRoot string, discovery ProjectTestsDiscoveryResponse, mode string, req ProjectTestsRunRequest) (string, []string, error) {
	command := "rspec"
	args := []string{"--format", "json"}
	if projectTestFileExists(repoRoot, "Gemfile") {
		if _, err := exec.LookPath("bundle"); err == nil {
			command = "bundle"
			args = []string{"exec", "rspec", "--format", "json"}
		}
	}

	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
		return command, args, nil
	case "branch":
		paths := branchProjectTestPaths(discovery, projectTestFrameworkRSpec)
		if len(paths) == 0 {
			return "", nil, errors.New("no RSpec test files changed on this branch")
		}
		args = append(args, paths...)
		return command, args, nil
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, errors.New("test file path is required")
		}
		args = append(args, filepath.ToSlash(path))
		return command, args, nil
	default:
		if !hasSelection {
			return "", nil, errors.New("test selection is required")
		}
		target := selection.File.Path
		if selection.Node.Line > 0 {
			target = fmt.Sprintf("%s:%d", selection.File.Path, selection.Node.Line)
		}
		args = append(args, target)
		return command, args, nil
	}
}

func buildGoTestCommand(discovery ProjectTestsDiscoveryResponse, mode string, req ProjectTestsRunRequest) (string, []string, error) {
	args := []string{"test", "-json"}
	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
		args = append(args, "./...")
	case "branch":
		packages := branchProjectTestPackages(discovery, projectTestFrameworkGo)
		if len(packages) == 0 {
			return "", nil, errors.New("no Go test files changed on this branch")
		}
		args = append(args, packages...)
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, errors.New("test file path is required")
		}
		args = append(args, goPackageFromTestPath(path))
	default:
		if !hasSelection {
			return "", nil, errors.New("test selection is required")
		}
		args = append(args, "-run", goTestRunPattern(selection.Node), goPackageFromTestPath(selection.File.Path))
	}
	return "go", args, nil
}

func buildJestTestCommand(repoRoot string, discovery ProjectTestsDiscoveryResponse, mode string, req ProjectTestsRunRequest) (string, []string, string, error) {
	outputFile, err := os.CreateTemp("", "a2gent-jest-results-*.json")
	if err != nil {
		return "", nil, "", err
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()

	command, args := projectJestCommand(repoRoot)
	args = append(args, "--runInBand", "--json", "--outputFile", outputPath, "--testLocationInResults")

	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
	case "branch":
		paths := branchProjectTestPaths(discovery, projectTestFrameworkJest)
		if len(paths) == 0 {
			return "", nil, "", errors.New("no Jest test files changed on this branch")
		}
		args = append(args, paths...)
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, "", errors.New("test file path is required")
		}
		args = append(args, filepath.ToSlash(path))
	default:
		if !hasSelection {
			return "", nil, "", errors.New("test selection is required")
		}
		args = append(args, selection.File.Path, "--testNamePattern", selection.Node.FullName)
	}
	return command, args, outputPath, nil
}

func runProjectTestCommand(ctx context.Context, repoRoot string, framework string, command string, args []string, env []string) projectTestCommandExecution {
	start := time.Now()
	runtimeCommand, runtimeArgs := projectTestRuntimeCommand(repoRoot, framework, command, args)
	cmd := exec.CommandContext(ctx, runtimeCommand, runtimeArgs...)
	cmd.Dir = repoRoot
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	duration := time.Since(start).Milliseconds()
	trimmedOutput := truncateProjectTestOutput(strings.TrimRight(string(output), "\r\n"), 128*1024)
	exitCode := 0
	errorText := ""
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if ctx.Err() != nil {
			errorText = ctx.Err().Error()
		} else {
			errorText = err.Error()
		}
	}
	return projectTestCommandExecution{
		Command: ProjectTestRunCommand{
			Framework:  framework,
			Command:    runtimeCommand,
			Args:       runtimeArgs,
			Display:    strings.Join(append([]string{runtimeCommand}, runtimeArgs...), " "),
			ExitCode:   exitCode,
			DurationMs: duration,
			Output:     trimmedOutput,
			Error:      errorText,
		},
	}
}

func parseProjectTestCommandResults(repoRoot string, discovery ProjectTestsDiscoveryResponse, framework string, execution projectTestCommandExecution) []ProjectTestResult {
	switch framework {
	case projectTestFrameworkRSpec:
		if results := parseRSpecTestResults(repoRoot, execution.Command.Output); len(results) > 0 {
			return results
		}
	case projectTestFrameworkGo:
		if results := parseGoTestResults(discovery, execution.Command.Output); len(results) > 0 {
			return results
		}
	case projectTestFrameworkJest:
		if results := parseJestTestResults(repoRoot, execution.OutputPath); len(results) > 0 {
			_ = os.Remove(execution.OutputPath)
			return results
		}
		_ = os.Remove(execution.OutputPath)
	}

	status := "passed"
	if execution.Command.ExitCode != 0 {
		status = "failed"
	}
	return []ProjectTestResult{{
		ID:         framework + ":command",
		Framework:  framework,
		Name:       execution.Command.Display,
		FullName:   execution.Command.Display,
		Status:     status,
		DurationMs: execution.Command.DurationMs,
		Output:     execution.Command.Output,
		Error:      execution.Command.Error,
	}}
}

func parseRSpecTestResults(repoRoot string, output string) []ProjectTestResult {
	type rspecException struct {
		ClassName string   `json:"class"`
		Message   string   `json:"message"`
		Backtrace []string `json:"backtrace"`
	}
	type rspecExample struct {
		ID              string          `json:"id"`
		Description     string          `json:"description"`
		FullDescription string          `json:"full_description"`
		Status          string          `json:"status"`
		FilePath        string          `json:"file_path"`
		LineNumber      int             `json:"line_number"`
		RunTime         float64         `json:"run_time"`
		Exception       *rspecException `json:"exception"`
	}
	type rspecJSON struct {
		Examples []rspecExample `json:"examples"`
	}
	start := strings.Index(output, `{"version"`)
	if start < 0 {
		return nil
	}
	var parsed rspecJSON
	decoder := json.NewDecoder(strings.NewReader(output[start:]))
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	results := make([]ProjectTestResult, 0, len(parsed.Examples))
	for _, example := range parsed.Examples {
		path := normalizeProjectTestResultPath(repoRoot, example.FilePath)
		status := normalizeProjectTestStatus(example.Status)
		errorText := ""
		if example.Exception != nil {
			errorText = strings.TrimSpace(example.Exception.Message)
			if len(example.Exception.Backtrace) > 0 {
				errorText = strings.TrimSpace(errorText + "\n" + strings.Join(example.Exception.Backtrace, "\n"))
			}
		}
		results = append(results, ProjectTestResult{
			ID:         nonEmptyProjectTestID(example.ID, projectTestNodeID(projectTestFrameworkRSpec, path, example.LineNumber)),
			Framework:  projectTestFrameworkRSpec,
			Name:       nonEmptyProjectTestName(example.Description, example.FullDescription),
			FullName:   nonEmptyProjectTestName(example.FullDescription, example.Description),
			Path:       path,
			Line:       example.LineNumber,
			Status:     status,
			DurationMs: int64(math.Round(example.RunTime * 1000)),
			Error:      errorText,
		})
	}
	return results
}

func parseGoTestResults(discovery ProjectTestsDiscoveryResponse, output string) []ProjectTestResult {
	type goJSONEvent struct {
		Action  string  `json:"Action"`
		Package string  `json:"Package"`
		Test    string  `json:"Test"`
		Elapsed float64 `json:"Elapsed"`
		Output  string  `json:"Output"`
	}
	type goResultState struct {
		event  goJSONEvent
		output strings.Builder
	}
	states := map[string]*goResultState{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event goJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Test == "" {
			continue
		}
		key := event.Package + "\x00" + event.Test
		state := states[key]
		if state == nil {
			state = &goResultState{}
			states[key] = state
		}
		if event.Output != "" {
			state.output.WriteString(event.Output)
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			state.event = event
		}
	}

	results := make([]ProjectTestResult, 0, len(states))
	for _, state := range states {
		if state.event.Action == "" {
			continue
		}
		path, line := findGoTestPath(discovery, state.event.Test)
		results = append(results, ProjectTestResult{
			ID:         projectTestFrameworkGo + ":" + state.event.Package + ":" + state.event.Test,
			Framework:  projectTestFrameworkGo,
			Name:       state.event.Test,
			FullName:   state.event.Test,
			Path:       path,
			Line:       line,
			Package:    state.event.Package,
			Status:     normalizeProjectTestStatus(state.event.Action),
			DurationMs: int64(math.Round(state.event.Elapsed * 1000)),
			Output:     strings.TrimSpace(state.output.String()),
		})
	}
	sortProjectTestResults(results)
	return results
}

func parseJestTestResults(repoRoot string, outputPath string) []ProjectTestResult {
	if outputPath == "" {
		return nil
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		return nil
	}
	type jestLocation struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	}
	type jestAssertion struct {
		Title           string        `json:"title"`
		FullName        string        `json:"fullName"`
		Status          string        `json:"status"`
		Duration        int64         `json:"duration"`
		FailureMessages []string      `json:"failureMessages"`
		Location        *jestLocation `json:"location"`
	}
	type jestFileResult struct {
		Name             string          `json:"name"`
		Status           string          `json:"status"`
		AssertionResults []jestAssertion `json:"assertionResults"`
	}
	type jestJSON struct {
		TestResults []jestFileResult `json:"testResults"`
	}
	var parsed jestJSON
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil
	}
	results := make([]ProjectTestResult, 0)
	for _, fileResult := range parsed.TestResults {
		path := normalizeProjectTestResultPath(repoRoot, fileResult.Name)
		for _, assertion := range fileResult.AssertionResults {
			line := 0
			if assertion.Location != nil {
				line = assertion.Location.Line
			}
			results = append(results, ProjectTestResult{
				ID:         projectTestNodeID(projectTestFrameworkJest, path, line),
				Framework:  projectTestFrameworkJest,
				Name:       nonEmptyProjectTestName(assertion.Title, assertion.FullName),
				FullName:   nonEmptyProjectTestName(assertion.FullName, assertion.Title),
				Path:       path,
				Line:       line,
				Status:     normalizeProjectTestStatus(assertion.Status),
				DurationMs: assertion.Duration,
				Error:      strings.Join(assertion.FailureMessages, "\n"),
			})
		}
	}
	sortProjectTestResults(results)
	return results
}

func buildProjectTestsCoverage(ctx context.Context, repoRoot string, repoPath string, discovery ProjectTestsDiscoveryResponse, req ProjectTestsCoverageRequest) ProjectTestsCoverageResponse {
	frameworks := projectTestTargetFrameworks(discovery, req.Framework)
	response := ProjectTestsCoverageResponse{
		RootFolder: repoRoot,
		RepoPath:   repoPath,
		Reports:    []ProjectTestCoverageReport{},
	}
	if len(frameworks) == 0 {
		response.Notes = append(response.Notes, "No supported test framework was detected for this project.")
		return response
	}

	changedFiles := changedCodeFileSet(discovery)
	for _, framework := range frameworks {
		switch framework {
		case projectTestFrameworkGo:
			response.Reports = append(response.Reports, buildGoProjectTestCoverage(ctx, repoRoot, discovery, changedFiles))
		case projectTestFrameworkJest:
			response.Reports = append(response.Reports, buildJestProjectTestCoverage(ctx, repoRoot, changedFiles))
		case projectTestFrameworkRSpec:
			response.Reports = append(response.Reports, buildRSpecProjectTestCoverage(ctx, repoRoot, discovery, changedFiles))
		}
	}
	return response
}

func buildRSpecProjectTestCoverage(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool) ProjectTestCoverageReport {
	report := ProjectTestCoverageReport{
		Framework: projectTestFrameworkRSpec,
		Supported: true,
		Mode:      "simplecov resultset",
		Files:     []ProjectTestCoverageFile{},
		Mappings:  []ProjectTestCoverageMapping{},
		Notes: []string{
			"RSpec file coverage is aggregate from the last SimpleCov run. Test line mapping is generated from branch-scoped examples.",
		},
	}
	resultsetPath := findSimpleCovResultset(repoRoot)
	if resultsetPath == "" {
		report.Notes = append(report.Notes, "No SimpleCov resultset found. Run RSpec with SimpleCov enabled before loading coverage.")
		return report
	}
	files, err := parseSimpleCovResultset(resultsetPath, repoRoot, changedFiles)
	if err != nil {
		report.Notes = append(report.Notes, "Failed to parse SimpleCov resultset: "+err.Error())
		return report
	}
	report.Files = files
	report.TotalFiles = len(files)
	report.Generated = len(files) > 0
	report.Mappings = append(report.Mappings, buildRSpecProjectTestCoverageMappings(ctx, repoRoot, discovery, changedFiles, filepath.Dir(resultsetPath), &report)...)
	return report
}

func buildRSpecProjectTestCoverageMappings(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool, coverageDir string, report *ProjectTestCoverageReport) []ProjectTestCoverageMapping {
	if len(changedFiles) == 0 {
		return nil
	}
	branchTests := branchRSpecCoverageTests(discovery)
	if len(branchTests) == 0 {
		report.Notes = append(report.Notes, "No branch-scoped RSpec examples were found for test-to-line coverage mapping.")
		return nil
	}
	if len(branchTests) > 30 {
		report.Notes = append(report.Notes, "RSpec test-to-line coverage mapping was limited to the first 30 branch-scoped examples.")
		branchTests = branchTests[:30]
	}

	restoreCoverageDir, err := moveProjectCoverageDirAside(coverageDir)
	if err != nil {
		report.Notes = append(report.Notes, "Failed to isolate SimpleCov coverage directory for per-test mapping: "+err.Error())
		return nil
	}
	defer func() {
		if restoreErr := restoreCoverageDir(); restoreErr != nil {
			report.Notes = append(report.Notes, "Failed to restore SimpleCov coverage directory after per-test mapping: "+restoreErr.Error())
		}
	}()

	mappings := make([]ProjectTestCoverageMapping, 0, len(branchTests))
	for _, selection := range branchTests {
		_ = os.RemoveAll(coverageDir)
		specTarget := fmt.Sprintf("%s:%d", selection.File.Path, selection.Node.Line)
		execution := runProjectTestCommand(ctx, repoRoot, projectTestFrameworkRSpec, "bundle", []string{"exec", "rspec", "--format", "json", specTarget}, nil)
		report.Commands = append(report.Commands, execution.Command)
		files, parseErr := parseSimpleCovResultset(filepath.Join(coverageDir, ".resultset.json"), repoRoot, changedFiles)
		if parseErr != nil {
			continue
		}
		coveredFiles := coverageRefsFromChangedFiles(files)
		if len(coveredFiles) == 0 {
			continue
		}
		mappings = append(mappings, ProjectTestCoverageMapping{
			TestID:       selection.Node.ID,
			TestName:     selection.Node.FullName,
			Framework:    projectTestFrameworkRSpec,
			Path:         selection.File.Path,
			CoveredFiles: coveredFiles,
		})
	}
	_ = os.RemoveAll(coverageDir)
	return mappings
}

func buildGoProjectTestCoverage(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool) ProjectTestCoverageReport {
	report := ProjectTestCoverageReport{
		Framework: projectTestFrameworkGo,
		Supported: true,
		Mode:      "go coverprofile",
		Files:     []ProjectTestCoverageFile{},
		Mappings:  []ProjectTestCoverageMapping{},
		Commands:  []ProjectTestRunCommand{},
		Notes:     []string{},
	}

	aggregate, err := os.CreateTemp("", "a2gent-go-cover-*.out")
	if err != nil {
		report.Notes = append(report.Notes, err.Error())
		return report
	}
	aggregatePath := aggregate.Name()
	_ = aggregate.Close()
	defer os.Remove(aggregatePath)

	execution := runProjectTestCommand(ctx, repoRoot, projectTestFrameworkGo, "go", []string{"test", "-coverprofile", aggregatePath, "./..."}, nil)
	report.Commands = append(report.Commands, execution.Command)
	if files, parseErr := parseGoCoverageProfile(repoRoot, aggregatePath, changedFiles); parseErr == nil {
		report.Files = files
		report.TotalFiles = len(files)
		report.Generated = len(files) > 0
	} else {
		report.Notes = append(report.Notes, "Failed to parse aggregate Go coverage: "+parseErr.Error())
	}

	branchTests := branchGoCoverageTests(discovery)
	if len(branchTests) == 0 {
		report.Notes = append(report.Notes, "No branch-scoped Go tests were found for per-test coverage mapping.")
		return report
	}
	if len(branchTests) > 50 {
		report.Notes = append(report.Notes, "Per-test Go coverage mapping was limited to the first 50 branch-scoped tests.")
		branchTests = branchTests[:50]
	}
	for _, selection := range branchTests {
		coverageFile, err := os.CreateTemp("", "a2gent-go-cover-test-*.out")
		if err != nil {
			report.Notes = append(report.Notes, err.Error())
			continue
		}
		coveragePath := coverageFile.Name()
		_ = coverageFile.Close()
		args := []string{"test", "-run", goTestRunPattern(selection.Node), "-coverprofile", coveragePath, goPackageFromTestPath(selection.File.Path)}
		testExecution := runProjectTestCommand(ctx, repoRoot, projectTestFrameworkGo, "go", args, nil)
		report.Commands = append(report.Commands, testExecution.Command)
		files, parseErr := parseGoCoverageProfile(repoRoot, coveragePath, changedFiles)
		_ = os.Remove(coveragePath)
		if parseErr != nil {
			continue
		}
		report.Mappings = append(report.Mappings, ProjectTestCoverageMapping{
			TestID:       selection.Node.ID,
			TestName:     selection.Node.FullName,
			Framework:    projectTestFrameworkGo,
			Path:         selection.File.Path,
			CoveredFiles: coverageRefsFromChangedFiles(files),
		})
	}
	return report
}

func buildJestProjectTestCoverage(ctx context.Context, repoRoot string, changedFiles map[string]bool) ProjectTestCoverageReport {
	report := ProjectTestCoverageReport{
		Framework: projectTestFrameworkJest,
		Supported: true,
		Mode:      "istanbul aggregate",
		Files:     []ProjectTestCoverageFile{},
		Commands:  []ProjectTestRunCommand{},
		Notes: []string{
			"Jest coverage is aggregate. Jest does not expose a standard per-test-to-file mapping without custom instrumentation.",
		},
	}
	coverageDir, err := os.MkdirTemp("", "a2gent-jest-coverage-*")
	if err != nil {
		report.Notes = append(report.Notes, err.Error())
		return report
	}
	defer os.RemoveAll(coverageDir)

	command, args := projectJestCommand(repoRoot)
	args = append(args, "--runInBand", "--coverage", "--coverageReporters=json", "--coverageDirectory", coverageDir)
	execution := runProjectTestCommand(ctx, repoRoot, projectTestFrameworkJest, command, args, nil)
	report.Commands = append(report.Commands, execution.Command)

	files, parseErr := parseIstanbulCoverage(filepath.Join(coverageDir, "coverage-final.json"), repoRoot, changedFiles)
	if parseErr != nil {
		report.Notes = append(report.Notes, "Failed to parse Jest coverage: "+parseErr.Error())
		return report
	}
	report.Files = files
	report.TotalFiles = len(files)
	report.Generated = len(files) > 0
	return report
}

func parseGoCoverageProfile(repoRoot string, profilePath string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	modulePath := ""
	if output, err := runProjectTestQuickCommand(repoRoot, "go", "list", "-m"); err == nil {
		modulePath = strings.TrimSpace(output)
	}
	byPath := map[string]*ProjectTestCoverageFile{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.Index(line, ":")
		space := strings.Index(line, " ")
		if colon <= 0 || space <= colon {
			continue
		}
		rawPath := line[:colon]
		fields := strings.Fields(line[space+1:])
		if len(fields) < 2 {
			continue
		}
		ranges := strings.Split(line[colon+1:space], ",")
		if len(ranges) != 2 {
			continue
		}
		startLine := parseProjectTestCoverageLine(ranges[0])
		endLine := parseProjectTestCoverageLine(ranges[1])
		if startLine <= 0 || endLine <= 0 {
			continue
		}
		count, _ := strconv.Atoi(fields[1])
		relPath := normalizeGoCoveragePath(repoRoot, modulePath, rawPath)
		file := byPath[relPath]
		if file == nil {
			file = &ProjectTestCoverageFile{Path: relPath, Changed: changedFiles[relPath]}
			byPath[relPath] = file
		}
		covered := count > 0
		file.Segments = append(file.Segments, ProjectTestCoverageSegment{
			StartLine: startLine,
			EndLine:   endLine,
			Covered:   covered,
		})
		lines := endLine - startLine + 1
		if lines < 1 {
			lines = 1
		}
		file.TotalLines += lines
		if covered {
			file.CoveredLines += lines
		}
	}
	return finishCoverageFiles(byPath), nil
}

func parseIstanbulCoverage(path string, repoRoot string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type loc struct {
		Line int `json:"line"`
	}
	type statementRange struct {
		Start loc `json:"start"`
		End   loc `json:"end"`
	}
	type istanbulFile struct {
		StatementMap map[string]statementRange `json:"statementMap"`
		Statements   map[string]int            `json:"s"`
	}
	parsed := map[string]istanbulFile{}
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, err
	}
	byPath := map[string]*ProjectTestCoverageFile{}
	for rawPath, fileCoverage := range parsed {
		relPath := normalizeProjectTestResultPath(repoRoot, rawPath)
		file := &ProjectTestCoverageFile{Path: relPath, Changed: changedFiles[relPath]}
		for id, statement := range fileCoverage.StatementMap {
			count := fileCoverage.Statements[id]
			startLine := statement.Start.Line
			endLine := statement.End.Line
			if startLine <= 0 || endLine <= 0 {
				continue
			}
			covered := count > 0
			file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: startLine, EndLine: endLine, Covered: covered})
			lines := endLine - startLine + 1
			if lines < 1 {
				lines = 1
			}
			file.TotalLines += lines
			if covered {
				file.CoveredLines += lines
			}
		}
		byPath[relPath] = file
	}
	return finishCoverageFiles(byPath), nil
}

func findSimpleCovResultset(repoRoot string) string {
	for _, relPath := range []string{
		"coverage/.resultset.json",
		"spec/coverage/.resultset.json",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func parseSimpleCovResultset(path string, repoRoot string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type simpleCovRun struct {
		Coverage map[string]json.RawMessage `json:"coverage"`
	}
	parsed := map[string]simpleCovRun{}
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, err
	}
	byPath := map[string]map[int]bool{}
	for _, run := range parsed {
		for rawPath, rawCoverage := range run.Coverage {
			lines, parseErr := parseSimpleCovLines(rawCoverage)
			if parseErr != nil {
				continue
			}
			relPath := normalizeProjectTestResultPath(repoRoot, rawPath)
			if relPath == "" || strings.HasPrefix(relPath, "../") {
				continue
			}
			lineCoverage := byPath[relPath]
			if lineCoverage == nil {
				lineCoverage = map[int]bool{}
				byPath[relPath] = lineCoverage
			}
			for index, line := range lines {
				if line == nil {
					continue
				}
				lineNumber := index + 1
				if *line > 0 {
					lineCoverage[lineNumber] = true
				} else if _, exists := lineCoverage[lineNumber]; !exists {
					lineCoverage[lineNumber] = false
				}
			}
		}
	}
	files := map[string]*ProjectTestCoverageFile{}
	for path, lines := range byPath {
		lineNumbers := make([]int, 0, len(lines))
		for lineNumber := range lines {
			lineNumbers = append(lineNumbers, lineNumber)
		}
		sort.Ints(lineNumbers)
		file := &ProjectTestCoverageFile{Path: path, Changed: changedFiles[path]}
		for _, lineNumber := range lineNumbers {
			covered := lines[lineNumber]
			file.TotalLines++
			if covered {
				file.CoveredLines++
			}
			appendProjectTestCoverageLineSegment(file, lineNumber, covered)
		}
		files[path] = file
	}
	return finishCoverageFiles(files), nil
}

func parseSimpleCovLines(raw json.RawMessage) ([]*int, error) {
	var direct []*int
	if err := json.Unmarshal(raw, &direct); err == nil && direct != nil {
		return direct, nil
	}
	var wrapped struct {
		Lines []*int `json:"lines"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Lines, nil
}

func appendProjectTestCoverageLineSegment(file *ProjectTestCoverageFile, lineNumber int, covered bool) {
	if len(file.Segments) == 0 {
		file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: lineNumber, EndLine: lineNumber, Covered: covered})
		return
	}
	last := &file.Segments[len(file.Segments)-1]
	if last.Covered == covered && last.EndLine+1 == lineNumber {
		last.EndLine = lineNumber
		return
	}
	file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: lineNumber, EndLine: lineNumber, Covered: covered})
}

func finishCoverageFiles(byPath map[string]*ProjectTestCoverageFile) []ProjectTestCoverageFile {
	files := make([]ProjectTestCoverageFile, 0, len(byPath))
	for _, file := range byPath {
		if file.TotalLines > 0 {
			file.Percent = math.Round((float64(file.CoveredLines)/float64(file.TotalLines))*1000) / 10
		}
		files = append(files, *file)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Changed != files[j].Changed {
			return files[i].Changed
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func projectTestTargetFrameworks(discovery ProjectTestsDiscoveryResponse, requested string) []string {
	normalized := strings.TrimSpace(strings.ToLower(requested))
	if normalized != "" && normalized != "all" {
		return []string{normalized}
	}
	frameworks := make([]string, 0)
	for _, framework := range discovery.Frameworks {
		if framework.Available && framework.TestCount > 0 {
			frameworks = append(frameworks, framework.ID)
		}
	}
	return frameworks
}

func findProjectTestSelection(discovery ProjectTestsDiscoveryResponse, req ProjectTestsRunRequest) (projectTestSelection, bool) {
	testID := strings.TrimSpace(req.TestID)
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	for _, file := range discovery.TestFiles {
		if path != "" && file.Path == path && testID == "" {
			return projectTestSelection{File: file}, true
		}
		if testID == "" {
			continue
		}
		if node, ok := findProjectTestNode(file.Tests, testID); ok {
			return projectTestSelection{File: file, Node: node}, true
		}
	}
	if path != "" {
		for _, file := range discovery.TestFiles {
			if file.Path == path {
				return projectTestSelection{File: file}, true
			}
		}
	}
	return projectTestSelection{}, false
}

func findProjectTestNode(nodes []ProjectTestNode, id string) (ProjectTestNode, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
		if child, ok := findProjectTestNode(node.Children, id); ok {
			return child, true
		}
	}
	return ProjectTestNode{}, false
}

func branchProjectTestPaths(discovery ProjectTestsDiscoveryResponse, framework string) []string {
	paths := make([]string, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework == framework && file.BranchStatus != "D" {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

func branchProjectTestPackages(discovery ProjectTestsDiscoveryResponse, framework string) []string {
	seen := map[string]bool{}
	packages := make([]string, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != framework || file.BranchStatus == "D" {
			continue
		}
		pkg := goPackageFromTestPath(file.Path)
		if !seen[pkg] {
			seen[pkg] = true
			packages = append(packages, pkg)
		}
	}
	sort.Strings(packages)
	return packages
}

func branchGoCoverageTests(discovery ProjectTestsDiscoveryResponse) []projectTestSelection {
	selections := make([]projectTestSelection, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != projectTestFrameworkGo || file.BranchStatus == "D" {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if strings.HasPrefix(node.Name, "Test") {
				selections = append(selections, projectTestSelection{File: file, Node: node})
			}
		}
	}
	sort.SliceStable(selections, func(i, j int) bool {
		if selections[i].File.Path != selections[j].File.Path {
			return selections[i].File.Path < selections[j].File.Path
		}
		return selections[i].Node.Line < selections[j].Node.Line
	})
	return selections
}

func branchRSpecCoverageTests(discovery ProjectTestsDiscoveryResponse) []projectTestSelection {
	selections := make([]projectTestSelection, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != projectTestFrameworkRSpec || file.BranchStatus == "D" {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if node.Type == "test" && node.Line > 0 {
				selections = append(selections, projectTestSelection{File: file, Node: node})
			}
		}
	}
	sort.SliceStable(selections, func(i, j int) bool {
		if selections[i].File.Path != selections[j].File.Path {
			return selections[i].File.Path < selections[j].File.Path
		}
		return selections[i].Node.Line < selections[j].Node.Line
	})
	return selections
}

func flattenProjectTestNodes(nodes []ProjectTestNode) []ProjectTestNode {
	flat := make([]ProjectTestNode, 0)
	for _, node := range nodes {
		flat = append(flat, node)
		flat = append(flat, flattenProjectTestNodes(node.Children)...)
	}
	return flat
}

func countProjectTestNodes(nodes []ProjectTestNode) int {
	count := 0
	for _, node := range nodes {
		if node.Type == "test" {
			count++
		}
		count += countProjectTestNodes(node.Children)
	}
	return count
}

func changedCodeFileSet(discovery ProjectTestsDiscoveryResponse) map[string]bool {
	changed := map[string]bool{}
	for _, file := range discovery.BranchTestFiles {
		_ = file
	}
	if discovery.BranchChangesAvailable && projectHasGitMetadata(discovery.RootFolder) {
		target := projectGitBranchChangesTarget(discovery.RootFolder)
		files, err := loadProjectGitBranchChangedFiles(discovery.RootFolder, target)
		if err == nil {
			for _, file := range files {
				if classifyProjectTestFile(file.Path) == "" {
					changed[file.Path] = true
				}
			}
		}
	}
	return changed
}

func summarizeProjectTestResults(results []ProjectTestResult) ProjectTestRunSummary {
	summary := ProjectTestRunSummary{Total: len(results)}
	for _, result := range results {
		switch result.Status {
		case "passed":
			summary.Passed++
		case "skipped", "pending":
			summary.Skipped++
		default:
			summary.Failed++
		}
	}
	slowest := append([]ProjectTestResult{}, results...)
	sort.SliceStable(slowest, func(i, j int) bool {
		return slowest[i].DurationMs > slowest[j].DurationMs
	})
	if len(slowest) > 10 {
		slowest = slowest[:10]
	}
	summary.Slowest = slowest
	return summary
}

func sortProjectTestResults(results []ProjectTestResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return results[i].Path < results[j].Path
		}
		if results[i].Line != results[j].Line {
			return results[i].Line < results[j].Line
		}
		return results[i].FullName < results[j].FullName
	})
}

func projectGitAddedLineNumbers(repoRoot string, target projectGitBranchChangesTargetInfo, relPath string) map[int]bool {
	lines := map[int]bool{}
	if !target.Available {
		return lines
	}
	diff, err := runGitCommandPreserveLeading(repoRoot, "diff", "--unified=0", "--no-color", "--find-renames", target.BaseRef+"...HEAD", "--", relPath)
	if err != nil {
		return lines
	}
	currentLine := 0
	for _, line := range strings.Split(diff, "\n") {
		if matches := gitDiffHunkPattern.FindStringSubmatch(line); matches != nil {
			currentLine, _ = strconv.Atoi(matches[1])
			continue
		}
		if currentLine <= 0 {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			lines[currentLine] = true
			currentLine++
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue
		}
		currentLine++
	}
	return lines
}

func projectJestCommand(repoRoot string) (string, []string) {
	if projectTestUsesManagedRuntime(repoRoot, projectTestFrameworkJest, "npx") {
		return "npx", []string{"--no-install", "jest"}
	}
	local := filepath.Join(repoRoot, "node_modules", ".bin", "jest")
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, []string{}
	}
	return "npx", []string{"--no-install", "jest"}
}

func projectTestRuntimeCommand(repoRoot string, framework string, command string, args []string) (string, []string) {
	if !projectTestUsesManagedRuntime(repoRoot, framework, command) {
		return command, args
	}
	if projectTestCommandExists("mise") {
		wrappedArgs := append([]string{"exec", "--", command}, args...)
		return "mise", wrappedArgs
	}
	if projectTestCommandExists("asdf") {
		wrappedArgs := append([]string{"exec", command}, args...)
		return "asdf", wrappedArgs
	}
	if framework == projectTestFrameworkRSpec && projectTestCommandExists("rbenv") {
		wrappedArgs := append([]string{"exec", command}, args...)
		return "rbenv", wrappedArgs
	}
	return command, args
}

func projectTestUsesManagedRuntime(repoRoot string, framework string, command string) bool {
	if projectTestCommandExists("mise") && projectTestProjectDeclaresRuntime(repoRoot, framework, command) {
		return true
	}
	if projectTestCommandExists("asdf") && projectTestProjectDeclaresRuntime(repoRoot, framework, command) {
		return true
	}
	if framework == projectTestFrameworkRSpec && projectTestCommandExists("rbenv") && projectTestFileExists(repoRoot, ".ruby-version") {
		return true
	}
	return false
}

func projectTestProjectDeclaresRuntime(repoRoot string, framework string, command string) bool {
	switch framework {
	case projectTestFrameworkRSpec:
		return projectTestFileExists(repoRoot, ".ruby-version") || projectToolVersionsContains(repoRoot, "ruby")
	case projectTestFrameworkJest:
		return projectTestFileExists(repoRoot, ".nvmrc") ||
			projectTestFileExists(repoRoot, ".node-version") ||
			projectToolVersionsContains(repoRoot, "nodejs") ||
			projectToolVersionsContains(repoRoot, "node")
	case projectTestFrameworkGo:
		return projectToolVersionsContains(repoRoot, "golang") || projectToolVersionsContains(repoRoot, "go")
	}

	base := filepath.Base(command)
	if base == "bundle" || base == "rspec" || base == "ruby" {
		return projectTestFileExists(repoRoot, ".ruby-version") || projectToolVersionsContains(repoRoot, "ruby")
	}
	if base == "node" || base == "npm" || base == "npx" || base == "yarn" {
		return projectTestFileExists(repoRoot, ".nvmrc") ||
			projectTestFileExists(repoRoot, ".node-version") ||
			projectToolVersionsContains(repoRoot, "nodejs") ||
			projectToolVersionsContains(repoRoot, "node")
	}
	if base == "go" {
		return projectToolVersionsContains(repoRoot, "golang") || projectToolVersionsContains(repoRoot, "go")
	}
	return false
}

func projectToolVersionsContains(repoRoot string, toolName string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, ".tool-versions"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[0] == toolName {
			return true
		}
	}
	return false
}

func projectTestCommandExists(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func projectTestFileExists(repoRoot string, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	return err == nil && !info.IsDir()
}

func projectTestDirExists(repoRoot string, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	return err == nil && info.IsDir()
}

func projectTestFileContains(repoRoot string, relPath string, needle string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(content)), strings.ToLower(needle))
}

func projectTestPackageJSONHasJest(repoRoot string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(content))
	return strings.Contains(lower, "\"jest\"") || strings.Contains(lower, " jest ") || strings.Contains(lower, "jest --")
}

func shouldSkipProjectTestDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "tmp", "dist", "build", "coverage", ".next", ".turbo":
		return true
	default:
		return strings.HasPrefix(name, ".cache")
	}
}

func leadingProjectTestIndent(line string) int {
	count := 0
	for _, r := range line {
		switch r {
		case ' ':
			count++
		case '\t':
			count += 2
		default:
			return count
		}
	}
	return count
}

func firstNonEmptyProjectTestMatch(matches []string, start int) string {
	for i := start; i < len(matches); i++ {
		value := strings.TrimSpace(matches[i])
		if value != "" {
			return value
		}
	}
	return ""
}

func projectTestNodeID(framework string, path string, line int) string {
	return fmt.Sprintf("%s:%s:%d", framework, filepath.ToSlash(path), line)
}

func projectTestBranchScope(status string) string {
	switch status {
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R":
		return "renamed"
	default:
		return "changed"
	}
}

func goPackageFromTestPath(relPath string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relPath)))
	if dir == "." || dir == "" {
		return "."
	}
	return "./" + dir
}

func goTestRunPattern(node ProjectTestNode) string {
	parts := strings.Split(node.FullName, " / ")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		quoted = append(quoted, "^"+regexp.QuoteMeta(part)+"$")
	}
	if len(quoted) == 0 {
		return "^" + regexp.QuoteMeta(node.Name) + "$"
	}
	return strings.Join(quoted, "/")
}

func normalizeProjectTestStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pass", "passed", "success":
		return "passed"
	case "skip", "skipped", "pending", "todo", "disabled":
		return "skipped"
	default:
		return "failed"
	}
}

func nonEmptyProjectTestID(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func nonEmptyProjectTestName(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return strings.TrimSpace(primary)
	}
	return strings.TrimSpace(fallback)
}

func normalizeProjectTestResultPath(repoRoot string, raw string) string {
	trimmed := filepath.Clean(strings.TrimSpace(raw))
	if trimmed == "" || trimmed == "." {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		if rel, err := filepath.Rel(repoRoot, trimmed); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(trimmed)
}

func findGoTestPath(discovery ProjectTestsDiscoveryResponse, testName string) (string, int) {
	baseName := strings.Split(testName, "/")[0]
	for _, file := range discovery.TestFiles {
		if file.Framework != projectTestFrameworkGo {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if node.Name == baseName || node.FullName == strings.ReplaceAll(testName, "/", " / ") {
				return node.Path, node.Line
			}
		}
	}
	return "", 0
}

func truncateProjectTestOutput(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	return output[:limit] + "\n...[truncated]"
}

func runProjectTestQuickCommand(repoRoot string, command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimeCommand, runtimeArgs := projectTestRuntimeCommand(repoRoot, "", command, args)
	cmd := exec.CommandContext(ctx, runtimeCommand, runtimeArgs...)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func parseProjectTestCoverageLine(raw string) int {
	part := raw
	if dot := strings.Index(part, "."); dot >= 0 {
		part = part[:dot]
	}
	value, _ := strconv.Atoi(part)
	return value
}

func normalizeGoCoveragePath(repoRoot string, modulePath string, rawPath string) string {
	path := filepath.ToSlash(rawPath)
	if modulePath != "" && strings.HasPrefix(path, modulePath+"/") {
		return strings.TrimPrefix(path, modulePath+"/")
	}
	if filepath.IsAbs(path) {
		return normalizeProjectTestResultPath(repoRoot, path)
	}
	return path
}

func moveProjectCoverageDirAside(coverageDir string) (func() error, error) {
	if strings.TrimSpace(coverageDir) == "" || coverageDir == "." || coverageDir == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid coverage directory %q", coverageDir)
	}
	parent := filepath.Dir(coverageDir)
	backupDir := filepath.Join(parent, fmt.Sprintf(".a2gent-coverage-backup-%d", time.Now().UnixNano()))
	existed := false
	if info, err := os.Stat(coverageDir); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", coverageDir)
		}
		if err := os.Rename(coverageDir, backupDir); err != nil {
			return nil, err
		}
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return func() error {
		removeErr := os.RemoveAll(coverageDir)
		if existed {
			if err := os.Rename(backupDir, coverageDir); err != nil {
				if removeErr != nil {
					return fmt.Errorf("%v; %w", removeErr, err)
				}
				return err
			}
		}
		return removeErr
	}, nil
}

func coverageRefsFromFiles(files []ProjectTestCoverageFile) []ProjectTestCoverageFileRef {
	refs := make([]ProjectTestCoverageFileRef, 0, len(files))
	for _, file := range files {
		if file.CoveredLines == 0 {
			continue
		}
		refs = append(refs, ProjectTestCoverageFileRef{
			Path:         file.Path,
			Changed:      file.Changed,
			CoveredLines: file.CoveredLines,
			TotalLines:   file.TotalLines,
			Percent:      file.Percent,
			Segments:     file.Segments,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Changed != refs[j].Changed {
			return refs[i].Changed
		}
		if refs[i].Percent != refs[j].Percent {
			return refs[i].Percent > refs[j].Percent
		}
		return refs[i].Path < refs[j].Path
	})
	return refs
}

func coverageRefsFromChangedFiles(files []ProjectTestCoverageFile) []ProjectTestCoverageFileRef {
	changed := make([]ProjectTestCoverageFile, 0, len(files))
	for _, file := range files {
		if file.Changed {
			changed = append(changed, file)
		}
	}
	return coverageRefsFromFiles(changed)
}
