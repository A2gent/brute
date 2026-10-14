package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type telegramTopicTestRequest struct {
	BotToken      string `json:"bot_token"`
	IntegrationID string `json:"integration_id,omitempty"`
	ChatID        string `json:"chat_id"`
}

// handleTelegramTestTopic proves the bot can really work with topics in a group: it creates a topic, posts a
// message inside it and removes the topic again, so the group is left as it was. Failures are expected outcomes
// (missing rights) and are reported in the body with advice, not as HTTP errors.
func (s *Server) handleTelegramTestTopic(w http.ResponseWriter, r *http.Request) {
	var req telegramTopicTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		s.errorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	botToken := s.resolveTelegramDiscoveryToken(TelegramChatDiscoveryRequest{BotToken: req.BotToken, IntegrationID: req.IntegrationID})
	if botToken == "" || botToken == "***" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s.jsonResponse(w, http.StatusOK, s.runTelegramTopicTest(ctx, botToken, chatID))
}

func (s *Server) runTelegramTopicTest(ctx context.Context, botToken, chatID string) IntegrationTestResponse {
	threadID, err := s.createTelegramForumTopic(ctx, botToken, chatID, "A2gent topic test")
	if err != nil {
		return IntegrationTestResponse{Success: false, Message: "The bot could not create a topic. " + telegramTopicFixAdvice(err)}
	}

	message := "Topic created, a message was posted inside it and the test topic was removed. Topics work."
	if err := s.sendTelegramMessage(ctx, botToken, chatID, threadID, "✅ Test message from A2gent inside a topic. This topic is deleted right away."); err != nil {
		message = "The bot created a topic but could not write into it: " + sanitizeTelegramError(err)
		_ = s.deleteTelegramForumTopic(ctx, botToken, chatID, threadID)
		return IntegrationTestResponse{Success: false, Message: message}
	}
	if err := s.deleteTelegramForumTopic(ctx, botToken, chatID, threadID); err != nil {
		return IntegrationTestResponse{
			Success: true,
			Message: fmt.Sprintf("Topics work, but the test topic could not be deleted (%s). You can delete \"A2gent topic test\" by hand.", sanitizeTelegramError(err)),
		}
	}
	return IntegrationTestResponse{Success: true, Message: message}
}
