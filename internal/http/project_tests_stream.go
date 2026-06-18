package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleProjectTestsRunStream(w http.ResponseWriter, r *http.Request) {
	var req ProjectTestsRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}

	writeEvent, ok := beginProjectTestsStream(w, s)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	writeEvent(ProjectTestsStreamEvent{Type: "run_created", Stage: "setup", Message: "Created test run."})
	writeEvent(ProjectTestsStreamEvent{Type: "discovery_started", Stage: "discovery", Message: "Discovering project tests."})
	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		writeEvent(ProjectTestsStreamEvent{Type: "error", Stage: "discovery", Error: "Failed to discover tests: " + err.Error()})
		return
	}
	writeEvent(ProjectTestsStreamEvent{Type: "discovery_completed", Stage: "discovery", Message: "Discovered project tests.", Discovery: &discovery})

	writeEvent(ProjectTestsStreamEvent{Type: "tests_started", Stage: "tests", Message: "Running selected tests."})
	response := executeProjectTestsWithObserver(ctx, targetRepoRoot, repoPath, discovery, req, projectTestsStageObserver("tests", writeEvent))
	writeEvent(ProjectTestsStreamEvent{Type: "tests_completed", Stage: "tests", Message: "Test run completed.", Run: &response})
	writeEvent(ProjectTestsStreamEvent{Type: "done", Stage: "complete", Message: "Test run finished."})
}

func (s *Server) handleProjectTestsBranchCacheRefreshStream(w http.ResponseWriter, r *http.Request) {
	var req ProjectTestsBranchCacheRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, ok := s.resolveProjectTestsTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}

	writeEvent, ok := beginProjectTestsStream(w, s)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	writeEvent(ProjectTestsStreamEvent{Type: "run_created", Stage: "setup", Message: "Created branch refresh run."})
	writeEvent(ProjectTestsStreamEvent{Type: "discovery_started", Stage: "discovery", Message: "Discovering branch tests."})
	discovery, err := buildProjectTestsDiscovery(projectID, repoPath, targetRepoRoot)
	if err != nil {
		writeEvent(ProjectTestsStreamEvent{Type: "error", Stage: "discovery", Error: "Failed to discover tests: " + err.Error()})
		return
	}
	writeEvent(ProjectTestsStreamEvent{Type: "discovery_completed", Stage: "discovery", Message: "Discovered branch tests.", Discovery: &discovery})

	writeEvent(ProjectTestsStreamEvent{Type: "tests_started", Stage: "tests", Message: "Running branch tests."})
	runResponse := executeProjectTestsWithObserver(ctx, targetRepoRoot, repoPath, discovery, ProjectTestsRunRequest{
		RepoPath:  repoPath,
		Framework: "all",
		Mode:      "branch",
	}, projectTestsStageObserver("tests", writeEvent))
	writeEvent(ProjectTestsStreamEvent{Type: "tests_completed", Stage: "tests", Message: "Branch tests completed.", Run: &runResponse})

	writeEvent(ProjectTestsStreamEvent{Type: "coverage_started", Stage: "coverage", Message: "Collecting branch coverage."})
	coverageResponse := buildProjectTestsCoverageWithObserver(ctx, targetRepoRoot, repoPath, discovery, ProjectTestsCoverageRequest{
		RepoPath:  repoPath,
		Framework: "all",
		Mode:      "branch",
	}, projectTestsStageObserver("coverage", writeEvent))
	writeEvent(ProjectTestsStreamEvent{Type: "coverage_completed", Stage: "coverage", Message: "Branch coverage completed.", Coverage: &coverageResponse})

	writeEvent(ProjectTestsStreamEvent{Type: "cache_save_started", Stage: "cache", Message: "Saving branch test cache."})
	response, err := s.saveProjectTestsBranchCache(projectID, repoPath, discovery, runResponse, coverageResponse)
	if err != nil {
		writeEvent(ProjectTestsStreamEvent{Type: "error", Stage: "cache", Error: err.Error()})
		return
	}
	writeEvent(ProjectTestsStreamEvent{Type: "cache_saved", Stage: "cache", Message: "Branch test cache saved.", Cache: &response})
	writeEvent(ProjectTestsStreamEvent{Type: "done", Stage: "complete", Message: "Branch refresh finished."})
}

func beginProjectTestsStream(w http.ResponseWriter, s *Server) (func(ProjectTestsStreamEvent) bool, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return nil, false
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var streamMu sync.Mutex
	streamWritable := true
	encoder := json.NewEncoder(w)
	writeEvent := func(event ProjectTestsStreamEvent) bool {
		streamMu.Lock()
		defer streamMu.Unlock()
		if !streamWritable {
			return false
		}
		if err := encoder.Encode(event); err != nil {
			streamWritable = false
			return false
		}
		flusher.Flush()
		return true
	}
	return writeEvent, true
}

func projectTestsStageObserver(stage string, writeEvent func(ProjectTestsStreamEvent) bool) projectTestRunObserver {
	return func(event ProjectTestsStreamEvent) {
		if event.Stage == "" {
			event.Stage = stage
		}
		writeEvent(event)
	}
}
