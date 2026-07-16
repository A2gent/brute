// handlers_projects.go keeps project CRUD handlers isolated from unrelated server concerns.
package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list projects: "+err.Error())
		return
	}

	resp := make([]ProjectResponse, len(projects))
	for i, project := range projects {
		resp[i] = projectToResponse(project)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project name is required")
		return
	}
	urlPatterns, err := normalizeProjectURLPatterns(req.URLPatterns)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	project := &storage.Project{
		ID:          uuid.New().String(),
		Name:        name,
		Folder:      normalizeFolder(req.Folder),
		Settings:    normalizeProjectSettings(req.Settings),
		URLPatterns: urlPatterns,
		IsSystem:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.store.SaveProject(project); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, projectToResponse(project))
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectToResponse(project))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found: "+err.Error())
		return
	}

	var req UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			s.errorResponse(w, http.StatusBadRequest, "Project name cannot be empty")
			return
		}
		project.Name = name
	}
	if req.Folder != nil {
		if project.ID == storage.SystemProjectSoulID {
			s.errorResponse(w, http.StatusBadRequest, "Soul project folder is tied to the agent data path")
			return
		}
		project.Folder = normalizeFolder(req.Folder)
	}
	if req.Settings != nil {
		previousSettings := copyProjectSettings(project.Settings)
		project.Settings = normalizeProjectSettings(*req.Settings)
		s.syncProjectSearchIndexAfterSettingsChange(project, previousSettings)
	}
	if req.URLPatterns != nil {
		urlPatterns, err := normalizeProjectURLPatterns(*req.URLPatterns)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		project.URLPatterns = urlPatterns
	}
	project.UpdatedAt = time.Now()

	if err := s.store.SaveProject(project); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, projectToResponse(project))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	if err := s.store.DeleteProject(projectID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete project: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func projectToResponse(project *storage.Project) ProjectResponse {
	if project == nil {
		return ProjectResponse{}
	}

	return ProjectResponse{
		ID:          project.ID,
		Name:        project.Name,
		Folder:      project.Folder,
		Settings:    normalizeProjectSettings(project.Settings),
		URLPatterns: normalizeProjectURLPatternsForResponse(project.URLPatterns),
		IsSystem:    project.IsSystem,
		CreatedAt:   project.CreatedAt,
		UpdatedAt:   project.UpdatedAt,
	}
}
