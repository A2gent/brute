package http

// HTTP handlers for provider-agnostic integration CRUD operations.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
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

	if isGoogleOAuthProvider(strings.ToLower(strings.TrimSpace(req.Provider))) {
		if err := s.applyGoogleClientReuse(&req); err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	integration, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.ProjectID != nil {
		projectID, err := s.validateIntegrationProjectID(*req.ProjectID)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		integration.ProjectID = projectID
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

	if req.Provider == "telegram_user" {
		// Login-managed state is never edited through the settings form.
		if req.Config == nil {
			req.Config = map[string]string{}
		}
		for _, key := range []string{"session", "cursors"} {
			req.Config[key] = existing.Config[key]
		}
	}

	if isGoogleOAuthProvider(req.Provider) {
		if err := s.applyGoogleClientReuse(&req); err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		// OAuth-managed state is written by the sign-in flow, never by the settings form.
		if req.Config == nil {
			req.Config = map[string]string{}
		}
		for _, key := range googleOAuthKeys {
			req.Config[key] = existing.Config[key]
		}
		if req.Config["client_id"] != existing.Config["client_id"] {
			// A different OAuth client invalidates the stored authorization.
			for _, key := range googleOAuthKeys {
				delete(req.Config, key)
			}
		}
	}

	next, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	next.ProjectID = existing.ProjectID
	if req.ProjectID != nil {
		projectID, err := s.validateIntegrationProjectID(*req.ProjectID)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		next.ProjectID = projectID
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

	s.respondIntegrationTest(w, r, integration)
}

// handleTestIntegrationDraft tests an integration from the unsaved create/edit form.
// When integration_id is given, masked secrets ("***") are resolved from the stored record.
func (s *Server) handleTestIntegrationDraft(w http.ResponseWriter, r *http.Request) {
	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if id := strings.TrimSpace(r.URL.Query().Get("integration_id")); id != "" {
		if existing, err := s.store.GetIntegration(id); err == nil && existing != nil {
			for key, value := range req.Config {
				if value == "***" && isSensitiveIntegrationConfigKey(key) {
					req.Config[key] = existing.Config[key]
				}
			}
		}
	}
	integration, err := newIntegrationFromRequest(req)
	if err != nil {
		s.jsonResponse(w, http.StatusBadRequest, IntegrationTestResponse{Success: false, Message: err.Error()})
		return
	}
	s.respondIntegrationTest(w, r, integration)
}

func (s *Server) respondIntegrationTest(w http.ResponseWriter, r *http.Request, integration *storage.Integration) {
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
	if integration.Provider == "telegram_user" {
		ok, message := s.testTelegramUserIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "google_calendar" {
		ok, message := s.testGoogleCalendarIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "gmail" {
		ok, message := s.testGmailIntegration(r.Context(), integration)
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
	if integration.Provider == "sentry" {
		ok, message := s.testSentryIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "cloudflare" {
		ok, message := s.testCloudflareIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "blender" {
		ok, message := s.testBlenderIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "meshy" || integration.Provider == "tripo" {
		ok, message := s.testModel3DIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}
	if integration.Provider == "comfyui" {
		ok, message := s.testComfyUIIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}

	s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: true, Message: "Configuration is valid. Live provider connectivity checks are not yet implemented."})
}

// validateIntegrationProjectID normalizes a requested integration project
// binding: "" means global, anything else must be an existing project.
func (s *Server) validateIntegrationProjectID(raw string) (string, error) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return "", nil
	}
	if project, err := s.store.GetProject(projectID); err != nil || project == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	return projectID, nil
}
