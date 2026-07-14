package http

// HTTP handlers for provider-agnostic integration CRUD operations.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list integrations: "+err.Error())
		return
	}

	resp := make([]IntegrationResponse, len(integrations))
	for i, integration := range integrations {
		resp[i] = integrationToResponse(integration)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	integration, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	integration.ID = uuid.New().String()
	integration.CreatedAt = now
	integration.UpdatedAt = now

	if err := s.store.SaveIntegration(integration); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save integration: "+err.Error())
		return
	}

	s.reconcileA2ATunnelAfterIntegrationSave(integration.Provider)
	s.jsonResponse(w, http.StatusCreated, integrationToResponse(integration))
}

func (s *Server) handleGetIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	integration, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, integrationToResponse(integration))
}

func (s *Server) handleUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	existing, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	// Caesar receives masked secrets. Preserve their stored values when an edit leaves
	// the mask untouched instead of replacing usable credentials with "***".
	for key, value := range req.Config {
		if value == "***" && isSensitiveIntegrationConfigKey(key) {
			req.Config[key] = existing.Config[key]
		}
	}

	next, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	next.ID = existing.ID
	next.CreatedAt = existing.CreatedAt
	next.UpdatedAt = time.Now()

	if err := s.store.SaveIntegration(next); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update integration: "+err.Error())
		return
	}

	s.reconcileA2ATunnelAfterIntegrationSave(next.Provider)
	s.jsonResponse(w, http.StatusOK, integrationToResponse(next))
}

func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	// Capture provider before deleting so we can reconcile the tunnel.
	var deletedProvider string
	if existing, err := s.store.GetIntegration(integrationID); err == nil && existing != nil {
		deletedProvider = existing.Provider
	}

	if err := s.store.DeleteIntegration(integrationID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete integration: "+err.Error())
		return
	}

	s.reconcileA2ATunnelAfterIntegrationSave(deletedProvider)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	integration, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	if err := validateIntegration(*integration); err != nil {
		s.jsonResponse(w, http.StatusBadRequest, IntegrationTestResponse{Success: false, Message: err.Error()})
		return
	}

	if integration.Provider == "telegram" {
		ok, message := s.testTelegramIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "jira" {
		ok, message := s.testJiraIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "circleci" {
		ok, message := s.testCircleCIIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "bitbucket" {
		ok, message := s.testBitbucketIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "appsignal" {
		ok, message := s.testAppSignalIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}

	s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: true, Message: "Configuration is valid. Live provider connectivity checks are not yet implemented."})
}
