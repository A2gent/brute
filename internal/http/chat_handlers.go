// chat_handlers.go keeps non-streaming chat request handling focused without changing behavior.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/a2atunnel"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	images, err := normalizeIncomingImages(req.Images)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid images payload: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Message) == "" && len(images) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Message or images are required")
		return
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)
	if sess.Status == session.StatusQueued && sessionIsSerialQueuedAutoRun(sess) {
		s.triggerSerialSessionQueueForSession(sess)
		s.errorResponse(w, http.StatusConflict, "Session is queued for serial execution and will start automatically")
		return
	}

	sess.AddUserMessageWithImages(req.Message, images)
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session: "+err.Error())
		return
	}

	runCtx, cancelRun := context.WithCancel(s.sessionRunParentContext())
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}()

	result, runErr := s.runSessionWithoutStreaming(runCtx, sess, req.Message)
	if finalizeErr := s.finalizeSessionRunWithoutStreaming(runCtx, sess, result, runErr); finalizeErr != nil {
		if runErr != nil {
			status, message := s.sessionRunHTTPError(result, runErr)
			s.errorResponse(w, status, message)
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, finalizeErr.Error())
		return
	}

	resp := ChatResponse{
		Content:  result.Content,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
		Usage: UsageResponse{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		},
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func normalizeIncomingImages(images []MessageImagePayload) ([]session.ImageAttachment, error) {
	if len(images) == 0 {
		return nil, nil
	}
	out := make([]session.ImageAttachment, 0, len(images))
	bridgeImages := make([]a2atunnel.A2AImage, 0, len(images))
	for idx, raw := range images {
		img := session.ImageAttachment{
			Name:       strings.TrimSpace(raw.Name),
			MediaType:  strings.TrimSpace(raw.MediaType),
			DataBase64: strings.TrimSpace(raw.DataBase64),
			URL:        strings.TrimSpace(raw.URL),
		}
		if img.DataBase64 == "" && img.URL == "" {
			return nil, fmt.Errorf("image %d has neither data_base64 nor url", idx+1)
		}
		if img.MediaType == "" && strings.HasPrefix(strings.ToLower(img.URL), "data:") {
			parts := strings.SplitN(img.URL, ";", 2)
			if len(parts) > 0 {
				img.MediaType = strings.TrimPrefix(parts[0], "data:")
			}
		}
		if img.MediaType == "" {
			img.MediaType = "image/png"
		}
		bridgeImages = append(bridgeImages, a2atunnel.A2AImage{
			Name:       img.Name,
			MediaType:  img.MediaType,
			DataBase64: img.DataBase64,
			URL:        img.URL,
		})
		out = append(out, img)
	}
	if err := a2atunnel.ValidateA2AImages(bridgeImages); err != nil {
		return nil, err
	}
	return out, nil
}

func lastUserMessageImages(sess *session.Session) []session.ImageAttachment {
	if sess == nil {
		return nil
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			return sess.Messages[i].Images
		}
	}
	return nil
}

func sameMessageImages(a, b []session.ImageAttachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].Name) != strings.TrimSpace(b[i].Name) {
			return false
		}
		if strings.TrimSpace(a[i].MediaType) != strings.TrimSpace(b[i].MediaType) {
			return false
		}
		if strings.TrimSpace(a[i].DataBase64) != strings.TrimSpace(b[i].DataBase64) {
			return false
		}
		if strings.TrimSpace(a[i].URL) != strings.TrimSpace(b[i].URL) {
			return false
		}
	}
	return true
}

func messageForRouting(text string, imageCount int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		return trimmed
	}
	if imageCount > 0 {
		return fmt.Sprintf("[User sent %d image(s)]", imageCount)
	}
	return ""
}
