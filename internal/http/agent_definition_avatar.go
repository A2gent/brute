package http

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/go-chi/chi/v5"
)

// Local agent avatars live next to agent.yaml as avatar.<ext>. A file
// convention (instead of writing agent.avatar_url) lets agents and users set an
// avatar by just dropping an image into the definition folder, and keeps
// agent.avatar_url free for public URLs used when publishing to the registry.
var agentAvatarExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

var agentAvatarContentTypeExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

const maxAgentAvatarBytes = 5 << 20

func agentDefinitionAvatarFile(def *agentdef.Definition) string {
	if def == nil {
		return ""
	}
	dir := strings.TrimSpace(def.Local.DefinitionDir)
	if dir == "" {
		return ""
	}
	for _, ext := range agentAvatarExtensions {
		path := filepath.Join(dir, "avatar"+ext)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// agentAvatarAPIPath returns the API-relative avatar URL; the mtime query busts
// browser caches after an avatar is replaced.
func agentAvatarAPIPath(def *agentdef.Definition, agentID string, projectID string) string {
	path := agentDefinitionAvatarFile(def)
	if path == "" {
		return ""
	}
	query := url.Values{}
	if info, err := os.Stat(path); err == nil {
		query.Set("v", fmt.Sprintf("%d", info.ModTime().Unix()))
	}
	if strings.TrimSpace(projectID) != "" {
		query.Set("project_id", strings.TrimSpace(projectID))
	}
	return "/unified-agents/" + url.PathEscape(agentID) + "/avatar?" + query.Encode()
}

func (s *Server) avatarDefinitionFromRequest(w http.ResponseWriter, r *http.Request) (*agentdef.Definition, string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "agentDefID"))
	projectID, _ := unifiedAgentsProjectFilter(r)
	def, discoveredProjectID, err := s.definitionForUnifiedAgent(id, projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, err.Error())
		return nil, "", false
	}
	if projectID == "" {
		projectID = discoveredProjectID
	}
	return def, projectID, true
}

func (s *Server) handleGetAgentAvatar(w http.ResponseWriter, r *http.Request) {
	def, _, ok := s.avatarDefinitionFromRequest(w, r)
	if !ok {
		return
	}
	path := agentDefinitionAvatarFile(def)
	if path == "" {
		s.errorResponse(w, http.StatusNotFound, "Agent has no avatar")
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleUploadAgentAvatar(w http.ResponseWriter, r *http.Request) {
	def, projectID, ok := s.avatarDefinitionFromRequest(w, r)
	if !ok {
		return
	}
	dir := strings.TrimSpace(def.Local.DefinitionDir)
	if dir == "" {
		s.errorResponse(w, http.StatusBadRequest, "Agent definition has no local folder for an avatar")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxAgentAvatarBytes+1))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read avatar: "+err.Error())
		return
	}
	if len(data) > maxAgentAvatarBytes {
		s.errorResponse(w, http.StatusRequestEntityTooLarge, "Avatar must be at most 5 MB")
		return
	}
	// Sniff the bytes instead of trusting Content-Type or a file name.
	ext, supported := agentAvatarContentTypeExtensions[http.DetectContentType(data)]
	if !supported {
		s.errorResponse(w, http.StatusBadRequest, "Avatar must be a PNG, JPEG, WebP or GIF image")
		return
	}
	removeAgentAvatarFiles(dir)
	if err := os.WriteFile(filepath.Join(dir, "avatar"+ext), data, 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save avatar: "+err.Error())
		return
	}
	s.publishProjectResourcesChanged("agents", projectID)
	s.jsonResponse(w, http.StatusOK, map[string]string{"avatar_url": agentAvatarAPIPath(def, strings.TrimSpace(chi.URLParam(r, "agentDefID")), projectID)})
}

func (s *Server) handleDeleteAgentAvatar(w http.ResponseWriter, r *http.Request) {
	def, projectID, ok := s.avatarDefinitionFromRequest(w, r)
	if !ok {
		return
	}
	if dir := strings.TrimSpace(def.Local.DefinitionDir); dir != "" {
		removeAgentAvatarFiles(dir)
	}
	s.publishProjectResourcesChanged("agents", projectID)
	w.WriteHeader(http.StatusNoContent)
}

func removeAgentAvatarFiles(dir string) {
	for _, ext := range agentAvatarExtensions {
		_ = os.Remove(filepath.Join(dir, "avatar"+ext))
	}
}
