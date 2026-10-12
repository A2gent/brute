package http

import (
	"net/http"
	"path/filepath"
	"strings"
)

// handleProjectGitHeadFileRaw serves a previewable file as committed at HEAD.
// WHY: the Implementation diff shows the previous version of a changed .glb next to the
// working-tree one, and git diff cannot render binary models.
func (s *Server) handleProjectGitHeadFileRaw(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if projectID == "" || relPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID and path are required")
		return
	}
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	repoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(repoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}
	normalizedPath, err := resolveGitRepoFilePath(repoRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !isProjectRawPreviewFile(normalizedPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only PDF, image, GLB, and HTML files can be previewed")
		return
	}

	// git show needs forward slashes regardless of OS.
	content, err := gitCommand(repoRoot, "show", "HEAD:"+filepath.ToSlash(normalizedPath)).Output()
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "File not found in HEAD")
		return
	}
	w.Header().Set("Content-Type", projectRawPreviewContentType(normalizedPath))
	w.Header().Set("Cache-Control", "no-cache")
	if isHTMLFile(normalizedPath) {
		w.Header().Set("Content-Security-Policy", projectHTMLPreviewCSP)
	}
	_, _ = w.Write(content)
}
