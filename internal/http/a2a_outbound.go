package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
	rawBody, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+readErr.Error())
		return
	}

	resp, err := s.processA2AOutboundChat(r.Context(), sessionID, rawBody)
	if err != nil {
		s.errorResponse(w, err.status, err.message)
		return
	}
	s.jsonResponse(w, http.StatusOK, *resp)
}

func (s *Server) handleA2AOutboundChatStream(w http.ResponseWriter, r *http.Request) {
	rawBody, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+readErr.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(eventType string, payload interface{}) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeEvent("status", map[string]interface{}{"state": "accepted"}) {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	type streamResult struct {
		resp *ChatResponse
		err  *a2aChatHTTPError
	}
	done := make(chan streamResult, 1)
	go func() {
		resp, chatErr := s.processA2AOutboundChat(r.Context(), sessionID, rawBody)
		done <- streamResult{resp: resp, err: chatErr}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case result := <-done:
			if result.err != nil {
				_ = writeEvent("status", map[string]interface{}{"state": "failed", "error": result.err.message, "http_status": result.err.status})
				return
			}
			_ = writeEvent("response", *result.resp)
			return
		case <-ticker.C:
			if !writeEvent("status", map[string]interface{}{"state": "running"}) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

type a2aChatHTTPError struct {
	status  int
	message string
}

func (s *Server) processA2AOutboundChat(ctx context.Context, sessionID string, rawBody []byte) (*ChatResponse, *a2aChatHTTPError) {
	var req ChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusBadRequest, message: "Invalid request body: " + err.Error()}
	}
	message := strings.TrimSpace(req.Message)
	images, err := normalizeIncomingImages(req.Images)
	if err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusBadRequest, message: "Invalid images payload: " + err.Error()}
	}
	if message == "" && len(images) == 0 {
		return nil, &a2aChatHTTPError{status: http.StatusBadRequest, message: "Message or images are required"}
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusNotFound, message: "Session not found: " + err.Error()}
	}
	isOutbound, targetAgentID, _ := sessionA2AOutboundMeta(sess)
	if !isOutbound || strings.TrimSpace(targetAgentID) == "" {
		return nil, &a2aChatHTTPError{status: http.StatusBadRequest, message: "Session is not an A2A outbound session"}
	}

	s.tunnelMu.Lock()
	client := s.tunnelClient
	s.tunnelMu.Unlock()
	if client == nil || !client.IsConnected() {
		return nil, &a2aChatHTTPError{status: http.StatusConflict, message: "A2A tunnel is not connected"}
	}

	conversationID, _ := sess.Metadata[a2aConversationIDKey].(string)
	if strings.TrimSpace(conversationID) == "" {
		conversationID = sess.ID
		sess.Metadata[a2aConversationIDKey] = conversationID
	}
	sourceAgentName := s.localA2AAgentName()
	sourceAgentID := s.localA2ASourceAgentID()

	payload, err := json.Marshal(a2atunnel.CallPayload{
		A2AVersion:     a2atunnel.A2ABridgeVersion,
		MessageID:      uuid.NewString(),
		ConversationID: conversationID,
		Sender: &a2atunnel.A2AParty{
			AgentID: sourceAgentID,
			Name:    sourceAgentName,
		},
		Recipient: &a2atunnel.A2AParty{
			AgentID: strings.TrimSpace(targetAgentID),
		},
		Content: a2atunnel.BuildA2AContent(message, sessionImagesToA2A(images)),

		Task:            message,
		SourceAgentID:   sourceAgentID,
		SourceAgentName: sourceAgentName,
		SourceSessionID: sess.ID,
		Images:          sessionImagesToA2A(images),
	})
	if err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusInternalServerError, message: "Failed to encode outbound payload: " + err.Error()}
	}

	sess.AddUserMessageWithImages(message, images)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusInternalServerError, message: "Failed to update session: " + err.Error()}
	}

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	respPayload, err := client.Call(callCtx, strings.TrimSpace(targetAgentID), payload)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Remote call failed: %v", err), nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return nil, &a2aChatHTTPError{status: http.StatusBadGateway, message: "Remote call failed: " + err.Error()}
	}

	result, resultImages := decodeA2AOutboundResult(respPayload)
	sess.AddAssistantMessageWithImagesAndMetadata(result, resultImages, nil, nil)
	sess.SetStatus(session.StatusCompleted)
	if err := s.sessionManager.Save(sess); err != nil {
		return nil, &a2aChatHTTPError{status: http.StatusInternalServerError, message: "Failed to persist session response: " + err.Error()}
	}

	resp := ChatResponse{
		Content:  result,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
	}
	return &resp, nil
}

func decodeA2AOutboundResult(payload []byte) (string, []session.ImageAttachment) {
	var structured a2atunnel.OutboundPayload
	if err := json.Unmarshal(payload, &structured); err == nil {
		result := strings.TrimSpace(structured.Result)
		images := structured.Images
		if len(images) == 0 && len(structured.Content) > 0 {
			derivedText, derivedImages := a2atunnel.LegacyFromA2AContent(structured.Content)
			if result == "" {
				result = strings.TrimSpace(derivedText)
			}
			images = derivedImages
		}
		if result != "" || len(images) > 0 {
			return result, a2aImagesToSession(images)
		}
	}

	var text string
	if err := json.Unmarshal(payload, &text); err == nil && strings.TrimSpace(text) != "" {
		return text, nil
	}

	trimmed := strings.TrimSpace(string(payload))
	if trimmed != "" {
		return trimmed, nil
	}
	return "Remote agent returned an empty response.", nil
}

func sessionImagesToA2A(images []session.ImageAttachment) []a2atunnel.A2AImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]a2atunnel.A2AImage, 0, len(images))
	for _, img := range images {
		out = append(out, a2atunnel.A2AImage{
			Name:       strings.TrimSpace(img.Name),
			MediaType:  strings.TrimSpace(img.MediaType),
			DataBase64: strings.TrimSpace(img.DataBase64),
			URL:        strings.TrimSpace(img.URL),
		})
	}
	return out
}

func a2aImagesToSession(images []a2atunnel.A2AImage) []session.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]session.ImageAttachment, 0, len(images))
	for _, img := range images {
		out = append(out, session.ImageAttachment{
			Name:       strings.TrimSpace(img.Name),
			MediaType:  strings.TrimSpace(img.MediaType),
			DataBase64: strings.TrimSpace(img.DataBase64),
			URL:        strings.TrimSpace(img.URL),
		})
	}
	return out
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
