package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// Built-in identities are fixed so prompt edits cannot hijack reserved commands.
// Default prompt text stays in Caesar; SQLite stores only user overrides.
var builtInSessionTemplateIdentities = map[string]struct{ name, slashCommand string }{
	"built-in:branch-review:architecture":         {"Architecture review", "architecture-review"},
	"built-in:branch-review:quality":              {"Quality review", "quality-review"},
	"built-in:branch-review:security-performance": {"Security and performance review", "security-performance-review"},
	"built-in:branch-review:simplicity":           {"Simplicity review", "simplicity-review"},
	"built-in:branch-review:documentation":        {"Documentation review", "documentation-review"},
}

func (s *Server) handleUpdateBuiltInSessionTemplate(w http.ResponseWriter, r *http.Request, id string) {
	var req SessionTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	identity := builtInSessionTemplateIdentities[id]
	content := strings.TrimSpace(req.Content)
	if content == "" {
		s.errorResponse(w, http.StatusBadRequest, "Content is required")
		return
	}
	if strings.TrimSpace(req.Name) != identity.name || strings.TrimLeft(strings.ToLower(strings.TrimSpace(req.SlashCommand)), "/") != identity.slashCommand {
		s.errorResponse(w, http.StatusBadRequest, "Built-in template name and slash command cannot be changed")
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
		ID: id, Name: identity.name, SlashCommand: identity.slashCommand,
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
