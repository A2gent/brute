package http

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleGetProjectFilePreview serves a project file at a path URL so an HTML iframe can load sibling assets.
func (s *Server) handleGetProjectFilePreview(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}
	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	relPath := strings.TrimPrefix(strings.TrimSpace(chi.URLParam(r, "*")), "/")
	resolvedPath, normalizedRelPath, err := resolveProjectPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	// WHY: a sandboxed iframe has an opaque origin, so ES modules and fetch() need CORS.
	// HTML also gets a sandbox CSP so opening the preview URL directly cannot act as the API origin.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", projectPreviewContentType(normalizedRelPath))
	if isHTMLFile(normalizedRelPath) {
		w.Header().Set("Content-Security-Policy", projectHTMLPreviewCSP)
	}
	// WHY: ServeFile redirects any URL ending in /index.html to ./, which breaks relative assets.
	file, err := os.Open(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to open file: "+err.Error())
		return
	}
	defer file.Close()
	http.ServeContent(w, r, filepath.Base(normalizedRelPath), info.ModTime(), file)
}
