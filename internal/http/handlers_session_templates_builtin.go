package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// Built-in names are fixed; their prompt text and agent can be overridden.
// Default prompt text stays in Caesar; SQLite stores only user overrides.
var builtInSessionTemplateIdentities = map[string]string{
	"built-in:branch-review:architecture":         "Architecture review",
	"built-in:branch-review:quality":              "Quality review",
	"built-in:branch-review:security-performance": "Security and performance review",
	"built-in:branch-review:simplicity":           "Simplicity review",
	"built-in:branch-review:documentation":        "Documentation review",
	"built-in:project-audit:dependencies":         "Dependency security audit",
	"built-in:project-audit:secrets":              "Secret exposure audit",
	"built-in:project-audit:access-control":       "Access control audit",
	"built-in:project-audit:test-coverage":        "Test coverage audit",
	"built-in:project-audit:ci":                   "CI health audit",
}

func (s *Server) handleUpdateBuiltInSessionTemplate(w http.ResponseWriter, r *http.Request, id string) {
	var req SessionTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	name, content, agentID, ok := s.validateSessionTemplateRequest(w, req)
	if !ok {
		return
	}
	if name != builtInSessionTemplateIdentities[id] {
		s.errorResponse(w, http.StatusBadRequest, "Built-in template name cannot be changed")
		return
	}

	// Use the list query to distinguish a missing override from a storage failure.
	// The first edit creates the override; subsequent edits retain its creation time.
	templates, err := s.store.ListSessionTemplates()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load session templates: "+err.Error())
		return
	}
	now := time.Now()
	template := &storage.SessionTemplate{
		ID: id, Name: name, AgentID: agentID,
		Content: content, CreatedAt: now, UpdatedAt: now,
	}
	for _, existing := range templates {
		if existing.ID == id {
			template.CreatedAt = existing.CreatedAt
			break
		}
	}
	if err := s.store.SaveSessionTemplate(template); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session template: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.sessionTemplateToResponse(template))
}
