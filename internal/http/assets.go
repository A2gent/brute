package http

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxImageAssetBytes = 25 * 1024 * 1024

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
