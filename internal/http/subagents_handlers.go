// subagents_handlers.go keeps sub-agent HTTP handlers focused without changing logic.
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

func (s *Server) subAgentToResponse(sa *storage.SubAgent) SubAgentResponse {
	tools := sa.EnabledTools
	if tools == nil {
		tools = []string{}
	}
	instrBlocks := sa.InstructionBlocks
	if instrBlocks == "" {
		instrBlocks = "[]"
	}
	projectID := ""
	if sa.ProjectID != nil {
		projectID = strings.TrimSpace(*sa.ProjectID)
	}
	return SubAgentResponse{
		ID:                sa.ID,
		Name:              sa.Name,
		ProjectID:         projectID,
		Provider:          sa.Provider,
		Model:             sa.Model,
		EnabledTools:      tools,
		InstructionBlocks: instrBlocks,
		CreatedAt:         sa.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         sa.UpdatedAt.Format(time.RFC3339),
	}
}

func (s *Server) handleListSubAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListSubAgents()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sub-agents: "+err.Error())
		return
	}

	resp := make([]SubAgentResponse, len(agents))
	for i, sa := range agents {
		resp[i] = s.subAgentToResponse(sa)
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) normalizeSubAgentProjectID(w http.ResponseWriter, raw string) (*string, bool) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return nil, true
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project not found: "+err.Error())
		return nil, false
	}
	return &projectID, true
}

func (s *Server) handleCreateSubAgent(w http.ResponseWriter, r *http.Request) {
	var req SubAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}
	projectID, ok := s.normalizeSubAgentProjectID(w, req.ProjectID)
	if !ok {
		return
	}

	now := time.Now()
	sa := &storage.SubAgent{
		ID:                uuid.New().String(),
		Name:              strings.TrimSpace(req.Name),
		ProjectID:         projectID,
		Provider:          strings.TrimSpace(req.Provider),
		Model:             strings.TrimSpace(req.Model),
		EnabledTools:      req.EnabledTools,
		InstructionBlocks: req.InstructionBlocks,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if sa.EnabledTools == nil {
		sa.EnabledTools = []string{}
	}

	if err := s.store.SaveSubAgent(sa); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create sub-agent: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, s.subAgentToResponse(sa))
}

func (s *Server) handleGetSubAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "subAgentID")
	sa, err := s.store.GetSubAgent(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Sub-agent not found: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.subAgentToResponse(sa))
}

func (s *Server) handleUpdateSubAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "subAgentID")
	sa, err := s.store.GetSubAgent(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Sub-agent not found: "+err.Error())
		return
	}

	var req SubAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name is required")
		return
	}

	projectID, ok := s.normalizeSubAgentProjectID(w, req.ProjectID)
	if !ok {
		return
	}

	sa.Name = strings.TrimSpace(req.Name)
	sa.ProjectID = projectID
	sa.Provider = strings.TrimSpace(req.Provider)
	sa.Model = strings.TrimSpace(req.Model)
	sa.EnabledTools = req.EnabledTools
	if sa.EnabledTools == nil {
		sa.EnabledTools = []string{}
	}
	sa.InstructionBlocks = req.InstructionBlocks
	sa.UpdatedAt = time.Now()

	if err := s.store.SaveSubAgent(sa); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update sub-agent: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.subAgentToResponse(sa))
}

func (s *Server) handleDeleteSubAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "subAgentID")
	if err := s.store.DeleteSubAgent(id); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete sub-agent: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleEstimateSubAgentInstructions returns a system prompt snapshot for an
// existing (saved) sub-agent, using instruction blocks from the request body.
func (s *Server) handleEstimateSubAgentInstructions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "subAgentID")
	sa, err := s.store.GetSubAgent(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Sub-agent not found: "+err.Error())
		return
	}

	var req struct {
		InstructionBlocks string   `json:"instruction_blocks"`
		Name              string   `json:"name"`
		EnabledTools      []string `json:"enabled_tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.InstructionBlocks != "" {
		sa.InstructionBlocks = req.InstructionBlocks
	}
	if req.Name != "" {
		sa.Name = req.Name
	}
	if req.EnabledTools != nil {
		sa.EnabledTools = req.EnabledTools
	}

	s.respondSubAgentEstimate(w, sa)
}

// handleEstimateSubAgentInstructionsDraft returns a system prompt snapshot for a
// draft (unsaved) sub-agent.
func (s *Server) handleEstimateSubAgentInstructionsDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstructionBlocks string   `json:"instruction_blocks"`
		Name              string   `json:"name"`
		EnabledTools      []string `json:"enabled_tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	sa := &storage.SubAgent{
		ID:                "draft",
		Name:              req.Name,
		InstructionBlocks: req.InstructionBlocks,
		EnabledTools:      req.EnabledTools,
	}
	if sa.Name == "" {
		sa.Name = "Draft Sub-Agent"
	}

	s.respondSubAgentEstimate(w, sa)
}

func (s *Server) respondSubAgentEstimate(w http.ResponseWriter, sa *storage.SubAgent) {
	snapshot := s.composeSubAgentSystemPromptSnapshot(sa, nil)
	if snapshot == nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to compose instruction snapshot")
		return
	}

	blocks := make([]SystemPromptBlockSnapshotPayload, len(snapshot.Blocks))
	for i, block := range snapshot.Blocks {
		blocks[i] = SystemPromptBlockSnapshotPayload{
			Type:            block.Type,
			Value:           block.Value,
			Enabled:         block.Enabled,
			ResolvedContent: block.ResolvedContent,
			SourcePath:      block.SourcePath,
			Error:           block.Error,
			EstimatedTokens: block.EstimatedTokens,
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"snapshot": SystemPromptSnapshotPayload{
			BasePrompt:        snapshot.BasePrompt,
			CombinedPrompt:    snapshot.CombinedPrompt,
			BaseEstimated:     snapshot.BaseEstimated,
			CombinedEstimated: snapshot.CombinedEstimated,
			Blocks:            blocks,
		},
	})
}
