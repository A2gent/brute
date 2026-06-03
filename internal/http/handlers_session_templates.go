// handlers_session_templates.go keeps session-template handlers focused without changing behavior.
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

func (s *Server) handleListSessionTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := s.store.ListSessionTemplates()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list session templates: "+err.Error())
		return
	}

	resp := make([]SessionTemplateResponse, len(templates))
	for i, template := range templates {
		resp[i] = s.sessionTemplateToResponse(template)
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateSessionTemplate(w http.ResponseWriter, r *http.Request) {
	var req SessionTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	content := strings.TrimSpace(req.Content)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}
	if content == "" {
		s.errorResponse(w, http.StatusBadRequest, "Content is required")
		return
	}

	now := time.Now()
	template := &storage.SessionTemplate{
		ID:        uuid.New().String(),
		Name:      name,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveSessionTemplate(template); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create session template: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusCreated, s.sessionTemplateToResponse(template))
}

func (s *Server) handleGetSessionTemplate(w http.ResponseWriter, r *http.Request) {
	templateID := chi.URLParam(r, "templateID")
	template, err := s.store.GetSessionTemplate(templateID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session template not found: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionTemplateToResponse(template))
}

func (s *Server) handleUpdateSessionTemplate(w http.ResponseWriter, r *http.Request) {
	templateID := chi.URLParam(r, "templateID")
	template, err := s.store.GetSessionTemplate(templateID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session template not found: "+err.Error())
		return
	}

	var req SessionTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	content := strings.TrimSpace(req.Content)
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}
	if content == "" {
		s.errorResponse(w, http.StatusBadRequest, "Content is required")
		return
	}

	template.Name = name
	template.Content = content
	template.UpdatedAt = time.Now()
	if err := s.store.SaveSessionTemplate(template); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session template: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionTemplateToResponse(template))
}

func (s *Server) handleDeleteSessionTemplate(w http.ResponseWriter, r *http.Request) {
	templateID := chi.URLParam(r, "templateID")
	if err := s.store.DeleteSessionTemplate(templateID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete session template: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sessionTemplateToResponse(template *storage.SessionTemplate) SessionTemplateResponse {
	return SessionTemplateResponse{
		ID:        template.ID,
		Name:      template.Name,
		Content:   template.Content,
		CreatedAt: template.CreatedAt,
		UpdatedAt: template.UpdatedAt,
	}
}
