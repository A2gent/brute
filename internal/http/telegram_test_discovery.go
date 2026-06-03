package http

// Telegram connectivity tests and recent-chat discovery endpoints.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

	botToken := strings.TrimSpace(req.BotToken)
	if botToken == "" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}

	apiReq, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100", botToken),
		nil,
	)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build Telegram request: "+err.Error())
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(apiReq)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to call Telegram API: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var payload telegramGetUpdatesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to decode Telegram response: "+err.Error())
		return
	}

	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = resp.Status
		}
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+msg)
		return
	}

	if !payload.OK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = "unknown Telegram API error"
		}
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+msg)
		return
	}

	candidatesByID := map[string]TelegramChatCandidate{}
	for _, update := range payload.Result {
		messages := []*telegramMessagePayload{update.Message, update.EditedMessage, update.ChannelPost, update.EditedChannelPost}
		for _, message := range messages {
			if message == nil || message.Chat.ID == 0 {
				continue
			}
			chatID := fmt.Sprintf("%d", message.Chat.ID)
			if _, exists := candidatesByID[chatID]; exists {
				continue
			}
			candidatesByID[chatID] = TelegramChatCandidate{
				ChatID:    chatID,
				Type:      message.Chat.Type,
				Title:     message.Chat.Title,
				Username:  message.Chat.Username,
				FirstName: message.Chat.FirstName,
				LastName:  message.Chat.LastName,
			}
		}
	}

	candidates := make([]TelegramChatCandidate, 0, len(candidatesByID))
	for _, candidate := range candidatesByID {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ChatID < candidates[j].ChatID
	})

	message := fmt.Sprintf("Found %d chat(s) from recent Telegram updates.", len(candidates))
	if len(candidates) == 0 {
		message = "No chat IDs found yet. Send a message to your bot in Telegram, then try again."
	}

	s.jsonResponse(w, http.StatusOK, TelegramChatDiscoveryResponse{
		Chats:   candidates,
		Message: message,
	})
}
