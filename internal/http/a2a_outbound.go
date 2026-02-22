package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

type createA2AOutboundSessionRequest struct {
	TargetAgentID   string `json:"target_agent_id"`
	TargetAgentName string `json:"target_agent_name,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
}

func (s *Server) handleCreateA2AOutboundSession(w http.ResponseWriter, r *http.Request) {
	var req createA2AOutboundSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	targetAgentID := strings.TrimSpace(req.TargetAgentID)
	if targetAgentID == "" {
		s.errorResponse(w, http.StatusBadRequest, "target_agent_id is required")
		return
	}

	sess, err := s.sessionManager.Create("a2a")
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}

	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[a2aOutboundSessionKey] = true
	sess.Metadata[a2aOutboundTargetAgentIDKey] = targetAgentID
	sess.Metadata[a2aOutboundTargetAgentNameKey] = strings.TrimSpace(req.TargetAgentName)
	sess.Metadata[a2aConversationIDKey] = sess.ID
	if projectID := strings.TrimSpace(req.ProjectID); projectID != "" {
		sess.ProjectID = &projectID
	}
	sess.SetTitle(strings.TrimSpace(req.TargetAgentName))
	sess.SetStatus(session.StatusPaused)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to persist session: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, s.sessionToResponse(sess))
}

func (s *Server) handleA2AOutboundChat(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Message is required")
		return
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	isOutbound, targetAgentID, _ := sessionA2AOutboundMeta(sess)
	if !isOutbound || strings.TrimSpace(targetAgentID) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Session is not an A2A outbound session")
		return
	}

	s.tunnelMu.Lock()
	client := s.tunnelClient
	s.tunnelMu.Unlock()
	if client == nil || !client.IsConnected() {
		s.errorResponse(w, http.StatusConflict, "A2A tunnel is not connected")
		return
	}

	conversationID, _ := sess.Metadata[a2aConversationIDKey].(string)
	if strings.TrimSpace(conversationID) == "" {
		conversationID = sess.ID
		sess.Metadata[a2aConversationIDKey] = conversationID
	}
	sourceAgentName := s.localA2AAgentName()
	sourceAgentID := s.localA2ASourceAgentID()

	payload, err := json.Marshal(a2atunnel.CallPayload{
		Task:            message,
		SourceAgentID:   sourceAgentID,
		SourceAgentName: sourceAgentName,
		ConversationID:  conversationID,
		SourceSessionID: sess.ID,
	})
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to encode outbound payload: "+err.Error())
		return
	}

	sess.AddUserMessage(message)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session: "+err.Error())
		return
	}

	callCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	respPayload, err := client.Call(callCtx, strings.TrimSpace(targetAgentID), payload)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Remote call failed: %v", err), nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		s.errorResponse(w, http.StatusBadGateway, "Remote call failed: "+err.Error())
		return
	}

	result := decodeA2AOutboundResult(respPayload)
	sess.AddAssistantMessage(result, nil)
	sess.SetStatus(session.StatusCompleted)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to persist session response: "+err.Error())
		return
	}

	resp := ChatResponse{
		Content:  result,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func decodeA2AOutboundResult(payload []byte) string {
	var structured struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &structured); err == nil && strings.TrimSpace(structured.Result) != "" {
		return structured.Result
	}

	var text string
	if err := json.Unmarshal(payload, &text); err == nil && strings.TrimSpace(text) != "" {
		return text
	}

	trimmed := strings.TrimSpace(string(payload))
	if trimmed != "" {
		return trimmed
	}
	return "Remote agent returned an empty response."
}

func (s *Server) localA2AAgentName() string {
	settings, err := s.store.GetSettings()
	if err != nil {
		return defaultAgentName
	}
	name := strings.TrimSpace(settings[agentNameSettingKey])
	if name == "" {
		return defaultAgentName
	}
	return name
}

func (s *Server) localA2ASourceAgentID() string {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return "local-agent"
	}
	for _, integration := range integrations {
		if integration == nil || integration.Provider != "a2_registry" {
			continue
		}
		apiKey := strings.TrimSpace(integration.Config["api_key"])
		if apiKey == "" {
			continue
		}
		sum := sha256.Sum256([]byte(apiKey))
		return "local-" + hex.EncodeToString(sum[:8])
	}
	return "local-agent"
}
