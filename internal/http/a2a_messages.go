package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/google/uuid"
)

func (s *Server) handleA2AMessageSend(w http.ResponseWriter, r *http.Request) {
	rawBody, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body: "+readErr.Error())
		return
	}

	response, err := s.processA2AMessageRequest(r.Context(), rawBody)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":  "completed",
		"message": response,
	})
}

func (s *Server) handleA2AMessageSendStream(w http.ResponseWriter, r *http.Request) {
	rawBody, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid request body: "+readErr.Error())
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

	response, err := s.processA2AMessageRequest(r.Context(), rawBody)
	if err != nil {
		_ = writeEvent("status", map[string]interface{}{"state": "failed", "error": err.Error()})
		return
	}
	_ = writeEvent("message", map[string]interface{}{
		"state":   "completed",
		"message": response,
	})
}

func (s *Server) processA2AMessageRequest(ctx context.Context, rawBody []byte) (*a2atunnel.OutboundPayload, error) {
	var req a2atunnel.InboundPayload
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	if len(req.Content) == 0 {
		return nil, fmt.Errorf("A2A messages endpoint requires canonical content[]")
	}
	if err := a2atunnel.ValidateA2AContent(req.Content); err != nil {
		return nil, fmt.Errorf("invalid content payload: %w", err)
	}

	derivedText, derivedImages := a2atunnel.LegacyFromA2AContent(req.Content)
	if strings.TrimSpace(req.Task) == "" {
		req.Task = strings.TrimSpace(derivedText)
	}
	if len(req.Images) == 0 {
		req.Images = derivedImages
	}
	if err := a2atunnel.ValidateA2AImages(req.Images); err != nil {
		return nil, fmt.Errorf("invalid images payload: %w", err)
	}
	if req.Sender != nil {
		if strings.TrimSpace(req.SourceAgentID) == "" {
			req.SourceAgentID = strings.TrimSpace(req.Sender.AgentID)
		}
		if strings.TrimSpace(req.SourceAgentName) == "" {
			req.SourceAgentName = strings.TrimSpace(req.Sender.Name)
		}
	}
	if strings.TrimSpace(req.A2AVersion) == "" {
		req.A2AVersion = a2atunnel.A2ABridgeVersion
	}
	if strings.TrimSpace(req.MessageID) == "" {
		req.MessageID = uuid.NewString()
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode canonical payload: %w", err)
	}

	handler := a2atunnel.NewInboundHandler(
		"brute",
		s.sessionManager,
		s.makeA2AAgentFactory(),
		s.toolManagerForSession,
		s.getA2AInboundProjectID,
		s.getA2AInboundSubAgentID,
	)
	respPayload, err := handler.Handle(ctx, &a2atunnel.AgentRequest{
		Kind:      a2atunnel.KindTask,
		RequestID: req.MessageID,
		Payload:   payload,
	})
	if err != nil {
		return nil, fmt.Errorf("A2A message handling failed: %w", err)
	}

	var response a2atunnel.OutboundPayload
	if err := json.Unmarshal(respPayload, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response payload: %w", err)
	}
	if len(response.Content) == 0 {
		response.Content = a2atunnel.BuildA2AContent(response.Result, response.Images)
	}
	if strings.TrimSpace(response.A2AVersion) == "" {
		response.A2AVersion = a2atunnel.A2ABridgeVersion
	}
	if strings.TrimSpace(response.MessageID) == "" {
		response.MessageID = uuid.NewString()
	}
	return &response, nil
}
