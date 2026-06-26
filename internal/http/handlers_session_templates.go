// handlers_session_templates.go keeps session-template handlers focused without changing behavior.
package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var sessionTemplateSlashCommandPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var reservedSessionTemplateSlashCommands = map[string]struct{}{
	// WHY: templates now become first-class composer slash commands. Rejecting
	// reserved names prevents a saved template from shadowing existing behavior.
	"template":                    {},
	"tpl":                         {},
	"queue":                       {},
	"run":                         {},
	"provider":                    {},
	"model":                       {},
	"workflow":                    {},
	"wf":                          {},
	"help":                        {},
	"h":                           {},
	"?":                           {},
	"fill":                        {},
	"f":                           {},
	"continue":                    {},
	"linked-continue":             {},
	"lc":                          {},
	"linked":                      {},
	"link":                        {},
	"new":                         {},
	"continuation":                {},
	"cont":                        {},
	"architecture-review":         {},
	"quality-review":              {},
	"security-performance-review": {},
	"simplicity-review":           {},
	"documentation-review":        {},
}

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

	name, content, slashCommand, ok := s.validateSessionTemplateRequest(w, req, "")
	if !ok {
		return
	}

	now := time.Now()
	template := &storage.SessionTemplate{
		ID:           uuid.New().String(),
		Name:         name,
		Content:      content,
		SlashCommand: slashCommand,
		CreatedAt:    now,
		UpdatedAt:    now,
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

	name, content, slashCommand, ok := s.validateSessionTemplateRequest(w, req, templateID)
	if !ok {
		return
	}

	template.Name = name
	template.Content = content
	template.SlashCommand = slashCommand
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

func (s *Server) validateSessionTemplateRequest(w http.ResponseWriter, req SessionTemplateRequest, currentTemplateID string) (string, string, string, bool) {
	name := strings.TrimSpace(req.Name)
	content := strings.TrimSpace(req.Content)
	slashCommand := strings.TrimLeft(strings.ToLower(strings.TrimSpace(req.SlashCommand)), "/")
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return "", "", "", false
	}
	if content == "" {
		s.errorResponse(w, http.StatusBadRequest, "Content is required")
		return "", "", "", false
	}
	if slashCommand == "" {
		s.errorResponse(w, http.StatusBadRequest, "Slash command is required")
		return "", "", "", false
	}
	if !sessionTemplateSlashCommandPattern.MatchString(slashCommand) {
		s.errorResponse(w, http.StatusBadRequest, "Slash command can use only lowercase letters, numbers, and hyphens")
		return "", "", "", false
	}
	if _, reserved := reservedSessionTemplateSlashCommands[slashCommand]; reserved {
		s.errorResponse(w, http.StatusBadRequest, "Slash command is reserved")
		return "", "", "", false
	}

	templates, err := s.store.ListSessionTemplates()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to validate session template: "+err.Error())
		return "", "", "", false
	}
	for _, template := range templates {
		if template.ID == currentTemplateID {
			continue
		}
		if strings.EqualFold(template.SlashCommand, slashCommand) {
			s.errorResponse(w, http.StatusBadRequest, "Slash command is already used by another template")
			return "", "", "", false
		}
	}
	return name, content, slashCommand, true
}

func (s *Server) sessionTemplateToResponse(template *storage.SessionTemplate) SessionTemplateResponse {
	return SessionTemplateResponse{
		ID:           template.ID,
		Name:         template.Name,
		Content:      template.Content,
		SlashCommand: template.SlashCommand,
		CreatedAt:    template.CreatedAt,
		UpdatedAt:    template.UpdatedAt,
	}
}
