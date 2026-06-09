package http

import (
	"net/http"
	"strings"

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
	})
	if err != nil {
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

func warmProjectSearchIndex(resolvedRoot string) {
	filesearch.DefaultManager().Warm(resolvedRoot)
}

func invalidateProjectSearchIndex(resolvedRoot string) {
	filesearch.DefaultManager().Invalidate(resolvedRoot)
}
