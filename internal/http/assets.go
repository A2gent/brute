package http

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxImageAssetBytes     = 25 * 1024 * 1024
	maxGeneratedAssetBytes = int64(2 * 1024 * 1024 * 1024)
)

func (s *Server) handleGetImageAsset(w http.ResponseWriter, r *http.Request) {
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid image path")
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "image file not found")
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "path points to a directory")
		return
	}
	if info.Size() <= 0 {
		s.errorResponse(w, http.StatusBadRequest, "image file is empty")
		return
	}
	if info.Size() > maxImageAssetBytes {
		s.errorResponse(w, http.StatusBadRequest, "image file is too large")
		return
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to read image file")
		return
	}

	contentType := strings.TrimSpace(http.DetectContentType(content))
	if !strings.HasPrefix(contentType, "image/") {
		s.errorResponse(w, http.StatusBadRequest, "file is not a supported image")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleGetGeneratedAsset(w http.ResponseWriter, r *http.Request) {
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	if s == nil || s.config == nil || strings.TrimSpace(s.config.DataPath) == "" {
		s.errorResponse(w, http.StatusInternalServerError, "generated asset storage is unavailable")
		return
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid generated asset path")
		return
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "generated asset not found")
		return
	}
	if !s.generatedAssetPathAllowed(resolvedPath) {
		s.errorResponse(w, http.StatusForbidden, "generated asset path is outside the generated files folder")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "generated asset not found")
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "path points to a directory")
		return
	}
	if info.Size() <= 0 {
		s.errorResponse(w, http.StatusBadRequest, "generated asset is empty")
		return
	}
	if info.Size() > maxGeneratedAssetBytes {
		s.errorResponse(w, http.StatusRequestEntityTooLarge, "generated asset is too large")
		return
	}

	contentType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(absPath))))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	filename := strings.ReplaceAll(filepath.Base(absPath), `"`, "")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, resolvedPath)
}

func (s *Server) generatedAssetPathAllowed(candidate string) bool {
	roots := []string{filepath.Join(strings.TrimSpace(s.config.DataPath), "generated")}
	if workDir := strings.TrimSpace(s.config.WorkDir); workDir != "" {
		roots = append(roots, filepath.Join(workDir, "generated"))
	}
	if s.store != nil {
		if projects, err := s.store.ListProjects(); err == nil {
			for _, project := range projects {
				if project != nil && project.Folder != nil && strings.TrimSpace(*project.Folder) != "" {
					roots = append(roots, filepath.Join(strings.TrimSpace(*project.Folder), "generated"))
				}
			}
		}
	}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err == nil && pathWithinRoot(absRoot, candidate) {
			return true
		}
	}
	return false
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
