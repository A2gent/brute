package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/filesearch"
)

const projectSearchMaxResults = 5

const projectSearchMaxFileResults = 20

func (s *Server) handleProjectSearch(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}
	if query == "" {
		s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
			RootFolder:      resolvedRoot,
			Query:           "",
			FileNameMatches: []ProjectFileNameMatch{},
			ContentMatches:  []ProjectContentMatch{},
		})
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	includeContent := mode != "files"
	searchResult, err := filesearch.DefaultManager().Search(r.Context(), resolvedRoot, filesearch.SearchRequest{
		Query:          query,
		FileLimit:      projectSearchMaxFileResults,
		ContentLimit:   projectSearchMaxResults,
		IncludeContent: includeContent,
	}, s.resolveProjectFileIndexingEnabled(project))
	if err != nil {
		if errors.Is(err, filesearch.ErrIndexingDisabled) {
			s.errorResponse(w, http.StatusConflict, "File indexing is disabled for this project. Enable it in Project Settings to use indexed quick search.")
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "Failed to search project files: "+err.Error())
		return
	}

	fileNameMatches := make([]ProjectFileNameMatch, 0, len(searchResult.FileMatches))
	for _, match := range searchResult.FileMatches {
		fileNameMatches = append(fileNameMatches, ProjectFileNameMatch{
			Path: match.Path,
			Name: match.Name,
		})
	}

	contentMatches := make([]ProjectContentMatch, 0, len(searchResult.ContentMatches))
	for _, match := range searchResult.ContentMatches {
		contentMatches = append(contentMatches, ProjectContentMatch{
			Path:    match.Path,
			Line:    match.Line,
			Preview: match.Preview,
		})
	}

	s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
		RootFolder:      resolvedRoot,
		Query:           query,
		FileNameMatches: fileNameMatches,
		ContentMatches:  contentMatches,
	})
}

func (s *Server) handleListProjectRecentFiles(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	limit := 5
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			s.errorResponse(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsedLimit > 20 {
			parsedLimit = 20
		}
		limit = parsedLimit
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	recentFiles, err := filesearch.RecentFiles(r.Context(), resolvedRoot, limit, filesearch.Options{})
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list recent project files: "+err.Error())
		return
	}

	files := make([]ProjectRecentFile, 0, len(recentFiles))
	for _, file := range recentFiles {
		files = append(files, ProjectRecentFile{
			Path:       file.Path,
			Name:       file.Name,
			ModifiedAt: file.ModifiedAt.UTC().Format(time.RFC3339),
		})
	}

	s.jsonResponse(w, http.StatusOK, ProjectRecentFilesResponse{
		RootFolder: resolvedRoot,
		Files:      files,
	})
}

