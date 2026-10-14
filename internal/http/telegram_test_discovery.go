package http

// Telegram connectivity tests and recent-chat discovery endpoints.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) testTelegramIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "integration is nil"
	}
	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return false, "missing bot_token"
	}

	client := &http.Client{Timeout: 15 * time.Second}

	getMeReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getMe", botToken),
		nil,
	)
	if err != nil {
		return false, "failed to build Telegram getMe request: " + err.Error()
	}
	getMeResp, err := client.Do(getMeReq)
	if err != nil {
		return false, "failed to call Telegram getMe: " + sanitizeTelegramError(err)
	}
	defer getMeResp.Body.Close()

	var getMe telegramGetMePayload
	if err := json.NewDecoder(getMeResp.Body).Decode(&getMe); err != nil {
		return false, "failed to decode Telegram getMe response: " + err.Error()
	}
	if getMeResp.StatusCode != http.StatusOK || !getMe.OK {
		msg := strings.TrimSpace(getMe.Description)
		if msg == "" {
			msg = getMeResp.Status
		}
		return false, "Telegram getMe failed: " + msg
	}

	webhookReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getWebhookInfo", botToken),
		nil,
	)
	if err != nil {
		return false, "failed to build Telegram getWebhookInfo request: " + err.Error()
	}
	webhookResp, err := client.Do(webhookReq)
	if err != nil {
		return false, "failed to call Telegram getWebhookInfo: " + sanitizeTelegramError(err)
	}
	defer webhookResp.Body.Close()

	var webhookInfo telegramWebhookInfoPayload
	if err := json.NewDecoder(webhookResp.Body).Decode(&webhookInfo); err != nil {
		return false, "failed to decode Telegram webhook response: " + err.Error()
	}
	if webhookResp.StatusCode != http.StatusOK || !webhookInfo.OK {
		msg := strings.TrimSpace(webhookInfo.Description)
		if msg == "" {
			msg = webhookResp.Status
		}
		return false, "Telegram getWebhookInfo failed: " + msg
	}

	username := strings.TrimSpace(getMe.Result.Username)
	if username == "" {
		username = fmt.Sprintf("%d", getMe.Result.ID)
	}
	webhookURL := strings.TrimSpace(webhookInfo.Result.URL)
	if webhookURL != "" {
		return true, fmt.Sprintf(
			"Telegram reachable as @%s, but webhook is set (%s). This backend uses getUpdates polling, so clear webhook with Bot API deleteWebhook.",
			username,
			webhookURL,
		)
	}

	updates, _, err := s.fetchTelegramUpdates(ctx, botToken, 0)
	if err != nil {
		return false, fmt.Sprintf(
			"Telegram reachable as @%s, webhook disabled, but failed to inspect recent chats for outbound test: %s",
			username,
			sanitizeTelegramError(err),
		)
	}

	privateChatID := ""
	for i := len(updates) - 1; i >= 0; i-- {
		msg := primaryTelegramMessage(updates[i])
		if msg == nil || msg.Chat.ID == 0 || msg.From.IsBot {
			continue
		}
		chatType := strings.ToLower(strings.TrimSpace(msg.Chat.Type))
		if chatType != "private" {
			continue
		}
		privateChatID = strconv.FormatInt(msg.Chat.ID, 10)
		break
	}

	if privateChatID == "" {
		lastUpdateID := strings.TrimSpace(integration.Config[telegramLastUpdateIDConfigKey])
		return false, fmt.Sprintf(
			"Telegram reachable as @%s and webhook is disabled, but no private chat was found for direct-message test. Open a private chat with the bot, send /start, then click Test again. last_update_id=%s",
			username,
			lastUpdateID,
		)
	}

	testText := "✅ Telegram test message from A2gent WebApp integration check."
	if err := s.sendTelegramMessage(ctx, botToken, privateChatID, 0, testText); err != nil {
		return false, fmt.Sprintf(
			"Telegram reachable as @%s, but direct-message test failed for private chat %s: %s",
			username,
			privateChatID,
			sanitizeTelegramError(err),
		)
	}

	lastUpdateID := strings.TrimSpace(integration.Config[telegramLastUpdateIDConfigKey])
	return true, fmt.Sprintf(
		"Telegram reachable as @%s. Webhook is disabled. Polling should work. Direct-message test sent to private chat %s. last_update_id=%s pending_webhook_updates=%d",
		username,
		privateChatID,
		lastUpdateID,
		webhookInfo.Result.PendingUpdateCount,
	)
}

func (s *Server) handleDiscoverTelegramChats(w http.ResponseWriter, r *http.Request) {
	var req TelegramChatDiscoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	botToken := s.resolveTelegramDiscoveryToken(req)
	if botToken == "" || botToken == "***" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}

	// Chats the running poller has already seen; it consumes updates, so getUpdates alone often finds nothing.
	var seen []TelegramChatCandidate
	if id := strings.TrimSpace(req.IntegrationID); id != "" {
		if integration, err := s.store.GetIntegration(id); err == nil && integration != nil && integration.Provider == "telegram" {
			seen = telegramSeenChats(integration)
		}
	}

	fresh, fetchErr := fetchTelegramChatCandidates(r.Context(), botToken)
	if fetchErr != nil && len(seen) == 0 {
		s.errorResponse(w, http.StatusBadGateway, fetchErr.Error())
		return
	}

	candidates := mergeTelegramChatCandidates(seen, fresh)
	message := fmt.Sprintf("Found %d chat(s) the bot has seen.", len(candidates))
	if len(candidates) == 0 {
		message = "No chat IDs found yet. Send a message to your bot in Telegram, then try again."
	}
	s.jsonResponse(w, http.StatusOK, TelegramChatDiscoveryResponse{Chats: candidates, Message: message})
}

// fetchTelegramChatCandidates lists chats found in the bot's pending updates.
func fetchTelegramChatCandidates(ctx context.Context, botToken string) ([]TelegramChatCandidate, error) {
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100", botToken), nil)
	if err != nil {
		return nil, fmt.Errorf("Failed to build Telegram request: %w", err)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(apiReq)
	if err != nil {
		return nil, fmt.Errorf("Failed to call Telegram API: %s", sanitizeTelegramError(err))
	}
	defer resp.Body.Close()

	var payload telegramGetUpdatesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("Failed to decode Telegram response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !payload.OK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("Telegram API request failed: %s", msg)
	}

	var candidates []TelegramChatCandidate
	for _, update := range payload.Result {
		for _, message := range []*telegramMessagePayload{update.Message, update.EditedMessage, update.ChannelPost, update.EditedChannelPost} {
			if message == nil || message.Chat.ID == 0 {
				continue
			}
			candidates = append(candidates, telegramChatCandidateFromPayload(message.Chat))
		}
	}
	return candidates, nil
}

// resolveTelegramDiscoveryToken returns the token to use for chat discovery. Saved integrations are shown
// with a masked token in the UI, so the stored one is looked up by integration id in that case.
func (s *Server) resolveTelegramDiscoveryToken(req TelegramChatDiscoveryRequest) string {
	botToken := strings.TrimSpace(req.BotToken)
	if (botToken == "" || botToken == "***") && strings.TrimSpace(req.IntegrationID) != "" {
		if integration, err := s.store.GetIntegration(strings.TrimSpace(req.IntegrationID)); err == nil && integration != nil && integration.Provider == "telegram" {
			botToken = strings.TrimSpace(integration.Config["bot_token"])
		}
	}
	return botToken
}

type telegramBotInfoResponse struct {
	Username  string `json:"username"`
	FirstName string `json:"first_name,omitempty"`
}

// handleTelegramBotInfo returns the bot's public username so the UI can build t.me deep links.
func (s *Server) handleTelegramBotInfo(w http.ResponseWriter, r *http.Request) {
	var req TelegramChatDiscoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	botToken := s.resolveTelegramDiscoveryToken(req)
	if botToken == "" || botToken == "***" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}
	apiReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fmt.Sprintf("https://api.telegram.org/bot%s/getMe", botToken), nil)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build Telegram request: "+err.Error())
		return
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(apiReq)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to call Telegram API: "+sanitizeTelegramError(err))
		return
	}
	defer resp.Body.Close()
	var payload struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to decode Telegram response: "+err.Error())
		return
	}
	if !payload.OK || payload.Result.Username == "" {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = "Telegram did not return a bot username"
		}
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+msg)
		return
	}
	s.jsonResponse(w, http.StatusOK, telegramBotInfoResponse{Username: payload.Result.Username, FirstName: payload.Result.FirstName})
}

type telegramTestMessageRequest struct {
	BotToken      string `json:"bot_token"`
	IntegrationID string `json:"integration_id,omitempty"`
	ChatID        string `json:"chat_id"`
}

// handleTelegramTestMessage sends a short message to one chat so the user can verify the bot can reach it.
// Failures are reported in the body (HTTP 200) because they are expected outcomes, e.g. a bot that was never started.
func (s *Server) handleTelegramTestMessage(w http.ResponseWriter, r *http.Request) {
	var req telegramTestMessageRequest
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

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	text := "✅ Test message from A2gent: this chat can receive messages from the agent."
	if err := s.sendTelegramMessage(ctx, botToken, chatID, 0, text); err != nil {
		s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: false, Message: "Telegram refused the message: " + sanitizeTelegramError(err)})
		return
	}
	s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: true, Message: "Test message sent to chat " + chatID + ". Check Telegram."})
}
